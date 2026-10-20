package engine

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var linuxAMD = Platform{"linux", "amd64"}

// hwVLLM is a 12 GB NVIDIA card on a driver new enough for vLLM's CUDA 13 build.
var hwVLLM = Hardware{OS: "linux", Arch: "amd64", Flavour: FlavourCUDA, NVIDIADriver: "610.57.04",
	Devices: []Device{{Backend: FlavourCUDA, Index: 0, ID: "CUDA0", Name: "NVIDIA GeForce RTX 3080 Ti", TotalMB: 12288, FreeMB: 12000}}}

// qwen3Config is Qwen3-0.6B's config.json (the fields jarvisd reads).
const qwen3Config = `{"architectures":["Qwen3ForCausalLM"],"model_type":"qwen3","hidden_size":1024,
 "num_attention_heads":16,"num_key_value_heads":8,"head_dim":128,"num_hidden_layers":28,"torch_dtype":"bfloat16"}`

// qwen3VLConfig nests the language model's shape under text_config, beside a vision tower.
const qwen3VLConfig = `{"architectures":["Qwen3VLForConditionalGeneration"],"model_type":"qwen3_vl",
 "text_config":{"model_type":"qwen3_vl_text","hidden_size":2048,"num_attention_heads":16,"num_key_value_heads":8,
 "head_dim":128,"num_hidden_layers":28,"dtype":"bfloat16"},"vision_config":{"depth":24}}`

// hfDir writes a Hugging Face layout model directory: config.json and a weights file of size.
func hfDir(t *testing.T, root, name, config string, size int64) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	return dir
}

// vLLM runs on linux/amd64 with an NVIDIA GPU and a driver for its CUDA build; everywhere
// else the admin is told which of those is missing.
func TestVLLMGate(t *testing.T) {
	if got := FlavoursFor(KindVLLM, linuxAMD); !slices.Equal(got, []string{"cuda"}) {
		t.Fatalf("flavours: %v", got)
	}
	if a, err := AssetsFor(KindVLLM, linuxAMD, FlavourCUDA); err != nil || len(a) != 1 || a[0].Name != "uv-x86_64-unknown-linux-gnu.tar.gz" {
		t.Fatalf("assets: %v %v", a, err)
	}
	for _, p := range []Platform{{"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		if why := Unavailable(KindVLLM, p); !strings.Contains(why, "linux/amd64") || !strings.Contains(why, p.String()) {
			t.Errorf("%s: %q", p, why)
		}
	}
	if why := UnavailableOn(KindVLLM, linuxAMD, hwVLLM); why != "" {
		t.Fatalf("on a supported host: %q", why)
	}
	noGPU := Hardware{OS: "linux", Arch: "amd64", Flavour: FlavourCPU}
	amd := Hardware{OS: "linux", Arch: "amd64", Flavour: FlavourROCm, Devices: []Device{{Backend: FlavourROCm, TotalMB: 16000}}}
	for name, hw := range map[string]Hardware{"no gpu": noGPU, "amd gpu": amd} {
		if why := UnavailableOn(KindVLLM, linuxAMD, hw); !strings.Contains(why, "NVIDIA GPU") {
			t.Errorf("%s: %q", name, why)
		}
	}
	old := hwVLLM
	old.NVIDIADriver = "550.163.01"
	if why := UnavailableOn(KindVLLM, linuxAMD, old); !strings.Contains(why, "580") || !strings.Contains(why, "550.163.01") {
		t.Errorf("old driver: %q", why)
	}
	// An unreadable driver version doesn't block: the engine's own start says what's wrong.
	unknown := hwVLLM
	unknown.NVIDIADriver = ""
	if why := UnavailableOn(KindVLLM, linuxAMD, unknown); why != "" {
		t.Errorf("unknown driver: %q", why)
	}
	// The platform decides first.
	if why := UnavailableOn(KindVLLM, Platform{"darwin", "arm64"}, hwVLLM); !strings.Contains(why, "linux/amd64") {
		t.Errorf("darwin: %q", why)
	}
	if UnavailableOn(KindLlama, linuxAMD, noGPU) != "" || UnavailableOn(KindMLX, Platform{"darwin", "arm64"}, Hardware{}) != "" {
		t.Error("other kinds gated by hardware")
	}

	// A model directory runs on vLLM on linux/amd64, on MLX on a Mac.
	if k, err := DirEngine(linuxAMD); err != nil || k != KindVLLM {
		t.Fatalf("dir engine on linux: %s %v", k, err)
	}
	if k, err := DirEngine(Platform{"darwin", "arm64"}); err != nil || k != KindMLX {
		t.Fatalf("dir engine on a mac: %s %v", k, err)
	}
	if _, err := DirEngine(Platform{"windows", "amd64"}); err == nil || !strings.Contains(err.Error(), "Apple Silicon") ||
		!strings.Contains(err.Error(), "NVIDIA") {
		t.Fatalf("dir engine on windows: %v", err)
	}

	e := PyEngines[KindVLLM]
	if Build(KindVLLM) != e.Version || !strings.Contains(e.Requirements, "vllm=="+e.Version) ||
		!strings.Contains(e.Requirements, "--hash=sha256:") || !strings.Contains(e.Requirements, "torch==") {
		t.Fatal("requirements are not a hash lock of vllm")
	}
	if !slices.Contains(Kinds, KindVLLM) || BaseURLKey(KindVLLM) != "" || PathKey(KindVLLM) != "" {
		t.Fatalf("kinds %v", Kinds)
	}
	if e.DownloadBytes < 3<<30 || e.DiskBytes <= e.DownloadBytes {
		t.Fatalf("sizes: download %d, disk %d", e.DownloadBytes, e.DiskBytes)
	}
}

// nvidia-smi's driver_version column is what the gate reads.
func TestDetectNVIDIADriver(t *testing.T) {
	d := &Detector{Platform: linuxAMD, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "nvidia-smi" && strings.Contains(strings.Join(args, " "), "driver_version") {
			return []byte("0, NVIDIA GeForce RTX 3080 Ti, 12288, 1435, 610.57.04\n"), nil
		}
		return nil, errors.New("not found")
	}}
	hw := d.Hardware(context.Background(), true)
	if hw.NVIDIADriver != "610.57.04" || len(hw.Discrete(FlavourCUDA)) != 1 || hw.Devices[0].FreeMB != 1435 {
		t.Fatalf("%+v", hw)
	}
}

func TestReadModelTraits(t *testing.T) {
	root := t.TempDir()
	tr, err := ReadModelTraits(hfDir(t, root, "Qwen3-0.6B", qwen3Config, 1_503_300_328))
	if err != nil {
		t.Fatal(err)
	}
	// 28 layers × 8 KV heads × 128 × (K and V) × 2 bytes.
	if tr.ModelType != "qwen3" || tr.Vision || tr.WeightBytes != 1_503_300_328 || tr.KVBytesPerToken != 28*8*128*2*2 {
		t.Fatalf("%+v", tr)
	}
	if tr.ToolParser != "hermes" || tr.ReasoningParser != "qwen3" {
		t.Fatalf("parsers %+v", tr)
	}
	vl, err := ReadModelTraits(hfDir(t, root, "Qwen3-VL-2B", qwen3VLConfig, 4<<30))
	if err != nil {
		t.Fatal(err)
	}
	if !vl.Vision || vl.KVBytesPerToken != 28*8*128*2*2 || vl.ToolParser != "hermes" {
		t.Fatalf("vl %+v", vl)
	}
	// No head_dim: hidden_size / heads. A family with no known parser gets none.
	gem, err := ReadModelTraits(hfDir(t, root, "other", `{"model_type":"somethingelse","hidden_size":2048,
		"num_attention_heads":8,"num_key_value_heads":4,"num_hidden_layers":10}`, 1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if gem.KVBytesPerToken != 10*4*256*2*2 || gem.ToolParser != "" || gem.ReasoningParser != "" {
		t.Fatalf("%+v", gem)
	}
	if _, err := ReadModelTraits(t.TempDir()); err == nil || !strings.Contains(err.Error(), "config.json") {
		t.Fatalf("no config: %v", err)
	}
}

// vLLM preallocates a share of the card. Unset, the share is sized to the model at its
// context (so llama-server keeps the rest); set, it is the operator's.
func TestVLLMGPUMemory(t *testing.T) {
	root := t.TempDir()
	dir := hfDir(t, root, "Qwen3-0.6B", qwen3Config, 1_503_300_328)
	c := LabelConfig{Label: LabelLive, Kind: KindVLLM, ModelPath: dir, Context: 8192}
	frac, dev, err := VLLMGPUMemory(c, hwVLLM)
	if err != nil {
		t.Fatal(err)
	}
	// 1.4 GiB weights + 0.875 GiB KV (8192 × 112 KiB) + 1.5 GiB overhead ≈ 3.78 GiB of 12 GiB.
	if dev.Index != 0 || frac != 0.32 {
		t.Fatalf("auto: %v on %+v", frac, dev)
	}
	c.Context = 32768
	if f, _, _ := VLLMGPUMemory(c, hwVLLM); f <= frac {
		t.Fatalf("longer context, same share: %v", f)
	}
	// Never more than vLLM's own default, even for a model that won't fit.
	big := c
	big.ModelPath = hfDir(t, root, "big", qwen3Config, 20<<30)
	if f, _, _ := VLLMGPUMemory(big, hwVLLM); f != 0.9 {
		t.Fatalf("big: %v", f)
	}
	c.GPUMemory = 0.5
	if f, _, err := VLLMGPUMemory(c, hwVLLM); err != nil || f != 0.5 {
		t.Fatalf("set: %v %v", f, err)
	}
	for _, bad := range []float64{0.01, 0.99, -1} {
		c.GPUMemory = bad
		if _, _, err := VLLMGPUMemory(c, hwVLLM); err == nil || !strings.Contains(err.Error(), "gpu_memory_utilization") {
			t.Errorf("%v: %v", bad, err)
		}
	}
	c.GPUMemory, c.Context = 0, 8192
	// gpu_devices picks the card; two cards split the model (tensor parallel).
	two := hwVLLM
	two.Devices = append(slices.Clone(hwVLLM.Devices), Device{Backend: FlavourCUDA, Index: 1, ID: "CUDA1", TotalMB: 24576})
	c.GPUDevices = "0"
	if _, d, _ := VLLMGPUMemory(c, two); d.Index != 0 {
		t.Fatalf("device %+v", d)
	}
	c.GPUDevices = ""
	if _, d, _ := VLLMGPUMemory(c, two); d.Index != 1 {
		t.Fatalf("largest card: %+v", d)
	}
	c.GPUDevices = "0,1"
	if f, _, _ := VLLMGPUMemory(c, two); f >= frac {
		t.Fatalf("split over two cards: %v", f)
	}
	c.GPUDevices = "3"
	if _, _, err := VLLMGPUMemory(c, two); err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("missing device: %v", err)
	}
	if _, _, err := VLLMGPUMemory(LabelConfig{ModelPath: dir}, Hardware{}); err == nil {
		t.Fatal("no card")
	}
}

func TestVLLMArgv(t *testing.T) {
	k := instanceKey{Kind: KindVLLM, Binary: "/e/venv/bin/python", Flavour: FlavourCUDA, Model: "/m/Qwen3-0.6B", Context: 8192,
		Parallel: 1, Devices: "0,1", GPUMemory: 0.32, ToolParser: "hermes", ReasoningParser: "qwen3", ExtraArgs: "--enforce-eager"}
	args, err := k.args(9000, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	want := "-I -m vllm.entrypoints.openai.api_server --model /m/Qwen3-0.6B --host 127.0.0.1 --port 9000 --served-model-name default_model " +
		"--max-model-len 8192 --gpu-memory-utilization 0.32 --tensor-parallel-size 2 --enable-auto-tool-choice --tool-call-parser hermes " +
		"--reasoning-parser qwen3 --enforce-eager"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("argv\n got: %s\nwant: %s", got, want)
	}
	k.Devices, k.ToolParser, k.ReasoningParser, k.ExtraArgs = "", "", "", ""
	args, _ = k.args(9000, "")
	if got := strings.Join(args, " "); strings.Contains(got, "tool") || strings.Contains(got, "tensor-parallel") || strings.Contains(got, "reasoning") {
		t.Errorf("no parsers, one card: %s", got)
	}
	// The GPU devices are vLLM's (CUDA_VISIBLE_DEVICES), unlike MLX's.
	c := LabelConfig{Label: LabelLive, Kind: KindVLLM, ModelPath: "/m/x", Context: 4096, GPUDevices: "1", KVCacheType: "q8_0", Parallel: 4}
	if key := keyFor(c, FlavourCUDA, "/e/py"); key.Devices != "1" || key.KVCacheType != "" || key.Parallel != 1 {
		t.Fatalf("key %+v", key)
	}
}

// Two labels on one vLLM model share one engine: it gets its own API key (it would otherwise
// answer any local process), the model's parsers, a GPU budget, the visible devices and its
// caches under the install dir.
func TestResolveVLLM(t *testing.T) {
	g, box := gpuRig(t, hwVLLM, &memStore{})
	g.r.Platform = linuxAMD
	dir := hfDir(t, g.models, "Qwen3-VL-2B", qwen3VLConfig, 4<<30)
	bin := os.Args[0] // fakeBinaries' engine, as if it were <install>/venv/bin/python
	live := LabelConfig{Label: LabelLive, Kind: KindVLLM, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir,
		Context: 4096, Parallel: 1, GPULayers: 999, GPUBackend: "auto", GPUDevices: "0"}
	g.src.set(live)
	bg := live
	bg.Label, bg.Parallel = LabelBackground, 2
	g.src.set(bg)

	ep := g.ready(LabelLive)
	if g.ready(LabelBackground).Engine != ep.Engine || !strings.HasPrefix(ep.Engine, "vllm-") {
		t.Fatalf("engine %q", ep.Engine)
	}
	if ep.Kind != KindVLLM || ep.APIKey == "" || ep.Model != "default_model" || !ep.Vision || ep.ContextLength != 4096 {
		t.Fatalf("endpoint %+v", ep)
	}
	if code, body := post(t, ep, "/chat/completions", `{"model":"default_model"}`); code != 200 || !strings.Contains(body, "hi from Qwen3-VL-2B") {
		t.Fatalf("%d %s", code, body)
	}
	noKey := ep
	noKey.APIKey = ""
	if code, _ := post(t, noKey, "/chat/completions", `{}`); code != 401 {
		t.Fatalf("without the key: %d", code)
	}
	st := g.starts()
	if len(st) != 1 {
		t.Fatalf("%d starts", len(st))
	}
	s := st[0]
	if s.Env["HF_HUB_OFFLINE"] != "1" || s.Env["CUDA_VISIBLE_DEVICES"] != "0" || s.Env["VLLM_API_KEY"] != ep.APIKey ||
		s.Env["VLLM_CACHE_ROOT"] != filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(bin))), "cache", "vllm") {
		t.Errorf("env %v", s.Env)
	}
	if slices.Contains(s.Args, ep.APIKey) {
		t.Error("the key is on argv")
	}
	argv := strings.Join(s.Args, " ")
	for _, want := range []string{"vllm.entrypoints.openai.api_server", "--gpu-memory-utilization 0.", "--tool-call-parser hermes",
		"--max-model-len 4096", "--served-model-name default_model"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv lacks %q: %s", want, argv)
		}
	}
	if i := instanceFor(g.r, ep.Engine); i.Kind != KindVLLM || i.Flavour != FlavourCUDA {
		t.Fatalf("instance %+v", i)
	}

	// Detection later reads the card's total a little differently (llama-server's figure once
	// a build is installed): the running engine keeps its share and isn't restarted.
	other := hwVLLM
	other.Devices = []Device{hwVLLM.Devices[0]}
	other.Devices[0].TotalMB = 11000
	box.set(other)
	g.r.Reconcile(context.Background())
	if again := g.ready(LabelLive); again.Engine != ep.Engine || len(g.starts()) != 1 {
		t.Fatalf("restarted: %s → %s, %d starts", ep.Engine, again.Engine, len(g.starts()))
	}
}

// vLLM refuses to start, with the reason, where it can't run, instead of downloading 4 GB.
func TestResolveVLLMRefused(t *testing.T) {
	old := hwVLLM
	old.NVIDIADriver = "550.163.01"
	for name, tc := range map[string]struct {
		hw    Hardware
		state string
		why   string
	}{
		"no gpu":     {Hardware{OS: "linux", Arch: "amd64", Flavour: FlavourCPU}, "no_engine_build", "NVIDIA GPU"},
		"old driver": {old, "no_engine_build", "580"},
		"mismatch":   {hwMismatch, StateGPUUnavailable, UserMsgDriverUpdated},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := gpuRig(t, tc.hw, &memStore{})
			g.r.Platform = linuxAMD
			dir := hfDir(t, g.models, "Qwen3-0.6B", qwen3Config, 1<<20)
			g.src.set(LabelConfig{Label: LabelLive, Kind: KindVLLM, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir,
				Context: 4096, GPULayers: 999, GPUBackend: "auto"})
			_, err := g.r.Resolve(context.Background(), LabelLive)
			var nr *NotReadyError
			if !errors.As(err, &nr) || nr.State != tc.state || !strings.Contains(nr.Reason, tc.why) {
				t.Fatalf("err %v", err)
			}
			if len(g.fetchQ) != 0 || len(g.starts()) != 0 {
				t.Fatalf("fetched %v, started %d", g.fetchQ, len(g.starts()))
			}
		})
	}
}

// A vLLM engine is a GPU engine to the GPU-loss handling: remembered once healthy, kept
// through a later driver fault while it runs, and refused (not run elsewhere) once the GPU is
// gone.
func TestVLLMGPULoss(t *testing.T) {
	mem := &memStore{}
	g, box := gpuRig(t, hwVLLM, mem)
	g.r.Platform = linuxAMD
	dir := hfDir(t, g.models, "Qwen3-0.6B", qwen3Config, 1<<20)
	c := LabelConfig{Label: LabelLive, Kind: KindVLLM, ModelKind: ModelLLM, Engine: ModeLocal, Model: dir, ModelPath: dir,
		Context: 4096, GPULayers: 999, GPUBackend: "auto"}
	g.src.set(c)
	ep := g.ready(LabelLive)
	g.r.Reconcile(context.Background())
	if p := mem.get(LabelLive); p.Flavour != FlavourCUDA {
		t.Fatalf("not remembered: %+v", p)
	}
	late := hwMismatch
	late.DetectedAt = instanceFor(g.r, ep.Engine).Started.Add(time.Millisecond)
	box.set(late)
	g.r.Reconcile(context.Background())
	if got, err := g.r.Resolve(context.Background(), LabelLive); err != nil || got.Engine != ep.Engine {
		t.Fatalf("running engine dropped: %v %+v", err, got)
	}
	g.r.StopAll(context.Background())
	gpuRefused(t, g.r, LabelLive, UserMsgDriverUpdated)

	// jarvisd restarts with the card gone and no fault: the generic reason, from the history.
	g2, _ := gpuRig(t, Hardware{OS: "linux", Arch: "amd64", Flavour: FlavourCPU}, mem)
	g2.r.Platform = linuxAMD
	g2.src.set(c)
	gpuRefused(t, g2.r, LabelLive, UserMsgGPUUnavailable)
}

func TestRoundUpShare(t *testing.T) {
	for in, want := range map[float64]float64{0.301: 0.31, 0.3: 0.3, 0.01: 0.1, 2: 0.9} {
		if got := roundShare(in); math.Abs(got-want) > 1e-9 {
			t.Errorf("%v: %v", in, got)
		}
	}
}
