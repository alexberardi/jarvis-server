package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
)

type mapSource struct {
	mu sync.Mutex
	m  map[string]LabelConfig
}

func (s *mapSource) set(c LabelConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]LabelConfig{}
	}
	s.m[c.Label] = c
}

func (s *mapSource) Label(_ context.Context, label string) (LabelConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := LabelDefFor(label); !ok {
		return LabelConfig{}, ErrUnknownLabel
	}
	if c, ok := s.m[label]; ok {
		return c, nil
	}
	return LabelConfig{Label: label, Engine: ModeOff}, nil
}

// fakeBinaries serves the test binary (the fake engine) for the flavours in have.
type fakeBinaries struct {
	mu   sync.Mutex
	have map[Flavour]bool
}

func (b *fakeBinaries) Path(_ Kind, f Flavour) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.have[f] {
		return "", false
	}
	return os.Args[0], true
}

type rig struct {
	t      *testing.T
	src    *mapSource
	bins   *fakeBinaries
	r      *Resolver
	logDir string
	models string
	fetchQ []string
	mu     sync.Mutex
}

func newRig(t *testing.T) *rig {
	t.Helper()
	t.Setenv("FAKE_ENGINE", "1")
	for _, k := range []string{"CUDA_VISIBLE_DEVICES", "GGML_VK_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES"} {
		if _, ok := os.LookupEnv(k); ok {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	g := &rig{t: t, src: &mapSource{}, bins: &fakeBinaries{have: map[Flavour]bool{FlavourCPU: true, FlavourCUDA: true}},
		logDir: t.TempDir(), models: t.TempDir()}
	t.Setenv("FAKE_ENGINE_LOG", g.logDir)
	g.r = &Resolver{
		Source: g.src, Binaries: g.bins,
		Hardware: func(context.Context) Hardware { return Hardware{Flavour: FlavourCUDA} },
		RequestFetch: func(_ context.Context, k Kind, f Flavour) error {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.fetchQ = append(g.fetchQ, string(k)+"/"+string(f))
			return nil
		},
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		HealthInterval: 20 * time.Millisecond, DrainGrace: 50 * time.Millisecond,
		StartTimeout: 5 * time.Second, StopTimeout: 2 * time.Second,
		Restart:     engines.RestartPolicy{BackoffMin: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond, MaxRestarts: 2, Window: time.Minute},
		RetryFailed: 100 * time.Millisecond,
		Interval:    50 * time.Millisecond,
	}
	t.Cleanup(func() { g.r.StopAll(context.Background()) })
	return g
}

func (g *rig) model(name string) string {
	p := filepath.Join(g.models, name)
	if err := os.WriteFile(p, []byte("gguf"), 0o644); err != nil {
		g.t.Fatal(err)
	}
	return p
}

func (g *rig) llm(label, model string) LabelConfig {
	return LabelConfig{Label: label, Kind: KindLlama, ModelKind: ModelLLM, Engine: ModeLocal, Model: filepath.Base(model),
		ModelPath: model, Context: 4096, Parallel: 2, GPULayers: 999, GPUBackend: "auto", KVCacheType: "f16", FlashAttn: "auto"}
}

// ready resolves until the label is ready.
func (g *rig) ready(label string) Endpoint {
	g.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ep, err := g.r.Resolve(context.Background(), label)
		if err == nil {
			return ep
		}
		if !errors.Is(err, ErrNotReady) || time.Now().After(deadline) {
			g.t.Fatalf("resolve %s: %v", label, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (g *rig) starts() []fakeStart {
	entries, _ := os.ReadDir(g.logDir)
	var out []fakeStart
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(g.logDir, e.Name()))
		var s fakeStart
		if json.Unmarshal(b, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func post(t *testing.T, ep Endpoint, path, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ep.BaseURL+path, strings.NewReader(body))
	if ep.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func instanceFor(r *Resolver, name string) InstanceStatus {
	for _, i := range r.Instances() {
		if i.Name == name {
			return i
		}
	}
	return InstanceStatus{}
}

func TestResolveSharesOneEngine(t *testing.T) {
	g := newRig(t)
	m := g.model("qwen.gguf")
	live := g.llm(LabelLive, m)
	live.GPUDevices = "1"
	g.src.set(live)
	bg := live
	bg.Label = LabelBackground
	g.src.set(bg)

	epLive := g.ready(LabelLive)
	epBg := g.ready(LabelBackground)
	if epLive.Engine != epBg.Engine || epLive.Engine == "" {
		t.Fatalf("not shared: %s vs %s", epLive.Engine, epBg.Engine)
	}
	if epLive.ContextLength != 4096 || epLive.Parallel != 2 || epLive.Vision || epLive.Remote {
		t.Fatalf("endpoint %+v", epBg)
	}
	if !strings.HasPrefix(epLive.BaseURL, "http://127.0.0.1:") || !strings.HasSuffix(epLive.BaseURL, "/v1") || epLive.APIKey == "" {
		t.Fatalf("endpoint %+v", epLive)
	}
	if code, body := post(t, epLive, "/chat/completions", `{}`); code != 200 || !strings.Contains(body, "hi from qwen.gguf") {
		t.Fatalf("%d %s", code, body)
	}
	noKey := epLive
	noKey.APIKey = ""
	if code, _ := post(t, noKey, "/chat/completions", `{}`); code != 401 {
		t.Fatalf("engine must require the key, got %d", code)
	}
	st := g.starts()
	if len(st) != 1 {
		t.Fatalf("%d engine starts, want 1", len(st))
	}
	if st[0].Env["CUDA_VISIBLE_DEVICES"] != "1" || st[0].Env["LLAMA_API_KEY"] != epLive.APIKey {
		t.Fatalf("env %v", st[0].Env)
	}
	if inst := instanceFor(g.r, epLive.Engine); len(inst.Labels) != 2 || inst.Kind != KindLlama || inst.Flavour != FlavourCUDA {
		t.Fatalf("instance %+v", inst)
	}

	// Background gets its own context: a second engine; live's keeps running untouched.
	livePID := instanceFor(g.r, epLive.Engine).PID
	bg.Context = 32768
	g.src.set(bg)
	epBg2 := g.ready(LabelBackground)
	if epBg2.Engine == epLive.Engine {
		t.Fatal("different load settings must not share")
	}
	if instanceFor(g.r, epLive.Engine).PID != livePID {
		t.Fatal("live engine restarted by a background change")
	}
	// And back: background's engine is no longer used and is stopped.
	bg.Context = 4096
	g.src.set(bg)
	if ep := g.ready(LabelBackground); ep.Engine != epLive.Engine {
		t.Fatal("should share again")
	}
	if n := len(g.r.Instances()); n != 1 {
		t.Fatalf("%d instances, want 1", n)
	}
}

func TestResolveReplacesEngine(t *testing.T) {
	g := newRig(t)
	a, b := g.model("a.gguf"), g.model("b.gguf")
	g.src.set(g.llm(LabelLive, a))
	ep1 := g.ready(LabelLive)
	g.src.set(g.llm(LabelLive, b))
	ep2 := g.ready(LabelLive)
	if ep1.Engine == ep2.Engine || ep2.Model != "b.gguf" {
		t.Fatalf("%+v", ep2)
	}
	if insts := g.r.Instances(); len(insts) != 1 || insts[0].Name != ep2.Engine {
		t.Fatalf("old engine kept: %+v", insts)
	}
	if code, body := post(t, ep2, "/chat/completions", `{}`); code != 200 || !strings.Contains(body, "b.gguf") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestResolveStates(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()

	if _, err := g.r.Resolve(ctx, LabelLive); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("off: %v", err)
	}
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindLlama, Engine: ModeLocal})
	if _, err := g.r.Resolve(ctx, LabelLive); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no model: %v", err)
	}
	if _, err := g.r.Resolve(ctx, "nope"); !errors.Is(err, ErrUnknownLabel) {
		t.Fatalf("unknown: %v", err)
	}
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindLlama, Engine: ModeLocal, Model: "x", Problem: "model x: model not installed"})
	var nr *NotReadyError
	if _, err := g.r.Resolve(ctx, LabelLive); !errors.As(err, &nr) || nr.State != "misconfigured" || !errors.Is(err, ErrNotReady) {
		t.Fatalf("problem: %v", err)
	}

	// Remote: no process.
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindLlama, Engine: ModeRemote, RemoteURL: "https://api.openai.com/v1/chat/completions",
		RemoteModel: "gpt-4.1-nano", RemoteAPIKey: "sk-1", RemoteVision: true})
	ep, err := g.r.Resolve(ctx, LabelLive)
	if err != nil || !ep.Remote || ep.BaseURL != "https://api.openai.com/v1" || ep.APIKey != "sk-1" || ep.Model != "gpt-4.1-nano" || !ep.Vision {
		t.Fatalf("remote %+v %v", ep, err)
	}
	if len(g.starts()) != 0 {
		t.Fatal("remote started a process")
	}

	// A flavour with no binary on disk asks for a fetch.
	c := g.llm(LabelBackground, g.model("m.gguf"))
	c.GPUBackend = "vulkan"
	g.src.set(c)
	if _, err := g.r.Resolve(ctx, LabelBackground); !errors.As(err, &nr) || nr.State != "fetching_engine" {
		t.Fatalf("fetch: %v", err)
	}
	if len(g.fetchQ) != 1 || g.fetchQ[0] != "llama-server/vulkan" {
		t.Fatalf("fetch requests %v", g.fetchQ)
	}
	g.bins.mu.Lock()
	g.bins.have[FlavourVulkan] = true
	g.bins.mu.Unlock()
	g.ready(LabelBackground)
	c.GPUBackend = "opencl"
	g.src.set(c)
	if _, err := g.r.Resolve(ctx, LabelBackground); !errors.As(err, &nr) || nr.State != "no_engine_build" {
		t.Fatalf("bad backend: %v", err)
	}
}

func TestResolveWhileLoading(t *testing.T) {
	g := newRig(t)
	t.Setenv("FAKE_ENGINE_LOADING", "300ms")
	g.src.set(g.llm(LabelLive, g.model("big.gguf")))
	var nr *NotReadyError
	if _, err := g.r.Resolve(context.Background(), LabelLive); !errors.As(err, &nr) || nr.State != string(engines.Starting) {
		t.Fatalf("loading: %v", err)
	}
	g.ready(LabelLive)
}

func TestResolveSTTAndEmbeddings(t *testing.T) {
	g := newRig(t)
	g.src.set(LabelConfig{Label: LabelSTT, Kind: KindWhisper, ModelKind: ModelSTT, Engine: ModeLocal, Model: "whisper-base",
		ModelPath: g.model("ggml-base.en.bin"), GPULayers: 999, GPUBackend: "auto"})
	g.src.set(LabelConfig{Label: LabelEmbeddings, Kind: KindLlama, ModelKind: ModelEmbedding, Engine: ModeLocal, Model: "all-minilm-l6-v2",
		ModelPath: g.model("minilm.gguf"), Context: 512, Parallel: 4, GPULayers: 0, Embedding: true, GPUBackend: "auto"})
	stt := g.ready(LabelSTT)
	if stt.Kind != KindWhisper || strings.HasSuffix(stt.BaseURL, "/v1") || stt.APIKey != "" || !strings.HasPrefix(stt.Engine, "whisper-") {
		t.Fatalf("stt %+v", stt)
	}
	if code, body := post(t, stt, "/inference", ""); code != 200 || !strings.Contains(body, "hello world") {
		t.Fatalf("%d %s", code, body)
	}
	emb := g.ready(LabelEmbeddings)
	if !emb.Embeddings || emb.Kind != KindLlama {
		t.Fatalf("emb %+v", emb)
	}
	if code, body := post(t, emb, "/embeddings", `{"input":"x"}`); code != 200 || !strings.Contains(body, "0.6") {
		t.Fatalf("%d %s", code, body)
	}
	var sawEmbed, sawWhisper bool
	for _, s := range g.starts() {
		a := strings.Join(s.Args, " ")
		sawEmbed = sawEmbed || strings.Contains(a, "--embedding --pooling mean")
		sawWhisper = sawWhisper || (strings.Contains(a, "ggml-base.en.bin") && !strings.Contains(a, "--jinja"))
	}
	if !sawEmbed || !sawWhisper {
		t.Fatalf("argv: %+v", g.starts())
	}
	st := g.r.Status(context.Background())
	states := map[string]string{}
	for _, s := range st {
		states[s.Label] = s.State
	}
	if states[LabelSTT] != "ready" || states[LabelEmbeddings] != "ready" || states[LabelLive] != "not_configured" {
		t.Fatalf("status %v", states)
	}
}

func TestEngineCrashAndFailedRetry(t *testing.T) {
	g := newRig(t)
	g.src.set(g.llm(LabelLive, g.model("a.gguf")))
	ep := g.ready(LabelLive)
	pid := instanceFor(g.r, ep.Engine).PID
	p, _ := os.FindProcess(pid)
	p.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if i := instanceFor(g.r, ep.Engine); i.PID != 0 && i.PID != pid && i.State == engines.Healthy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not restarted: %+v", instanceFor(g.r, ep.Engine))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A model that can't load: the supervisor gives up, the resolver retries after a cooldown.
	bad := g.model("bad.gguf")
	c := g.llm(LabelBackground, bad)
	g.src.set(c)
	os.Remove(bad)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.r.Start(ctx)
	var nr *NotReadyError
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, err := g.r.Resolve(context.Background(), LabelBackground)
		if errors.As(err, &nr) && nr.State == string(engines.Failed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never failed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Put the file back: the retry loop brings it up without any further call.
	os.WriteFile(bad, []byte("gguf"), 0o644)
	deadline = time.Now().Add(5 * time.Second)
	for {
		ready := false
		for _, s := range g.r.Instances() {
			if s.Model == bad && s.State == engines.Healthy {
				ready = true
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed engine never retried: %+v", g.r.Instances())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	deadline = time.Now().Add(5 * time.Second)
	for len(g.r.Instances()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("engines not stopped on shutdown")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
