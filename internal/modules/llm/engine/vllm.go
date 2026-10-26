package engine

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// vLLM (decision 2026-10-19-vllm-engine, llm LD10): vllm's OpenAI-compatible server, a Python
// engine on linux/amd64 with an NVIDIA GPU. It runs Hugging Face directory models in their
// own precision (BF16, FP8, AWQ, GPTQ) where llama-server runs GGUF files.
//
// Unlike llama-server it preallocates a fixed share of each card (--gpu-memory-utilization,
// default 0.9 upstream) for weights, activations and as much KV cache as fits. Next to a
// llama-server label on the same card that default would take the card, so jarvisd always
// passes the share: the label's gpu_memory_utilization, or one sized to the model at its
// context (VLLMGPUMemory). The fit check counts that share as the engine's need.

// KindVLLM is vLLM's OpenAI-compatible server: Hugging Face directories on NVIDIA GPUs.
const KindVLLM Kind = "vllm"

//go:embed pyengines/vllm.txt
var vllmRequirements string

// vllmMinDriver is the oldest NVIDIA driver the pinned torch's CUDA 13.0 runtime loads on.
const vllmMinDriver = 580

// vllmOverhead is what vLLM takes beyond weights and KV cache: the CUDA context, the
// profiled activation peak and the captured CUDA graphs (about 1.2 GiB for a small model).
const vllmOverhead = 1536 << 20

func init() {
	PyEngines[KindVLLM] = PyEngine{
		Kind: KindVLLM, Version: "0.32.0", Python: "3.12",
		Requirements: vllmRequirements,
		Platforms:    map[Platform][]Flavour{{"linux", "amd64"}: {FlavourCUDA}},
		Needs:        fmt.Sprintf("linux/amd64 with an NVIDIA GPU (driver %d or later)", vllmMinDriver),
		Gate:         vllmGate,
		// Measured for 0.32.0: 3.9 GB of wheels unpack to 8.1 GB, copied out of uv's cache
		// (removed after the install) into the environment, so 16.2 GB at the peak.
		DownloadBytes: 3_932_507_100,
		DiskBytes:     17_000_000_000,
		Module:        "vllm.entrypoints.openai.api_server",
		Check:         "import torch, vllm, vllm.entrypoints.openai.api_server",
		Args:          vllmArgs,
		HealthPath:    "/health",
		RequestModel:  "default_model",
		APIKeyEnv:     "VLLM_API_KEY",
		// FlashInfer's top-k/top-p sampler is compiled on first use (nvcc, ninja, a host
		// compiler and matching CUDA headers), which a host without a CUDA toolkit lacks: its
		// warmup killed the default start and its first sampled request the EngineCore (#582).
		// PyTorch's sampler needs none of it.
		Env: map[string]string{"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1",
			"VLLM_NO_USAGE_STATS": "1", "DO_NOT_TRACK": "1", "VLLM_USE_FLASHINFER_SAMPLER": "0"},
		// Kernel and compile caches vLLM, torch and triton write at run time; under the
		// install so jarvisd's home stays the only place it writes.
		CacheEnv: map[string]string{"VLLM_CACHE_ROOT": "vllm", "TRITON_CACHE_DIR": "triton",
			"TORCHINDUCTOR_CACHE_DIR": "inductor", "XDG_CACHE_HOME": "xdg", "FLASHINFER_WORKSPACE_BASE": "flashinfer"},
		Tune:  vllmTune,
		Short: "vllm",
	}
	Kinds = append(Kinds, KindVLLM)
}

// vllmArgs is the server's command line. Requests name the model "default_model"; the model's
// tool-call and reasoning parsers (from its config.json) make tool calls and <think> blocks
// come back as llama-server --jinja returns them.
func vllmArgs(a PyArgs) []string {
	out := []string{"--model", a.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(a.Port), "--served-model-name", "default_model"}
	if a.Context > 0 {
		out = append(out, "--max-model-len", strconv.Itoa(a.Context))
	}
	if a.GPUMemory > 0 {
		out = append(out, "--gpu-memory-utilization", strconv.FormatFloat(a.GPUMemory, 'f', -1, 64))
	}
	if a.Devices > 1 {
		out = append(out, "--tensor-parallel-size", strconv.Itoa(a.Devices))
	}
	if a.ToolParser != "" {
		out = append(out, "--enable-auto-tool-choice", "--tool-call-parser", a.ToolParser)
	}
	if a.ReasoningParser != "" {
		out = append(out, "--reasoning-parser", a.ReasoningParser)
	}
	return out
}

// vllmGate says why vLLM can't run on hw ("" = it can): it needs an NVIDIA card and a driver
// for its CUDA build. An unreadable driver version passes (the engine's start says more).
func vllmGate(hw Hardware) string {
	if len(hw.Discrete(FlavourCUDA)) == 0 {
		return fmt.Sprintf("vllm needs an NVIDIA GPU (driver %d or later); none was detected", vllmMinDriver)
	}
	major, _, _ := strings.Cut(hw.NVIDIADriver, ".")
	if n, err := strconv.Atoi(major); err == nil && n < vllmMinDriver {
		return fmt.Sprintf("vllm %s needs NVIDIA driver %d or later (CUDA 13); this host has %s",
			PyEngines[KindVLLM].Version, vllmMinDriver, hw.NVIDIADriver)
	}
	return ""
}

// vllmTuned caches vllmTune's answer per input for the process: it runs on every resolve, and
// a later detection that reads the card's total a little differently (llama-server's figure
// instead of nvidia-smi's once a build is installed) must not change the share and restart a
// running engine. A changed setting, model, context or devices is another input.
var vllmTuned sync.Map // string -> instanceKey

// vllmTune completes a vLLM engine's key: its GPU share, the model's parsers and whether it
// takes images.
func vllmTune(c LabelConfig, hw Hardware, k *instanceKey) error {
	in := fmt.Sprintf("%s|%d|%s|%v", c.ModelPath, c.Context, normList(c.GPUDevices), c.GPUMemory)
	if v, ok := vllmTuned.Load(in); ok {
		t := v.(instanceKey)
		k.GPUMemory, k.ToolParser, k.ReasoningParser, k.Vision = t.GPUMemory, t.ToolParser, t.ReasoningParser, t.Vision
		return nil
	}
	t, err := ReadModelTraits(c.ModelPath)
	if err != nil {
		return err
	}
	frac, _, err := VLLMGPUMemory(c, hw)
	if err != nil {
		return err
	}
	k.GPUMemory, k.ToolParser, k.ReasoningParser, k.Vision = frac, t.ToolParser, t.ReasoningParser, t.Vision
	vllmTuned.Store(in, *k)
	return nil
}

// ModelTraits is what jarvisd reads from a Hugging Face model directory.
type ModelTraits struct {
	ModelType string
	// Vision: the model has a vision tower (config.json vision_config) and takes images.
	Vision bool
	// WeightBytes is the size of its safetensors files.
	WeightBytes int64
	// KVBytesPerToken is the KV cache per token in 16-bit (0 = the config doesn't say).
	KVBytesPerToken int64
	// ToolParser and ReasoningParser are vLLM's for the model's family ("" = none known).
	ToolParser, ReasoningParser string
}

// vllmParsers maps config.json model_type to vLLM's tool-call and reasoning parsers. Families
// not listed run without tool parsing: vLLM refuses tool_choice "auto", which cc sends with
// its tools (extra_args can name a parser), and leaves <think> in the content (cc strips it).
var vllmParsers = map[string][2]string{
	"qwen2":        {"hermes", ""},
	"qwen2_moe":    {"hermes", ""},
	"qwen2_5_vl":   {"hermes", ""},
	"qwen3":        {"hermes", "qwen3"},
	"qwen3_moe":    {"hermes", "qwen3"},
	"qwen3_vl":     {"hermes", "qwen3"},
	"qwen3_vl_moe": {"hermes", "qwen3"},
	"llama":        {"llama3_json", ""},
	"mistral":      {"mistral", ""},
}

// ReadModelTraits reads a model directory's config.json and weights.
func ReadModelTraits(dir string) (ModelTraits, error) {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return ModelTraits{}, fmt.Errorf("model directory %s: %w", dir, err)
	}
	type shape struct {
		ModelType string `json:"model_type"`
		Hidden    int64  `json:"hidden_size"`
		Heads     int64  `json:"num_attention_heads"`
		KVHeads   int64  `json:"num_key_value_heads"`
		HeadDim   int64  `json:"head_dim"`
		Layers    int64  `json:"num_hidden_layers"`
	}
	var cfg struct {
		shape
		Text   *shape          `json:"text_config"`
		Vision json.RawMessage `json:"vision_config"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return ModelTraits{}, fmt.Errorf("model directory %s: config.json: %w", dir, err)
	}
	t := ModelTraits{ModelType: cfg.ModelType, Vision: len(cfg.Vision) > 0 && string(cfg.Vision) != "null"}
	s := cfg.shape
	if cfg.Text != nil {
		s = *cfg.Text
	}
	if s.KVHeads == 0 {
		s.KVHeads = s.Heads
	}
	if s.HeadDim == 0 && s.Heads > 0 {
		s.HeadDim = s.Hidden / s.Heads
	}
	t.KVBytesPerToken = s.Layers * s.KVHeads * s.HeadDim * 2 * 2
	p := vllmParsers[t.ModelType]
	t.ToolParser, t.ReasoningParser = p[0], p[1]
	files, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			t.WeightBytes += st.Size()
		}
	}
	return t, nil
}

// VLLMGPUMemory is the share of each card a vLLM label's engine preallocates (its
// --gpu-memory-utilization) and the card it is judged on: the label's gpu_memory_utilization,
// else the model's weights plus its KV cache at the label's context plus vllmOverhead, split
// over the label's cards, rounded up to the next hundredth (between 0.1 and vLLM's own 0.9).
func VLLMGPUMemory(c LabelConfig, hw Hardware) (float64, Device, error) {
	devs := hw.Discrete(FlavourCUDA)
	if len(devs) == 0 {
		return 0, Device{}, fmt.Errorf("no NVIDIA GPU detected")
	}
	dev, n := devs[0], 1
	if idx := normList(c.GPUDevices); idx != "" {
		ids := strings.Split(idx, ",")
		n = len(ids)
		i, _ := strconv.Atoi(ids[0])
		found := false
		for _, d := range devs {
			if d.Index == i {
				dev, found = d, true
			}
		}
		if !found {
			return 0, Device{}, fmt.Errorf("gpu_devices %s: no NVIDIA GPU %s detected", idx, ids[0])
		}
	}
	if c.GPUMemory != 0 {
		if c.GPUMemory < 0.05 || c.GPUMemory > 0.95 {
			return 0, dev, fmt.Errorf("gpu_memory_utilization %v: must be between 0.05 and 0.95 (0 = sized to the model)", c.GPUMemory)
		}
		return c.GPUMemory, dev, nil
	}
	t, err := ReadModelTraits(c.ModelPath)
	if err != nil {
		return 0, dev, err
	}
	ctx := int64(c.Context)
	if ctx <= 0 {
		ctx = 8192
	}
	kv := t.KVBytesPerToken
	if kv == 0 {
		kv = 128 << 10 // a 7B-class model with grouped KV heads
	}
	need := float64(t.WeightBytes+kv*ctx)/float64(n) + vllmOverhead
	return roundShare(need / float64(dev.TotalMB<<20)), dev, nil
}

// roundShare rounds a share of a card up to the next hundredth, within [0.1, 0.9].
func roundShare(f float64) float64 {
	f = math.Ceil(math.Round(f*1e6)/1e4) / 100
	return min(max(f, 0.1), 0.9)
}
