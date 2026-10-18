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
	"maps"
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
	// APIKey goes in "Authorization: Bearer" (x-api-key for API=anthropic). Local llama-server engines get a random key per
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
	// FoldSystemMessages: requests must be folded for a strict chat template (ID12).
	FoldSystemMessages bool `json:"fold_system_messages,omitempty"`
	// API is the dialect a remote endpoint speaks: APIOpenAI or APIAnthropic (remote_api).
	// Local engines leave it empty (OpenAI-compatible).
	API string `json:"api,omitempty"`
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
	// 8× it). Default 60s, as the legacy model service.
	RetryFailed time.Duration
	// MaxRetries is how many times a failed engine is retried before the resolver gives up
	// and leaves it failed until its settings change or Retry is called. Default 3;
	// negative = never give up.
	MaxRetries int
	// Interval is how often Start's loop reconciles; default 15s.
	Interval time.Duration
	// TemplateDir holds the pinned chat templates engines are launched with
	// (<home>/templates); default <tmp>/jarvisd-chat-templates.
	TemplateDir string
	// PIDDir holds a pid file per running engine (<home>/engines/run). The first use of the
	// resolver kills engines a previous jarvisd left there (engines.Sweep): on macOS they
	// outlive a crashed jarvisd. Empty = no pid files.
	PIDDir string
	// Memory persists where each label last ran healthy on a GPU, so a GPU that disappears
	// later is noticed even when the label's settings are all "auto" (nil = in memory only).
	Memory GPUMemory

	// mu guards the maps below. It is never held while an engine stops (that waits up to
	// StopTimeout): engines are detached from instances under mu and stopped after it is
	// released, so other labels resolve meanwhile.
	mu        sync.Mutex
	instances map[string]*instance // by key hash
	bound     map[string]string    // label -> key hash
	wake      chan struct{}
	// stopping holds the detached engines whose stop is under way; each channel closes once its
	// engine has exited. No new engine starts while one is stopping, so a replacement loads
	// only after the GPU memory is freed.
	stopping map[*instance]chan struct{}
	// detached are the engines detached since mu was taken, for release to stop.
	detached []*instance
	// last is each label's latest ensure outcome, which Status reports for local labels
	// without an engine.
	last map[string]error
	// lastGood is Memory's content: label -> the GPU placement it last ran healthy on.
	lastGood map[string]Placement
	// gpuDown remembers labels refused for a lost GPU, to log each loss once.
	gpuDown map[string]string
}

type instance struct {
	key    instanceKey
	hash   string
	name   string
	port   int
	apiKey string
	alias  string
	sup    *engines.Supervisor
	// retries counts failed-engine retries, for the cooldown and MaxRetries.
	retries int
	// gaveUp is set once retries reach MaxRetries: the engine stays failed until Retry.
	gaveUp bool
}

// ErrUnknownEngine is returned by Retry for an engine name the resolver doesn't run.
var ErrUnknownEngine = errors.New("unknown engine")

// ErrNotFailed is returned by Retry for an engine that hasn't failed: there is nothing to retry.
var ErrNotFailed = errors.New("engine has not failed")

func (r *Resolver) init() {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.instances == nil {
		r.instances = map[string]*instance{}
		r.bound = map[string]string{}
		r.wake = make(chan struct{}, 1)
		r.stopping = map[*instance]chan struct{}{}
		r.last = map[string]error{}
		// Before any engine starts, so only a previous jarvisd's are in the dir.
		if n := engines.Sweep(r.PIDDir, r.Log); n > 0 {
			r.Log.Warn("killed engines left running by a previous jarvisd", "count", n)
		}
	}
	if r.lastGood == nil {
		r.lastGood = map[string]Placement{}
		r.gpuDown = map[string]string{}
		if r.Memory != nil {
			m, err := r.Memory.Load()
			if err != nil {
				r.Log.Warn("reading the last-known GPU placements", "err", err)
			}
			maps.Copy(r.lastGood, m)
		}
	}
}

// Resolve returns the endpoint for a label, starting its engine if needed. It returns
// ErrNotConfigured, a *NotReadyError (errors.Is ErrNotReady) while the engine loads or is
// down, or ErrUnknownLabel.
func (r *Resolver) Resolve(ctx context.Context, label string) (Endpoint, error) {
	for {
		r.mu.Lock()
		r.init()
		ep, inst, err := r.ensure(ctx, label)
		r.gc()
		var p parked
		if inst != nil {
			p = inst.parked()
		}
		var wait []chan struct{}
		if isStopWait(err) {
			wait = slices.Collect(maps.Values(r.stopping))
		}
		r.release(ctx)
		if wait != nil {
			if waitAll(ctx, wait) != nil {
				return Endpoint{}, err.(stopWait).NotReadyError
			}
			continue
		}
		if err != nil || inst == nil {
			return ep, err
		}
		return ready(ep, inst, p)
	}
}

// parked is whether the resolver gave up on an engine, read under r.mu for ready.
type parked struct {
	gaveUp  bool
	retries int
}

func (inst *instance) parked() parked { return parked{inst.gaveUp, inst.retries} }

// ready reports a local label's endpoint by its engine's state; p is its parked snapshot.
func ready(ep Endpoint, inst *instance, p parked) (Endpoint, error) {
	st := inst.sup.Status()
	switch st.State {
	case engines.Healthy:
	case engines.Unhealthy:
		ep.Degraded = true
	default:
		reason := st.LastError
		if p.gaveUp {
			reason = fmt.Sprintf("gave up after %d retries (%s); fix the settings or press Retry", p.retries, st.LastError)
		}
		return Endpoint{}, &NotReadyError{Label: ep.Label, State: string(st.State), Reason: reason}
	}
	return ep, nil
}

// stopWait is ensure's answer while the engine a label needs can't start yet because another
// one is stopping: the caller waits for the stops and tries again.
type stopWait struct{ *NotReadyError }

func (e stopWait) Unwrap() error { return e.NotReadyError }

func isStopWait(err error) bool {
	var w stopWait
	return errors.As(err, &w)
}

// waitAll waits for every channel to close, or for ctx.
func waitAll(ctx context.Context, chs []chan struct{}) error {
	for _, ch := range chs {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// release unlocks r.mu and then stops the engines detached while it was held (in parallel,
// waiting for them).
func (r *Resolver) release(ctx context.Context) {
	ds := r.detached
	r.detached = nil
	r.mu.Unlock()
	if len(ds) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, inst := range ds {
		wg.Go(func() { r.halt(ctx, inst) })
	}
	wg.Wait()
	r.mu.Lock()
	for _, inst := range ds {
		close(r.stopping[inst])
		delete(r.stopping, inst)
	}
	r.mu.Unlock()
}

// ensure brings label's engine into line with its config and records the outcome for Status.
// It returns the endpoint (without readiness) and, for local labels, the instance. r.mu is
// held.
func (r *Resolver) ensure(ctx context.Context, label string) (Endpoint, *instance, error) {
	ep, inst, err := r.apply(ctx, label)
	r.last[label] = err
	return ep, inst, err
}

// describe answers for a label that runs no local engine (off, remote, misconfigured, no
// model); local is true otherwise, with ep holding only the label's identity.
func describe(c LabelConfig) (ep Endpoint, local bool, err error) {
	ep = Endpoint{Label: c.Label, Kind: c.Kind, Embeddings: c.Embedding}
	switch {
	case c.Engine == ModeOff:
		return ep, false, ErrNotConfigured
	case c.Engine == ModeRemote:
		if c.RemoteURL == "" {
			return ep, false, ErrNotConfigured
		}
		api := RemoteAPI(c.RemoteAPIMode, c.RemoteURL)
		if c.Embedding && api == APIAnthropic {
			return ep, false, &NotReadyError{Label: c.Label, State: "misconfigured",
				Reason: "the Anthropic API has no embeddings endpoint; use a local engine or an OpenAI-compatible remote (remote_api=openai)"}
		}
		ep.Remote, ep.BaseURL, ep.APIKey, ep.Model = true, NormalizeBaseURL(c.RemoteURL), c.RemoteAPIKey, c.RemoteModel
		ep.Vision, ep.FoldSystemMessages, ep.API = c.RemoteVision, c.FoldSystemMessages, api
		// A remote's window is what the operator says it is (llm.<slot>.context; 0 = unknown):
		// cc compaction needs it (docs/cc/chat-images.md §7).
		ep.ContextLength, ep.Parallel = c.Context, 1
		return ep, false, nil
	case c.Problem != "":
		return ep, false, &NotReadyError{Label: c.Label, State: "misconfigured", Reason: c.Problem}
	case c.Engine != ModeLocal:
		return ep, false, &NotReadyError{Label: c.Label, State: "misconfigured", Reason: "unknown engine mode " + c.Engine}
	case c.ModelPath == "":
		return ep, false, ErrNotConfigured
	}
	return ep, true, nil
}

func (r *Resolver) apply(ctx context.Context, label string) (Endpoint, *instance, error) {
	c, err := r.Source.Label(ctx, label)
	if err != nil {
		return Endpoint{}, nil, err
	}
	ep, local, err := describe(c)
	if !local {
		delete(r.bound, label)
		return ep, nil, err
	}

	f, err := r.flavour(ctx, c)
	if err != nil {
		delete(r.bound, label)
		return ep, nil, &NotReadyError{Label: label, State: "no_engine_build", Reason: err.Error()}
	}
	if nr, keep := r.gpuGate(ctx, label, c); nr != nil {
		delete(r.bound, label)
		return ep, nil, nr
	} else if keep != nil {
		return r.fill(ep, c, keep), keep, nil
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
	if c.ChatTemplate != "" && c.Kind == KindLlama && !c.Embedding {
		if key.ChatTemplate, err = r.templateFile(c.ChatTemplate); err != nil {
			delete(r.bound, label)
			return ep, nil, &NotReadyError{Label: label, State: "misconfigured", Reason: "chat template: " + err.Error()}
		}
	}
	h := key.hash()
	inst := r.instances[h]
	if inst == nil {
		// Free whatever this label held alone before loading the replacement, and load nothing
		// while an engine is still stopping.
		if old, ok := r.bound[label]; ok && old != h && r.users(old) == 1 {
			delete(r.bound, label)
			r.detach(old)
		}
		if len(r.stopping) > 0 {
			delete(r.bound, label)
			return ep, nil, stopWait{&NotReadyError{Label: label, State: string(engines.Restarting),
				Reason: "waiting for the previous engine to stop"}}
		}
		inst, err = r.start(c, key, h)
		if err != nil {
			delete(r.bound, label)
			return ep, nil, &NotReadyError{Label: label, State: string(engines.Failed), Reason: err.Error()}
		}
	}
	r.bound[label] = h
	return r.fill(ep, c, inst), inst, nil
}

// fill completes a local label's endpoint from its instance.
func (r *Resolver) fill(ep Endpoint, c LabelConfig, inst *instance) Endpoint {
	ep.Engine, ep.APIKey, ep.Model = inst.name, inst.apiKey, inst.alias
	ep.FoldSystemMessages = c.FoldSystemMessages
	ep.ContextLength, ep.Parallel = inst.key.Context, inst.key.Parallel
	ep.Vision = inst.key.MMProj != ""
	if c.Kind == KindWhisper {
		ep.BaseURL = fmt.Sprintf("http://127.0.0.1:%d", inst.port)
	} else {
		ep.BaseURL = fmt.Sprintf("http://127.0.0.1:%d/v1", inst.port)
	}
	return ep
}

// NormalizeBaseURL turns a pasted endpoint into a base URL: trailing slashes and a trailing
// /chat/completions (CI's legacy JARVIS_REST_MODEL_URL), /embeddings or Anthropic's /messages
// are dropped.
func NormalizeBaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	for _, suf := range []string{"/chat/completions", "/embeddings", "/completions", "/messages"} {
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
		PIDDir:       r.PIDDir,
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

// detach takes an engine out of service for release to stop. r.mu is held.
func (r *Resolver) detach(h string) {
	inst := r.instances[h]
	if inst == nil {
		return
	}
	delete(r.instances, h)
	r.stopping[inst] = make(chan struct{})
	r.detached = append(r.detached, inst)
}

// halt stops a detached engine. r.mu is not held. The stop runs to its end even if the
// caller's ctx is cancelled: a replacement waits for it.
func (r *Resolver) halt(ctx context.Context, inst *instance) {
	r.Log.Info("stopping engine", "engine", inst.name)
	stopT := r.StopTimeout
	if stopT <= 0 {
		stopT = 10 * time.Second
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopT+5*time.Second)
	defer cancel()
	if err := inst.sup.Stop(sctx); err != nil {
		r.Log.Warn("engine stop", "engine", inst.name, "err", err)
	}
}

// gc detaches instances no label uses. r.mu is held.
func (r *Resolver) gc() {
	for h := range r.instances {
		if r.users(h) == 0 {
			r.detach(h)
		}
	}
}

// Reconcile brings every label's engine into line with the current settings, retries failed
// engines after their cooldown, and stops engines no label uses.
func (r *Resolver) Reconcile(ctx context.Context) {
	for {
		r.mu.Lock()
		r.init()
		waiting := false
		for _, d := range LabelDefs {
			_, _, err := r.ensure(ctx, d.Name)
			waiting = waiting || isStopWait(err)
			if err != nil && !errors.Is(err, ErrNotConfigured) {
				r.Log.Debug("label not ready", "label", d.Name, "err", err)
			}
		}
		r.gc()
		r.recordGPU(ctx)
		r.retryFailed()
		var wait []chan struct{}
		if waiting {
			wait = slices.Collect(maps.Values(r.stopping))
		}
		r.release(ctx)
		if wait == nil || waitAll(ctx, wait) != nil {
			return
		}
	}
}

// retryFailed restarts failed engines after their cooldown. A failed supervisor has no
// process, so Start only launches one and returns: it is safe under r.mu. r.mu is held.
func (r *Resolver) retryFailed() {
	base := r.RetryFailed
	if base <= 0 {
		base = time.Minute
	}
	maxRetries := r.MaxRetries
	if maxRetries == 0 {
		maxRetries = 3
	}
	for _, inst := range r.instances {
		st := inst.sup.Status()
		if st.State != engines.Failed {
			if st.State == engines.Healthy {
				inst.retries = 0
			}
			continue
		}
		if inst.gaveUp {
			continue
		}
		if maxRetries >= 0 && inst.retries >= maxRetries {
			inst.gaveUp = true
			r.Log.Error("engine gave up: still failing after retries", "engine", inst.name, "retries", inst.retries, "err", st.LastError)
			continue
		}
		if time.Since(st.Since) < retryCooldown(base, inst.retries) {
			continue
		}
		inst.retries++
		r.Log.Info("retrying failed engine", "engine", inst.name, "attempt", inst.retries)
		if err := inst.sup.Start(); err != nil {
			r.Log.Warn("engine retry failed", "engine", inst.name, "err", err)
		}
	}
}

// retryCooldown is the wait before resolver retry retries+1: base doubling, up to 8× base.
func retryCooldown(base time.Duration, retries int) time.Duration {
	return base << min(retries, 3)
}

// Retry starts a failed engine (one the resolver gave up on, or one still waiting out its
// cooldown) again with a fresh retry budget: the admin's "Retry" after fixing what broke it.
// A failed supervisor has no process, so this never waits on a stop under r.mu (#207).
// ErrUnknownEngine if name isn't running, ErrNotFailed if it hasn't failed.
func (r *Resolver) Retry(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	for _, inst := range r.instances {
		if inst.name != name {
			continue
		}
		if inst.sup.Status().State != engines.Failed {
			return fmt.Errorf("%w: %q", ErrNotFailed, name)
		}
		inst.retries, inst.gaveUp = 0, false
		r.Log.Info("retrying engine on request", "engine", inst.name)
		return inst.sup.Start()
	}
	return fmt.Errorf("%w %q", ErrUnknownEngine, name)
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
	wait := slices.Collect(maps.Values(r.stopping))
	r.mu.Unlock()
	wg.Wait()
	_ = waitAll(ctx, wait) // stops already under way
	r.mu.Lock()
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
	Started   time.Time     `json:"started,omitzero"` // process launch; zero when none runs
	LastError string        `json:"last_error,omitempty"`
	// Retries counts the resolver's retries of this failed engine; GaveUp is set once they
	// reach MaxRetries and the engine is left failed until Retry or a settings change.
	Retries int      `json:"retries"`
	GaveUp  bool     `json:"gave_up"`
	Output  []string `json:"output"`
	Kind    Kind     `json:"kind"`
	Flavour Flavour  `json:"flavour"`
	Port    int      `json:"port"`
	Model   string   `json:"model"`
	Labels  []string `json:"labels"`
	Args    []string `json:"args"`
}

// Status reports every label against the current settings. It only reads: a local label is
// reported by the engine it is bound to, or by its last Resolve/Reconcile outcome, so a
// changed setting shows once the reconcile loop (Notify) has applied it.
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
		ep, err := r.peek(c)
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

// peek is Resolve without side effects, for Status.
func (r *Resolver) peek(c LabelConfig) (Endpoint, error) {
	ep, local, err := describe(c)
	if !local {
		return ep, err
	}
	r.mu.Lock()
	r.init()
	inst := r.instances[r.bound[c.Label]]
	last, seen := r.last[c.Label]
	var p parked
	if inst != nil {
		p = inst.parked()
	}
	r.mu.Unlock()
	switch {
	case inst != nil:
		return ready(r.fill(ep, c, inst), inst, p)
	case seen && last != nil:
		return Endpoint{}, last
	}
	return Endpoint{}, &NotReadyError{Label: c.Label, State: string(engines.Stopped), Reason: "no engine running"}
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
			Since: st.Since, Started: st.Started, LastError: st.LastError, Retries: inst.retries, GaveUp: inst.gaveUp, Output: st.Output, Kind: inst.key.Kind, Flavour: inst.key.Flavour,
			Port: inst.port, Model: inst.key.Model, Labels: labels, Args: args})
	}
	slices.SortFunc(out, func(a, b InstanceStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}
