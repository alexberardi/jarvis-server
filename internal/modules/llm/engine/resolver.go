// Package engine runs jarvisd's local inference engines: llama-server (LLM labels, vision,
// embeddings) and whisper-server (speech-to-text), through one code path.
//
//   - release.go: the pinned upstream builds per kind, platform and GPU flavour
//   - binaries.go, download.go, archive.go: fetching a build (resumable, sha256-checked)
//   - gpu.go: GPU detection (subprocess only) and placement proposals
//   - config.go: labels, their settings and the effective LabelConfig
//   - spec.go: LabelConfig → instance key and argv
//   - resolver.go: the Resolver, which runs one supervised process per distinct key (LD1)
//     and answers Resolve(label) with an Endpoint
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
)

// Endpoint is how a caller reaches a label's model.
type Endpoint struct {
	Label string `json:"label"`
	Kind  Kind   `json:"kind"`
	// BaseURL: for LLM and embedding labels, an OpenAI-compatible base including /v1 (append
	// "/chat/completions" or "/embeddings"); for stt, the whisper-server root (POST /inference).
	BaseURL string `json:"base_url"`
	// APIKey goes in "Authorization: Bearer". Local llama-server engines get a random key per
	// instance; whisper-server has none (it binds 127.0.0.1 only).
	APIKey string `json:"-"`
	// Model is the name to send as the request's "model".
	Model      string `json:"model"`
	Vision     bool   `json:"vision"`
	Embeddings bool   `json:"embeddings"`
	Remote     bool   `json:"remote"`
	// ContextLength is the engine's -c (shared by its Parallel slots); 0 when unknown (remote).
	ContextLength int `json:"context_length"`
	Parallel      int `json:"parallel"`
	// Engine is the supervised instance's name ("" for remote). Labels sharing an engine
	// report the same name.
	Engine string `json:"engine,omitempty"`
	// Degraded is set while the engine fails health checks but hasn't been restarted yet.
	Degraded bool `json:"degraded,omitempty"`
}

// ErrNotConfigured is returned for a label with no model (or engine=off): 01 §3.9 "not
// configured". It is not an error state; the label simply isn't set up.
var ErrNotConfigured = errors.New("not configured")

// ErrNotReady is matched (errors.Is) by every NotReadyError.
var ErrNotReady = errors.New("not ready")

// NotReadyError says why a configured label can't take requests yet. Track B maps it to 503
// model_not_loaded with State.
type NotReadyError struct {
	Label string
	// State is the engine state (starting, restarting, draining, failed, stopped) or one of
	// "fetching_engine", "no_engine_build", "misconfigured".
	State  string
	Reason string
}

func (e *NotReadyError) Error() string {
	s := fmt.Sprintf("label %s not ready: %s", e.Label, e.State)
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// BinaryProvider finds installed engine builds.
type BinaryProvider interface {
	Path(k Kind, f Flavour) (string, bool)
}

// Resolver maps labels to engines. Labels whose configs yield the same instance key share
// one supervised process; a config change starts the new engine (after stopping one that no
// label uses any more, to free its GPU memory) and touches no other label's engine.
type Resolver struct {
	Source   ConfigSource
	Binaries BinaryProvider
	// Hardware returns the (cached) detection, for gpu_backend=auto.
	Hardware func(ctx context.Context) Hardware
	// RequestFetch asks for a missing build to be downloaded (a deduplicated queue job).
	RequestFetch func(ctx context.Context, k Kind, f Flavour) error
	Log          *slog.Logger

	// Engine tunables; zero values take production defaults.
	HealthInterval time.Duration // default 5s
	DrainGrace     time.Duration // default 30s
	StartTimeout   time.Duration // default 10m (big GGUFs load slowly)
	StopTimeout    time.Duration // default 10s
	Restart        engines.RestartPolicy
	// RetryFailed is the base cooldown before a failed engine is tried again (doubling up to
	// 10× it). Default 60s, as the legacy model service.
	RetryFailed time.Duration
	// Interval is how often Start's loop reconciles; default 15s.
	Interval time.Duration

	mu        sync.Mutex
	instances map[string]*instance // by key hash
	bound     map[string]string    // label -> key hash
	wake      chan struct{}
}

type instance struct {
	key    instanceKey
	hash   string
	name   string
	port   int
	apiKey string
	alias  string
	sup    *engines.Supervisor
	// retries counts failed-engine retries, for the cooldown.
	retries int
}

func (r *Resolver) init() {
	if r.instances == nil {
		r.instances = map[string]*instance{}
		r.bound = map[string]string{}
		r.wake = make(chan struct{}, 1)
	}
	if r.Log == nil {
		r.Log = slog.Default()
	}
}

// Resolve returns the endpoint for a label, starting its engine if needed. It returns
// ErrNotConfigured, a *NotReadyError (errors.Is ErrNotReady) while the engine loads or is
// down, or ErrUnknownLabel.
func (r *Resolver) Resolve(ctx context.Context, label string) (Endpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	ep, inst, err := r.ensure(ctx, label)
	r.gc(ctx)
	if err != nil || inst == nil {
		return ep, err
	}
	st := inst.sup.Status()
	switch st.State {
	case engines.Healthy:
	case engines.Unhealthy:
		ep.Degraded = true
	default:
		return Endpoint{}, &NotReadyError{Label: label, State: string(st.State), Reason: st.LastError}
	}
	return ep, nil
}

// ensure brings label's engine into line with its config. It returns the endpoint (without
// readiness) and, for local labels, the instance. r.mu is held.
func (r *Resolver) ensure(ctx context.Context, label string) (Endpoint, *instance, error) {
	c, err := r.Source.Label(ctx, label)
	if err != nil {
		return Endpoint{}, nil, err
	}
	ep := Endpoint{Label: label, Kind: c.Kind, Embeddings: c.Embedding}
	switch {
	case c.Engine == ModeOff:
		delete(r.bound, label)
		return ep, nil, ErrNotConfigured
	case c.Engine == ModeRemote:
		delete(r.bound, label)
		if c.RemoteURL == "" {
			return ep, nil, ErrNotConfigured
		}
		ep.Remote, ep.BaseURL, ep.APIKey, ep.Model = true, NormalizeBaseURL(c.RemoteURL), c.RemoteAPIKey, c.RemoteModel
		ep.Vision = c.RemoteVision
		return ep, nil, nil
	case c.Problem != "":
		delete(r.bound, label)
		return ep, nil, &NotReadyError{Label: label, State: "misconfigured", Reason: c.Problem}
	case c.Engine != ModeLocal:
		delete(r.bound, label)
		return ep, nil, &NotReadyError{Label: label, State: "misconfigured", Reason: "unknown engine mode " + c.Engine}
	case c.ModelPath == "":
		delete(r.bound, label)
		return ep, nil, ErrNotConfigured
	}

	f, err := r.flavour(ctx, c)
	if err != nil {
		delete(r.bound, label)
		return ep, nil, &NotReadyError{Label: label, State: "no_engine_build", Reason: err.Error()}
	}
	bin, ok := r.Binaries.Path(c.Kind, f)
	if !ok {
		delete(r.bound, label)
		if r.RequestFetch != nil {
			if err := r.RequestFetch(ctx, c.Kind, f); err != nil {
				r.Log.Warn("engine fetch request failed", "kind", c.Kind, "flavour", f, "err", err)
			}
		}
		return ep, nil, &NotReadyError{Label: label, State: "fetching_engine",
			Reason: fmt.Sprintf("downloading %s %s build %s", c.Kind, f, Releases[c.Kind].Build)}
	}
	key := keyFor(c, f, bin)
	h := key.hash()
	inst := r.instances[h]
	if inst == nil {
		// Free whatever this label held alone before loading the replacement.
		if old, ok := r.bound[label]; ok && old != h && r.users(old) == 1 {
			delete(r.bound, label)
			r.stop(ctx, old)
		}
		inst, err = r.start(c, key, h)
		if err != nil {
			delete(r.bound, label)
			return ep, nil, &NotReadyError{Label: label, State: string(engines.Failed), Reason: err.Error()}
		}
	}
	r.bound[label] = h
	ep.Engine, ep.APIKey, ep.Model = inst.name, inst.apiKey, inst.alias
	ep.ContextLength, ep.Parallel = key.Context, key.Parallel
	ep.Vision = key.MMProj != ""
	if c.Kind == KindWhisper {
		ep.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", inst.port)
	} else {
		ep.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", inst.port)
	}
	return ep, inst, nil
}

// NormalizeBaseURL turns a pasted endpoint into an OpenAI base URL: trailing slashes and a
// trailing /chat/completions (CI's legacy JARVIS_REST_MODEL_URL) are dropped.
func NormalizeBaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, suf := range []string{"/chat/completions", "/embeddings", "/completions"} {
		u = strings.TrimSuffix(u, suf)
	}
	return strings.TrimRight(u, "/")
}

// flavour picks the build for a label: the configured backend, or detection's proposal when
// auto. With auto, a kind that has no build for the detected flavour (whisper on a CUDA linux
// box) falls back to CPU unless an operator binary is configured.
func (r *Resolver) flavour(ctx context.Context, c LabelConfig) (Flavour, error) {
	f, err := ParseFlavour(c.GPUBackend)
	if err != nil {
		return "", err
	}
	if f != "" {
		return f, nil
	}
	if r.Hardware != nil {
		f = r.Hardware(ctx).Flavour
	}
	if f == "" {
		f = FlavourCPU
	}
	if _, ok := r.Binaries.Path(c.Kind, f); ok {
		return f, nil
	}
	if _, err := AssetsFor(c.Kind, Host(), f); err != nil {
		if _, err := AssetsFor(c.Kind, Host(), FlavourCPU); err == nil {
			return FlavourCPU, nil
		}
		return "", err
	}
	return f, nil
}

func (r *Resolver) users(h string) int {
	n := 0
	for _, b := range r.bound {
		if b == h {
			n++
		}
	}
	return n
}

func shortKind(k Kind) string {
	if k == KindWhisper {
		return "whisper"
	}
	return "llama"
}

func (r *Resolver) start(c LabelConfig, key instanceKey, h string) (*instance, error) {
	port, err := engines.FreePort()
	if err != nil {
		return nil, err
	}
	inst := &instance{key: key, hash: h, name: shortKind(c.Kind) + "-" + h[:8], port: port, alias: c.Model}
	if inst.alias == "" {
		inst.alias = filepath.Base(c.ModelPath)
	}
	env := map[string]string{}
	if c.Kind == KindLlama {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		inst.apiKey = hex.EncodeToString(b)
		env["LLAMA_API_KEY"] = inst.apiKey // not on argv, so `ps` doesn't show it
	}
	dir := filepath.Dir(key.Binary)
	if runtime.GOOS == "linux" {
		// The CUDA runtime ships beside the binaries.
		ld := dir
		if cur := os.Getenv("LD_LIBRARY_PATH"); cur != "" {
			ld += ":" + cur
		}
		env["LD_LIBRARY_PATH"] = ld
	}
	args, err := key.args(port, inst.alias)
	if err != nil {
		return nil, err
	}
	def := func(v, d time.Duration) time.Duration {
		if v <= 0 {
			return d
		}
		return v
	}
	spec := engines.Spec{
		Name: inst.name, Path: key.Binary, Args: args, Env: env, Dir: dir,
		GPU: engines.GPU{Backend: key.Flavour.GPUBackend(), VisibleDevices: key.Devices},
		Health: &engines.HealthCheck{
			URL:        fmt.Sprintf("http://127.0.0.1:%d/health", port),
			Interval:   def(r.HealthInterval, 5*time.Second),
			DrainGrace: def(r.DrainGrace, 30*time.Second),
		},
		StartTimeout: def(r.StartTimeout, 10*time.Minute),
		StopTimeout:  def(r.StopTimeout, 10*time.Second),
		Restart:      r.Restart,
	}
	sup, err := engines.New(spec, r.Log)
	if err != nil {
		return nil, err
	}
	r.Log.Info("starting engine", "engine", inst.name, "kind", c.Kind, "flavour", key.Flavour,
		"model", key.Model, "devices", key.Devices, "port", port)
	inst.sup = sup
	r.instances[h] = inst
	if err := sup.Start(); err != nil {
		// The supervisor records Failed; the retry loop tries again after the cooldown.
		r.Log.Error("engine failed to start", "engine", inst.name, "err", err)
	}
	return inst, nil
}

func (r *Resolver) stop(ctx context.Context, h string) {
	inst := r.instances[h]
	if inst == nil {
		return
	}
	delete(r.instances, h)
	r.Log.Info("stopping engine", "engine", inst.name)
	stopT := r.StopTimeout
	if stopT <= 0 {
		stopT = 10 * time.Second
	}
	sctx, cancel := context.WithTimeout(ctx, stopT+5*time.Second)
	defer cancel()
	if err := inst.sup.Stop(sctx); err != nil {
		r.Log.Warn("engine stop", "engine", inst.name, "err", err)
	}
}

// gc stops instances no label uses. r.mu is held.
func (r *Resolver) gc(ctx context.Context) {
	for h := range r.instances {
		if r.users(h) == 0 {
			r.stop(ctx, h)
		}
	}
}

// Reconcile brings every label's engine into line with the current settings, retries failed
// engines after their cooldown, and stops engines no label uses.
func (r *Resolver) Reconcile(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	for _, d := range LabelDefs {
		if _, _, err := r.ensure(ctx, d.Name); err != nil && !errors.Is(err, ErrNotConfigured) {
			r.Log.Debug("label not ready", "label", d.Name, "err", err)
		}
	}
	r.gc(ctx)
	base := r.RetryFailed
	if base <= 0 {
		base = time.Minute
	}
	for _, inst := range r.instances {
		st := inst.sup.Status()
		if st.State != engines.Failed {
			if st.State == engines.Healthy {
				inst.retries = 0
			}
			continue
		}
		cool := base << min(inst.retries, 3)
		cool = min(cool, 10*base)
		if time.Since(st.Since) < cool {
			continue
		}
		inst.retries++
		r.Log.Info("retrying failed engine", "engine", inst.name, "attempt", inst.retries)
		if err := inst.sup.Restart(ctx); err != nil {
			r.Log.Warn("engine retry failed", "engine", inst.name, "err", err)
		}
	}
}

// Notify asks the running loop to reconcile now (after a settings write, an install or a
// fetched engine).
func (r *Resolver) Notify() {
	r.mu.Lock()
	r.init()
	ch := r.wake
	r.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Start reconciles now and then every Interval until ctx ends, when it stops every engine.
// It returns at once.
func (r *Resolver) Start(ctx context.Context) error {
	r.mu.Lock()
	r.init()
	wake := r.wake
	r.mu.Unlock()
	every := r.Interval
	if every <= 0 {
		every = 15 * time.Second
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			r.Reconcile(ctx)
			select {
			case <-ctx.Done():
				sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.StopAll(sctx)
				cancel()
				return
			case <-t.C:
			case <-wake:
			}
		}
	}()
	return nil
}

// StopAll stops every engine and forgets the bindings.
func (r *Resolver) StopAll(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	clear(r.bound)
	var wg sync.WaitGroup
	for h, inst := range r.instances {
		delete(r.instances, h)
		wg.Go(func() { _ = inst.sup.Stop(ctx) })
	}
	wg.Wait()
}

// LabelStatus is one label's state for admin and /health.
type LabelStatus struct {
	Label  string      `json:"label"`
	Config LabelConfig `json:"config"`
	// State: ready, degraded, not_configured, remote, or a NotReadyError state.
	State    string    `json:"state"`
	Reason   string    `json:"reason,omitempty"`
	Endpoint *Endpoint `json:"endpoint,omitempty"`
}

// InstanceStatus is one supervised engine.
type InstanceStatus struct {
	Name      string        `json:"name"`
	State     engines.State `json:"state"`
	PID       int           `json:"pid"`
	Restarts  int           `json:"restarts"`
	Since     time.Time     `json:"since"`
	LastError string        `json:"last_error,omitempty"`
	Output    []string      `json:"output"`
	Kind      Kind          `json:"kind"`
	Flavour   Flavour       `json:"flavour"`
	Port      int           `json:"port"`
	Model     string        `json:"model"`
	Labels    []string      `json:"labels"`
	Args      []string      `json:"args"`
}

// Status reports every label against the current settings (it resolves each, so a changed
// setting takes effect here too).
func (r *Resolver) Status(ctx context.Context) []LabelStatus {
	var out []LabelStatus
	for _, d := range LabelDefs {
		ls := LabelStatus{Label: d.Name}
		c, err := r.Source.Label(ctx, d.Name)
		if err != nil {
			ls.State, ls.Reason = "error", err.Error()
			out = append(out, ls)
			continue
		}
		c.RemoteAPIKey = ""
		ls.Config = c
		ep, err := r.Resolve(ctx, d.Name)
		var nr *NotReadyError
		switch {
		case errors.Is(err, ErrNotConfigured):
			ls.State = "not_configured"
		case errors.As(err, &nr):
			ls.State, ls.Reason = nr.State, nr.Reason
		case err != nil:
			ls.State, ls.Reason = "error", err.Error()
		default:
			ep.APIKey = ""
			ls.Endpoint = &ep
			switch {
			case ep.Remote:
				ls.State = "remote"
			case ep.Degraded:
				ls.State = "degraded"
			default:
				ls.State = "ready"
			}
		}
		out = append(out, ls)
	}
	return out
}

// Instances reports the supervised engines, sorted by name.
func (r *Resolver) Instances() []InstanceStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	var out []InstanceStatus
	for h, inst := range r.instances {
		var labels []string
		for l, b := range r.bound {
			if b == h {
				labels = append(labels, l)
			}
		}
		slices.Sort(labels)
		args, _ := inst.key.args(inst.port, inst.alias)
		st := inst.sup.Status()
		out = append(out, InstanceStatus{Name: st.Name, State: st.State, PID: st.PID, Restarts: st.Restarts,
			Since: st.Since, LastError: st.LastError, Output: st.Output, Kind: inst.key.Kind, Flavour: inst.key.Flavour,
			Port: inst.port, Model: inst.key.Model, Labels: labels, Args: args})
	}
	slices.SortFunc(out, func(a, b InstanceStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}
