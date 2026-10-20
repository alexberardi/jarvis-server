package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Transformers runs on MPS or the CPU on a Mac and on the CPU on Windows x64, each from its own
// hash-locked package set with its size; nowhere else yet.
func TestTransformersPlatforms(t *testing.T) {
	mac, win := Platform{"darwin", "arm64"}, Platform{"windows", "amd64"}
	if got := FlavoursFor(KindTransformers, mac); !slices.Equal(got, []string{"metal", "cpu"}) {
		t.Fatalf("flavours on a mac: %v", got)
	}
	if got := FlavoursFor(KindTransformers, win); !slices.Equal(got, []string{"cpu"}) {
		t.Fatalf("flavours on windows: %v", got)
	}
	for _, f := range []Flavour{FlavourMetal, FlavourCPU} {
		if a, err := AssetsFor(KindTransformers, mac, f); err != nil || len(a) != 1 || a[0].Name != "uv-aarch64-apple-darwin.tar.gz" {
			t.Fatalf("assets on a mac, %s: %v %v", f, a, err)
		}
	}
	if a, err := AssetsFor(KindTransformers, win, FlavourCPU); err != nil || len(a) != 1 || a[0].Name != "uv-x86_64-pc-windows-msvc.zip" {
		t.Fatalf("assets on windows: %v %v", a, err)
	}
	if _, err := AssetsFor(KindTransformers, win, FlavourCUDA); err == nil || !strings.Contains(err.Error(), "cpu only, not cuda") {
		t.Fatalf("cuda on windows: %v", err)
	}
	if _, err := AssetsFor(KindTransformers, mac, FlavourCUDA); err == nil || !strings.Contains(err.Error(), "metal or cpu here, not cuda") {
		t.Fatalf("cuda on a mac: %v", err)
	}
	for _, p := range []Platform{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"windows", "arm64"}} {
		if why := Unavailable(KindTransformers, p); !strings.Contains(why, "Apple Silicon") || !strings.Contains(why, p.String()) {
			t.Errorf("%s: %q", p, why)
		}
	}

	e := PyEngines[KindTransformers]
	ms, ws := e.Set(mac, FlavourMetal), e.Set(win, FlavourCPU)
	if e.Set(mac, FlavourCPU).Requirements != ms.Requirements {
		t.Error("MPS and CPU on a Mac are one torch build")
	}
	for name, s := range map[string]PySet{"mac": ms, "windows": ws} {
		if !strings.Contains(s.Requirements, "transformers=="+e.Version) || !strings.Contains(s.Requirements, "torch==") ||
			!strings.Contains(s.Requirements, "fastapi==") || !strings.Contains(s.Requirements, "--hash=sha256:") ||
			!strings.Contains(s.Requirements, "\nrequests==") { // transformers.cli.transformers imports it
			t.Errorf("%s: not a hash lock of transformers[serving] and torch", name)
		}
		// The size is shown before the install: torch alone is over 100 MB.
		if s.DownloadBytes < 100_000_000 || s.DiskBytes <= s.DownloadBytes || s.IndexURL != "" {
			t.Errorf("%s: download %d, disk %d, index %q", name, s.DownloadBytes, s.DiskBytes, s.IndexURL)
		}
	}
	if ms.Requirements == ws.Requirements || !strings.Contains(ws.Requirements, "colorama==") {
		t.Error("windows has its own set")
	}
	if !slices.Contains(Kinds, KindTransformers) || Build(KindTransformers) != e.Version || BaseURLKey(KindTransformers) != "" {
		t.Fatalf("kinds %v", Kinds)
	}
	if UnavailableOn(KindTransformers, mac, Hardware{}) != "" {
		t.Error("gated by hardware")
	}
}

// A model directory keeps MLX on a Mac and vLLM on linux/amd64 unless the label's dir_engine
// asks for Transformers; on Windows Transformers is the only one.
func TestDirEngineChoice(t *testing.T) {
	mac, win, linux := Platform{"darwin", "arm64"}, Platform{"windows", "amd64"}, Platform{"linux", "amd64"}
	for _, c := range []struct {
		p      Platform
		choice string
		want   Kind
		err    string
	}{
		{mac, "", KindMLX, ""},
		{mac, "auto", KindMLX, ""},
		{mac, "transformers", KindTransformers, ""},
		{mac, " Transformers ", KindTransformers, ""},
		{mac, "mlx-lm", KindMLX, ""},
		{mac, "vllm", "", "linux/amd64"},
		{mac, "llama", "", "not an engine for a model directory"},
		{win, "", KindTransformers, ""},
		{linux, "", KindVLLM, ""},
		{linux, "transformers", "", "Linux is not supported yet"},
	} {
		k, err := DirEngine(c.p, c.choice)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s %q: %s %v, want error %q", c.p, c.choice, k, err, c.err)
			}
			continue
		}
		if err != nil || k != c.want {
			t.Errorf("%s %q: %s %v, want %s", c.p, c.choice, k, err, c.want)
		}
	}
	var opts []string
	for _, f := range fields {
		if f.name == "dir_engine" {
			for _, o := range f.opts {
				opts = append(opts, o.(string))
			}
		}
	}
	if !slices.Equal(opts, DirEngineChoices()) {
		t.Errorf("dir_engine options %v, engines %v", opts, DirEngineChoices())
	}
}

func TestTransformersArgv(t *testing.T) {
	k := instanceKey{Kind: KindTransformers, Binary: "/e/venv/bin/python", Flavour: FlavourMetal, Model: "/m/SmolLM2-135M-Instruct",
		Context: 8192, Parallel: 1, DType: "bfloat16", ExtraArgs: "--attn-implementation sdpa"}
	args, err := k.args(9000, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := "-I -m transformers.cli.transformers serve /m/SmolLM2-135M-Instruct --host 127.0.0.1 --port 9000 --device mps " +
		"--dtype bfloat16 --attn-implementation sdpa"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("argv\n got: %s\nwant: %s", got, want)
	}
	k.Flavour, k.DType, k.ExtraArgs = FlavourCPU, "", ""
	args, _ = k.args(9000, "")
	if got := strings.Join(args, " "); !strings.HasSuffix(got, "--device cpu") {
		t.Errorf("cpu: %s", got)
	}
	k.Flavour, k.Quantization = FlavourCUDA, "bnb-4bit"
	args, _ = k.args(9000, "")
	if got := strings.Join(args, " "); !strings.HasSuffix(got, "--device cuda --quantization bnb-4bit") {
		t.Errorf("cuda: %s", got)
	}
}

// dtype and quantization are part of the engine's key, checked before it starts.
func TestTransformersTune(t *testing.T) {
	root := t.TempDir()
	text := hfDir(t, root, "SmolLM2-135M-Instruct", qwen3Config, 1<<20)
	vl := hfDir(t, root, "Qwen3-VL-2B", qwen3VLConfig, 1<<20)
	tune := func(c LabelConfig, f Flavour) (instanceKey, error) {
		c.Kind = KindTransformers
		k := keyFor(c, f, "/e/py")
		return k, transformersTune(c, Hardware{}, &k)
	}
	k, err := tune(LabelConfig{ModelPath: text, DType: "auto", Quantization: "none"}, FlavourMetal)
	if err != nil || k.DType != "" || k.Quantization != "" || k.Vision {
		t.Fatalf("defaults: %+v %v", k, err)
	}
	k, err = tune(LabelConfig{ModelPath: vl, DType: "float16"}, FlavourMetal)
	if err != nil || k.DType != "float16" || !k.Vision {
		t.Fatalf("float16 vision model: %+v %v", k, err)
	}
	a, _ := tune(LabelConfig{ModelPath: text, DType: "bfloat16"}, FlavourMetal)
	b, _ := tune(LabelConfig{ModelPath: text, DType: "float32"}, FlavourMetal)
	if a.hash() == b.hash() {
		t.Error("two dtypes share an engine")
	}
	if k, err := tune(LabelConfig{ModelPath: text, Quantization: "bnb-8bit"}, FlavourCUDA); err != nil || k.Quantization != "bnb-8bit" {
		t.Fatalf("cuda quantization: %+v %v", k, err)
	}
	for _, c := range []struct {
		cfg  LabelConfig
		f    Flavour
		want string
	}{
		{LabelConfig{ModelPath: text, Quantization: "bnb-4bit"}, FlavourMetal, "needs CUDA"},
		{LabelConfig{ModelPath: text, Quantization: "awq"}, FlavourCUDA, "must be one of none, bnb-4bit, bnb-8bit"},
		{LabelConfig{ModelPath: text, DType: "int8"}, FlavourCPU, "must be one of auto, bfloat16"},
		{LabelConfig{ModelPath: filepath.Join(root, "missing")}, FlavourCPU, "config.json"},
		{LabelConfig{ModelPath: "/m/a@b"}, FlavourCPU, "has an @"},
	} {
		if _, err := tune(c.cfg, c.f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v on %s: %v, want %q", c.cfg, c.f, err, c.want)
		}
	}
}

// A label on Transformers starts `transformers serve` with the model preloaded, on MPS unless
// gpu_backend says cpu, and its endpoint names the model by its path (the server refuses any
// other name).
func TestResolveTransformers(t *testing.T) {
	g := newRig(t)
	g.r.Platform = Platform{"darwin", "arm64"}
	g.bins.have = map[Flavour]bool{FlavourMetal: true} // the CPU install comes later, fetched
	dir := hfDir(t, g.models, "SmolLM2-135M-Instruct", qwen3Config, 1<<20)
	live := LabelConfig{Label: LabelLive, Kind: KindTransformers, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir,
		Context: 4096, Parallel: 1, GPULayers: 999, GPUBackend: "auto", DType: "bfloat16"}
	g.src.set(live)

	ep := g.ready(LabelLive)
	if ep.Kind != KindTransformers || ep.APIKey != "" || ep.Model != dir || !ep.NoReasoningBudget || ep.Vision ||
		!strings.HasPrefix(ep.Engine, "tf-") || !strings.HasSuffix(ep.BaseURL, "/v1") {
		t.Fatalf("endpoint %+v", ep)
	}
	if code, body := post(t, ep, "/chat/completions", `{"model":"`+dir+`"}`); code != 200 || !strings.Contains(body, "hi from SmolLM2") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := post(t, ep, "/chat/completions", `{"model":"default_model"}`); code != 400 {
		t.Fatalf("another model name: %d", code)
	}
	st := g.starts()
	if len(st) != 1 {
		t.Fatalf("%d starts", len(st))
	}
	argv := strings.Join(st[0].Args, " ")
	if !strings.Contains(argv, "transformers.cli.transformers serve "+dir+" --host 127.0.0.1") ||
		!strings.Contains(argv, "--device mps --dtype bfloat16") || strings.Contains(argv, "-ngl") {
		t.Errorf("argv %s", argv)
	}
	if st[0].Env["HF_HUB_OFFLINE"] != "1" {
		t.Errorf("env %v", st[0].Env)
	}
	if i := instanceFor(g.r, ep.Engine); i.Flavour != FlavourMetal {
		t.Fatalf("instance %+v", i)
	}

	// gpu_backend=cpu: the CPU install, asked for like any missing build.
	live.GPUBackend = "cpu"
	g.src.set(live)
	_, err := g.r.Resolve(context.Background(), LabelLive)
	var nr *NotReadyError
	if !errors.As(err, &nr) || nr.State != "fetching_engine" || !slices.Contains(g.fetchQ, "transformers/cpu") {
		t.Fatalf("cpu: %v, fetch queue %v", err, g.fetchQ)
	}
	g.bins.mu.Lock()
	g.bins.have[FlavourCPU] = true
	g.bins.mu.Unlock()
	cpu := g.ready(LabelLive)
	if i := instanceFor(g.r, cpu.Engine); i.Flavour != FlavourCPU || cpu.Engine == ep.Engine {
		t.Fatalf("cpu engine %+v", i)
	}
	if !slices.ContainsFunc(g.starts(), func(s fakeStart) bool { return strings.Contains(strings.Join(s.Args, " "), "--device cpu") }) {
		t.Fatalf("no cpu start in %v", g.starts())
	}

	// A setting the engine can't honour says why instead of starting it.
	live.GPUBackend, live.Quantization = "auto", "bnb-4bit"
	g.src.set(live)
	if _, err := g.r.Resolve(context.Background(), LabelLive); !errors.As(err, &nr) || nr.State != "misconfigured" ||
		!strings.Contains(nr.Reason, "needs CUDA") {
		t.Fatalf("quantization on MPS: %v", err)
	}
}

// The install takes the platform's own set, from the extra index when the set names one.
func TestPythonEngineInstallSetPerPlatform(t *testing.T) {
	g := newPyRig(t)
	saved := PyEngines[KindTransformers]
	t.Cleanup(func() { PyEngines[KindTransformers] = saved })
	e := saved
	e.Sets = map[string]PySet{SetKey(g.darwin, FlavourCPU): {Requirements: "torch==9.9 \\\n    --hash=sha256:00\n",
		IndexURL: "https://download.pytorch.org/whl/cpu"}}
	PyEngines[KindTransformers] = e
	g.b.DiskFree = func(string) uint64 { return 1 << 40 }
	checkLog := filepath.Join(t.TempDir(), "check")
	t.Setenv("FAKE_PY_CHECK_LOG", checkLog)

	if _, err := g.b.Fetch(context.Background(), KindTransformers, FlavourMetal, nil); err != nil {
		t.Fatal(err)
	}
	// The import check runs offline, as the engine does.
	if b, _ := os.ReadFile(checkLog); string(b) != "HF_HUB_OFFLINE=1" {
		t.Errorf("check env: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(g.b.InstallDir(KindTransformers, FlavourMetal), "requirements.txt")); string(b) != saved.Requirements {
		t.Error("metal: not the default set")
	}
	if a := strings.Join(g.uvCall(t, "pip").Args, " "); strings.Contains(a, "--extra-index-url") {
		t.Errorf("metal pip %s", a)
	}
	if _, err := g.b.Fetch(context.Background(), KindTransformers, FlavourCPU, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(g.b.InstallDir(KindTransformers, FlavourCPU), "requirements.txt")); !strings.HasPrefix(string(b), "torch==9.9") {
		t.Errorf("cpu: requirements %q", b)
	}
	if a := strings.Join(g.uvCall(t, "pip").Args, " "); !strings.Contains(a, "--extra-index-url https://download.pytorch.org/whl/cpu --index-strategy unsafe-best-match") {
		t.Errorf("cpu pip %s", a)
	}
	if _, err := g.b.Fetch(context.Background(), KindTransformers, FlavourCUDA, nil); err == nil {
		t.Error("cuda installed on a mac")
	}
}

// A Windows install's interpreter is venv\Scripts\python.exe.
func TestPythonInWindows(t *testing.T) {
	if got := pythonIn(Platform{"windows", "amd64"}, "e"); got != filepath.Join("e", "venv", "Scripts", "python.exe") {
		t.Errorf("windows: %s", got)
	}
	if got := pythonIn(Platform{"darwin", "arm64"}, "e"); got != filepath.Join("e", "venv", "bin", "python") {
		t.Errorf("darwin: %s", got)
	}
}
