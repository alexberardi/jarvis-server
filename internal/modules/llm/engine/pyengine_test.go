package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// runFakeUV is uv for the Python engine install tests: `python install V`, `venv --python V
// DIR` (DIR/bin/python is the fake engine) and `pip install … -r FILE`. Each call is logged
// to $FAKE_UV_LOG/<subcommand>.json. FAKE_UV_FAIL=<subcommand> makes that step fail.
func runFakeUV(args []string) int {
	if len(args) == 0 {
		return 2
	}
	step := args[0]
	if dir := os.Getenv("FAKE_UV_LOG"); dir != "" {
		env := map[string]string{}
		for _, kv := range os.Environ() {
			if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "UV_") || strings.HasPrefix(k, "PYTHON") {
				env[k] = v
			}
		}
		b, _ := json.Marshal(fakeStart{Args: args, Env: env})
		_ = os.WriteFile(filepath.Join(dir, step+".json"), b, 0o644)
	}
	if os.Getenv("FAKE_UV_FAIL") == step {
		fmt.Fprintln(os.Stderr, "error: no matching distribution found for mlx-metal")
		return 1
	}
	switch step {
	case "python":
		if err := os.MkdirAll(filepath.Join(os.Getenv("UV_PYTHON_INSTALL_DIR"), "cpython-"+args[len(args)-1]), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	case "venv":
		dir := args[len(args)-1]
		exe, _ := os.Executable()
		script := fmt.Sprintf("#!/bin/sh\nFAKE_ENGINE=1 exec '%s' \"$@\"\n", exe)
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			return 1
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", "python"), []byte(script), 0o755); err != nil {
			return 1
		}
	case "pip":
		i := slices.Index(args, "-r")
		if i < 0 || !slices.Contains(args, "--require-hashes") {
			fmt.Fprintln(os.Stderr, "want -r FILE --require-hashes")
			return 2
		}
		b, err := os.ReadFile(args[i+1])
		if err != nil || !strings.Contains(string(b), "--hash=sha256:") {
			fmt.Fprintln(os.Stderr, "requirements without hashes")
			return 1
		}
		if n, _ := strconv.Atoi(os.Getenv("FAKE_UV_PIP_BYTES")); n > 0 {
			// Wheels landing in the cache while the install runs, for the progress poll.
			os.MkdirAll(os.Getenv("UV_CACHE_DIR"), 0o755)
			os.WriteFile(filepath.Join(os.Getenv("UV_CACHE_DIR"), "wheel"), make([]byte, n), 0o644)
			time.Sleep(300 * time.Millisecond)
		}
	default:
		return 2
	}
	return 0
}

// pyRig serves a fake uv release and an engines dir on darwin/arm64, so the install runs on
// any test host.
type pyRig struct {
	b      *Binaries
	srv    *rangeServer
	uvLog  string
	darwin Platform
}

func newPyRig(t *testing.T) *pyRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake uv and python are shell scripts")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	g := &pyRig{darwin: Platform{"darwin", "arm64"}, uvLog: t.TempDir()}
	t.Setenv("FAKE_UV_LOG", g.uvLog)
	uv := makeTarGz(t, []tarEntry{
		{name: "uv-aarch64-apple-darwin/", dir: true},
		{name: "uv-aarch64-apple-darwin/uv", body: fmt.Sprintf("#!/bin/sh\nFAKE_UV=1 exec '%s' \"$@\"\n", exe), mode: 0o755},
	})
	withRelease(t, KindUV, g.darwin, map[Flavour][]Asset{FlavourCPU: {{"uv.tar.gz", int64(len(uv)), sum(uv)}}})
	g.srv = &rangeServer{files: map[string][]byte{"/rel/b1/uv.tar.gz": uv}}
	ts := httptest.NewServer(g.srv)
	t.Cleanup(ts.Close)
	g.b = &Binaries{Dir: filepath.Join(t.TempDir(), "engines"), Platform: g.darwin, Client: ts.Client(),
		BaseURL: func(Kind) string { return ts.URL + "/rel/" }}
	return g
}

func (g *pyRig) uvCall(t *testing.T, step string) fakeStart {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g.uvLog, step+".json"))
	if err != nil {
		t.Fatalf("uv %s not run: %v", step, err)
	}
	var s fakeStart
	json.Unmarshal(b, &s)
	return s
}

// MLX is offered on Apple Silicon only, and everywhere else the error says why.
func TestPythonEngineGate(t *testing.T) {
	mac := Platform{"darwin", "arm64"}
	if got := FlavoursFor(KindMLX, mac); !slices.Equal(got, []string{"metal"}) {
		t.Fatalf("flavours on %s: %v", mac, got)
	}
	if a, err := AssetsFor(KindMLX, mac, FlavourMetal); err != nil || len(a) != 1 || a[0].Name != "uv-aarch64-apple-darwin.tar.gz" {
		t.Fatalf("assets on %s: %v %v", mac, a, err)
	}
	if Unavailable(KindMLX, mac) != "" {
		t.Fatal("unavailable on a mac")
	}
	if _, err := AssetsFor(KindMLX, mac, FlavourCPU); err == nil || !strings.Contains(err.Error(), "metal") {
		t.Fatalf("cpu flavour: %v", err)
	}
	for _, p := range []Platform{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}} {
		if got := FlavoursFor(KindMLX, p); len(got) != 0 {
			t.Errorf("%s offers %v", p, got)
		}
		_, err := AssetsFor(KindMLX, p, FlavourMetal)
		why := Unavailable(KindMLX, p)
		if err == nil || !strings.Contains(err.Error(), "Apple Silicon") || !strings.Contains(why, "Apple Silicon") || !strings.Contains(why, p.String()) {
			t.Errorf("%s: err %v, why %q", p, err, why)
		}
	}
	if k, err := DirEngine(mac, ""); err != nil || k != KindMLX {
		t.Fatalf("dir engine on a mac: %s %v", k, err)
	}
	if _, err := DirEngine(Platform{"linux", "arm64"}, ""); err == nil || !strings.Contains(err.Error(), "Apple Silicon") {
		t.Fatalf("dir engine on linux/arm64: %v", err)
	}
	if Build(KindMLX) != PyEngines[KindMLX].Version || Build(KindLlama) != Releases[KindLlama].Build {
		t.Fatal("Build")
	}
	// The pinned set is a hash lock of the server package.
	req := PyEngines[KindMLX].Requirements
	if !strings.Contains(req, "mlx-lm=="+PyEngines[KindMLX].Version) || !strings.Contains(req, "--hash=sha256:") {
		t.Fatal("requirements are not a hash lock of mlx-lm")
	}
	if slices.Contains(Kinds, KindUV) || !slices.Contains(Kinds, KindMLX) {
		t.Fatalf("kinds %v", Kinds)
	}
}

// uv and the Python engines have no mirror or override setting: llama.cpp's must not apply.
func TestPythonEngineSettingKeys(t *testing.T) {
	for _, k := range []Kind{KindUV, KindMLX} {
		if BaseURLKey(k) != "" || PathKey(k) != "" {
			t.Errorf("%s: %q %q", k, BaseURLKey(k), PathKey(k))
		}
	}
	if BaseURLKey(KindLlama) != KeyLlamaBaseURL || PathKey(KindWhisper) != KeyWhisperPath {
		t.Error("llama/whisper keys")
	}
}

func TestPythonEngineInstall(t *testing.T) {
	g := newPyRig(t)
	if _, ok := g.b.Path(KindMLX, FlavourMetal); ok {
		t.Fatal("installed before fetch")
	}
	var done, total int64
	py, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, func(d, tot int64) { done, total = d, tot })
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(g.b.Dir, "mlx-lm", PyEngines[KindMLX].Version+"-metal")
	if py != filepath.Join(dir, "venv", "bin", "python") {
		t.Fatalf("python %s", py)
	}
	if done == 0 || done != total {
		t.Fatalf("progress %d/%d", done, total)
	}
	// uv came from its pinned release, and every step stayed inside the engines dir.
	if _, ok := g.b.Path(KindUV, FlavourCPU); !ok {
		t.Fatal("uv not installed")
	}
	cache := filepath.Join(g.b.Dir, "mlx-lm", ".uv-cache")
	for _, step := range []string{"python", "venv", "pip"} {
		c := g.uvCall(t, step)
		want := map[string]string{
			"UV_PYTHON_INSTALL_DIR": filepath.Join(dir, "python"), "UV_CACHE_DIR": cache,
			"UV_PYTHON_PREFERENCE": "only-managed", "UV_NO_CONFIG": "1",
		}
		for k, v := range want {
			if c.Env[k] != v {
				t.Errorf("uv %s: %s=%q, want %q", step, k, c.Env[k], v)
			}
		}
	}
	if c := g.uvCall(t, "python"); strings.Join(c.Args, " ") != "python install --no-bin "+PyEngines[KindMLX].Python {
		t.Errorf("python step %v", c.Args)
	}
	if c := g.uvCall(t, "venv"); c.Args[len(c.Args)-1] != filepath.Join(dir, "venv") {
		t.Errorf("venv step %v", c.Args)
	}
	if c := g.uvCall(t, "pip"); !slices.Contains(c.Args, "--only-binary") || !slices.Contains(c.Args, py) {
		t.Errorf("pip step %v", c.Args)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "requirements.txt")); string(b) != PyEngines[KindMLX].Requirements {
		t.Error("requirements.txt is not the pinned set")
	}
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Error("uv cache left behind after a good install")
	}

	if got, ok := g.b.Path(KindMLX, FlavourMetal); !ok || got != py {
		t.Fatalf("Path after fetch: %s %v", got, ok)
	}
	if inst := g.b.Installed(KindMLX); inst[FlavourMetal] != py {
		t.Fatalf("installed %v", inst)
	}
	var found bool
	for _, e := range g.b.List() {
		if e.Kind == KindMLX {
			found = e.Path == py && e.Pinned && e.Flavour == FlavourMetal && e.Build == PyEngines[KindMLX].Version
		}
	}
	if !found {
		t.Fatalf("list %+v", g.b.List())
	}
	n := g.srv.requests.Load()
	os.Remove(filepath.Join(g.uvLog, "pip.json"))
	if _, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, nil); err != nil || g.srv.requests.Load() != n {
		t.Fatal("second fetch should be a no-op")
	}
	if _, err := os.Stat(filepath.Join(g.uvLog, "pip.json")); err == nil {
		t.Fatal("second fetch reinstalled")
	}
	// An operator's llama-server override is no Python engine.
	g.b.Override = func(Kind) string { return os.Args[0] }
	if got, _ := g.b.Path(KindMLX, FlavourMetal); got != py {
		t.Fatalf("override applied to a python engine: %s", got)
	}
	if err := g.b.Remove(KindMLX, PyEngines[KindMLX].Version, FlavourMetal); err != nil || len(g.b.Installed(KindMLX)) != 0 {
		t.Fatal("remove")
	}
}

// A failed step leaves nothing that looks installed, says what failed, and a retry starts over.
func TestPythonEngineInstallFailure(t *testing.T) {
	for _, c := range []struct{ fail, env, want string }{
		{"pip", "FAKE_UV_FAIL", "no matching distribution"},
		{"check", "FAKE_PY_FAIL", "No module named"},
	} {
		t.Run(c.fail, func(t *testing.T) {
			g := newPyRig(t)
			v := c.fail
			if c.env == "FAKE_PY_FAIL" {
				v = "1"
			}
			t.Setenv(c.env, v)
			_, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v", err)
			}
			if _, ok := g.b.Path(KindMLX, FlavourMetal); ok {
				t.Fatal("a failed install is installed")
			}
			if len(g.b.Installed(KindMLX)) != 0 {
				t.Fatal("listed as installed")
			}
			for _, e := range g.b.List() {
				if e.Kind == KindMLX {
					t.Fatalf("listed %+v", e)
				}
			}
			os.Unsetenv(c.env)
			if _, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, nil); err != nil {
				t.Fatalf("retry: %v", err)
			}
		})
	}
}

// The fetch job installs a Python engine by the same kind/flavour payload.
func TestPythonEngineFetchRefusedOffPlatform(t *testing.T) {
	g := newPyRig(t)
	g.b.Platform = Platform{"linux", "amd64"}
	if _, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, nil); err == nil || !strings.Contains(err.Error(), "Apple Silicon") {
		t.Fatalf("err %v", err)
	}
	if g.srv.requests.Load() != 0 {
		t.Fatal("downloaded uv for an engine that can't run here")
	}
}

func TestPythonEngineArgv(t *testing.T) {
	c := LabelConfig{Label: LabelLive, Kind: KindMLX, Model: "/m/Qwen3-8B-4bit", ModelPath: "/m/Qwen3-8B-4bit", Context: 16384,
		Parallel: 4, GPULayers: 999, GPUDevices: "0", KVCacheType: "q8_0", FlashAttn: "on",
		ExtraArgs: `--chat-template-args '{"enable_thinking": false}'`}
	k := keyFor(c, FlavourMetal, "/e/venv/bin/python")
	args, err := k.args(9000, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := `-I -m mlx_lm.server --model /m/Qwen3-8B-4bit --host 127.0.0.1 --port 9000 --allowed-origins http://127.0.0.1:9000 --max-tokens 16384 --chat-template-args {"enable_thinking": false}`
	if got := strings.Join(args, " "); got != want {
		t.Errorf("argv\n got: %s\nwant: %s", got, want)
	}
	// llama.cpp-only settings don't split one MLX model into two engines (LD1).
	other := c
	other.Label, other.Parallel, other.GPUDevices, other.KVCacheType, other.FlashAttn, other.GPULayers = LabelBackground, 1, "", "f16", "auto", 0
	if keyFor(c, FlavourMetal, "/e/py").hash() != keyFor(other, FlavourMetal, "/e/py").hash() {
		t.Error("two labels on the same MLX model don't share an engine")
	}
}

// Two labels on one MLX model share one supervised mlx_lm.server, reached like llama-server
// but with no key, the loaded model's request name and the engine's own environment.
func TestResolvePythonEngine(t *testing.T) {
	g := newRig(t)
	g.r.Platform = Platform{"darwin", "arm64"}
	g.bins.have[FlavourMetal] = true
	dir := filepath.Join(g.models, "Qwen3-0.6B-4bit")
	os.MkdirAll(dir, 0o755)
	live := LabelConfig{Label: LabelLive, Kind: KindMLX, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir,
		Context: 4096, Parallel: 1, GPULayers: 999, GPUBackend: "auto"}
	g.src.set(live)
	bg := live
	bg.Label, bg.Parallel = LabelBackground, 2
	g.src.set(bg)

	ep := g.ready(LabelLive)
	epBg := g.ready(LabelBackground)
	if ep.Engine != epBg.Engine || !strings.HasPrefix(ep.Engine, "mlx-") {
		t.Fatalf("engines %q %q", ep.Engine, epBg.Engine)
	}
	if ep.Kind != KindMLX || ep.APIKey != "" || ep.Model != "default_model" || ep.Vision || ep.ContextLength != 4096 || ep.Parallel != 1 ||
		!strings.HasSuffix(ep.BaseURL, "/v1") {
		t.Fatalf("endpoint %+v", ep)
	}
	if code, body := post(t, ep, "/chat/completions", `{"model":"default_model"}`); code != 200 || !strings.Contains(body, "hi from Qwen3-0.6B-4bit") {
		t.Fatalf("%d %s", code, body)
	}
	st := g.starts()
	if len(st) != 1 {
		t.Fatalf("%d starts", len(st))
	}
	if st[0].Env["HF_HUB_OFFLINE"] != "1" {
		t.Errorf("env %v", st[0].Env)
	}
	if !slices.Contains(st[0].Args, "mlx_lm.server") || slices.Contains(st[0].Args, "-ngl") {
		t.Errorf("args %v", st[0].Args)
	}
	if i := instanceFor(g.r, ep.Engine); i.Kind != KindMLX || i.Flavour != FlavourMetal {
		t.Fatalf("instance %+v", i)
	}
}

// Off Apple Silicon a label on an MLX model says why instead of fetching anything.
func TestResolvePythonEngineOffPlatform(t *testing.T) {
	g := newRig(t)
	g.r.Platform = Platform{"linux", "amd64"}
	dir := filepath.Join(g.models, "Qwen3-0.6B-4bit")
	os.MkdirAll(dir, 0o755)
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindMLX, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir, GPUBackend: "auto"})
	_, err := g.r.Resolve(context.Background(), LabelLive)
	var nr *NotReadyError
	if !errors.As(err, &nr) || nr.State != "no_engine_build" || !strings.Contains(nr.Reason, "Apple Silicon") {
		t.Fatalf("err %v", err)
	}
	if len(g.fetchQ) != 0 {
		t.Fatalf("fetched %v", g.fetchQ)
	}
	// An explicit backend other than the engine's own is refused with the reason too.
	g.r.Platform = Platform{"darwin", "arm64"}
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindMLX, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir, GPUBackend: "cpu"})
	if _, err := g.r.Resolve(context.Background(), LabelLive); !errors.As(err, &nr) || !strings.Contains(nr.Reason, "metal") {
		t.Fatalf("cpu backend: %v", err)
	}
	// Missing engine: the label asks for the install like a missing llama-server build.
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindMLX, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir, GPUBackend: "auto"})
	if _, err := g.r.Resolve(context.Background(), LabelLive); !errors.As(err, &nr) || nr.State != "fetching_engine" {
		t.Fatalf("missing engine: %v", err)
	}
	if !slices.Contains(g.fetchQ, "mlx-lm/metal") {
		t.Fatalf("fetch queue %v", g.fetchQ)
	}
}

// A big engine (vLLM's install takes about 17 GB at its peak) doesn't start without the disk
// for it, and reports its package step as bytes on disk.
func TestPythonEngineInstallDiskAndProgress(t *testing.T) {
	g := newPyRig(t)
	saved := PyEngines[KindMLX]
	t.Cleanup(func() { PyEngines[KindMLX] = saved })
	e := saved
	e.DiskBytes, e.DownloadBytes = 3_000_000_000, 1_000_000_000
	PyEngines[KindMLX] = e

	g.b.DiskFree = func(string) uint64 { return 500_000_000 }
	_, err := g.b.Fetch(context.Background(), KindMLX, FlavourMetal, nil)
	if err == nil || !strings.Contains(err.Error(), "needs about 3.0 GB free") || !strings.Contains(err.Error(), "0.5 GB free") ||
		!strings.Contains(err.Error(), "1.0 GB download") {
		t.Fatalf("short disk: %v", err)
	}
	if g.srv.requests.Load() != 0 {
		t.Fatal("downloaded with no room for the install")
	}

	g.b.DiskFree = func(string) uint64 { return 20_000_000_000 }
	oldPoll := installPoll
	installPoll = 10 * time.Millisecond
	t.Cleanup(func() { installPoll = oldPoll })
	t.Setenv("FAKE_UV_PIP_BYTES", "1048576")
	var mu sync.Mutex
	var seen []int64
	_, err = g.b.Fetch(context.Background(), KindMLX, FlavourMetal, func(done, total int64) {
		if total == e.DiskBytes {
			mu.Lock()
			seen = append(seen, done)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || slices.Max(seen) < 1<<20 {
		t.Fatalf("package progress %v", seen)
	}
}
