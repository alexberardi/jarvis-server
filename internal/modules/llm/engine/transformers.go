package engine

import (
	_ "embed"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Transformers (decision 2026-10-19-transformers-engine, llm LD11): Hugging Face's own
// OpenAI-compatible server, `transformers serve`, a Python engine for any Hugging Face directory
// model (safetensors) where MLX and vLLM don't run or a label asks for it (dir_engine). It runs
// on the GPU torch has there: MPS on Apple Silicon (or the CPU, gpu_backend=cpu), the CPU on
// Windows x64. The Linux sets (CPU and CUDA 12.8 from PyTorch's wheel index) are still to come.
//
// What `transformers serve` 5.20.2 does with jarvisd's requests (read from its source):
//   - It preloads the model named on its command line before it listens, so /health answers
//     once the model is loaded, and it never unloads that model.
//   - A request's "model" must be that same string (else 400), so requests name the model's
//     path (RequestModel "").
//   - A field outside OpenAI's schema is refused (422): jarvisd doesn't send llama-server's
//     reasoning_budget to it (NoReasoningBudget); thinking goes by chat_template_kwargs.
//   - response_format, tool_choice and stream_options are ignored with a warning: JSON mode is
//     jarvisd's own validation and retry, and tools are always "auto". A stream still ends with
//     a usage chunk.
//   - Tool calls come back as tool_calls for the model families it has a response template for
//     (Qwen 2-3.5, Gemma 4, and tokenizers that ship one); other models' calls stay in the text.
//   - With no max_tokens a reply is capped at 1024 tokens (its default), not at the context.
//   - It has no API key option and binds 127.0.0.1 only; CORS stays off (its default).

// KindTransformers is `transformers serve`: Hugging Face directory models on torch.
const KindTransformers Kind = "transformers"

//go:embed pyengines/transformers-darwin-arm64.txt
var transformersMac string

//go:embed pyengines/transformers-windows-amd64.txt
var transformersWindows string

// TransformersDTypes and TransformersQuantizations are the dtype and quantization settings'
// values ("auto" and "none" = not passed).
var (
	TransformersDTypes        = []string{"auto", "bfloat16", "float16", "float32"}
	TransformersQuantizations = []string{"none", "bnb-4bit", "bnb-8bit"}
)

func init() {
	PyEngines[KindTransformers] = PyEngine{
		Kind: KindTransformers, Version: "5.20.2", Python: "3.12",
		Platforms: map[Platform][]Flavour{
			{"darwin", "arm64"}:  {FlavourMetal, FlavourCPU},
			{"windows", "amd64"}: {FlavourCPU},
		},
		// Measured on macOS arm64: 174 MB of wheels (torch is 127 MB) unpack to about 0.8 GB in
		// the environment, next to 0.78 GB in uv's cache until the install ends.
		Requirements: transformersMac, DownloadBytes: 173_828_456, DiskBytes: 1_700_000_000,
		Sets: map[string]PySet{
			SetKey(Platform{"windows", "amd64"}, FlavourCPU): {Requirements: transformersWindows,
				DownloadBytes: 177_554_959, DiskBytes: 1_800_000_000},
		},
		DirFallback: true,
		Needs:       "Apple Silicon (MPS or CPU) or Windows x64 (CPU); Linux is not supported yet",
		Module:      "transformers.cli.transformers",
		Check:       "import torch, accelerate, fastapi, uvicorn, transformers.cli.transformers, transformers.cli.serve",
		Args:        transformersArgs,
		HealthPath:  "/health",
		Env: map[string]string{"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1",
			"HF_HUB_DISABLE_UPDATE_CHECK": "1", "PYTORCH_ENABLE_MPS_FALLBACK": "1"},
		CacheEnv:          map[string]string{"HF_HOME": "hf", "XDG_CACHE_HOME": "xdg", "TORCHINDUCTOR_CACHE_DIR": "inductor"},
		Tune:              transformersTune,
		NoReasoningBudget: true,
		Short:             "tf",
	}
	Kinds = append(Kinds, KindTransformers)
}

// torchDevice is the --device for an install's accelerator.
func torchDevice(f Flavour) string {
	switch f {
	case FlavourMetal:
		return "mps"
	case FlavourCUDA:
		return "cuda"
	}
	return "cpu"
}

// transformersArgs is `transformers serve MODEL …`: the model preloaded on the install's device.
func transformersArgs(a PyArgs) []string {
	out := []string{"serve", a.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(a.Port), "--device", torchDevice(a.Flavour)}
	if a.DType != "" {
		out = append(out, "--dtype", a.DType)
	}
	if a.Quantization != "" {
		out = append(out, "--quantization", a.Quantization)
	}
	return out
}

// transformersTune completes a Transformers engine's key: dtype, quantization (bitsandbytes,
// CUDA only) and whether the model takes images.
func transformersTune(c LabelConfig, _ Hardware, k *instanceKey) error {
	if strings.Contains(c.ModelPath, "@") {
		// The server reads "@" as a revision: the path would name another model.
		return fmt.Errorf("transformers can't load a model whose path has an @ in it: %s", c.ModelPath)
	}
	dt := strings.ToLower(strings.TrimSpace(c.DType))
	if dt != "" && !slices.Contains(TransformersDTypes, dt) {
		return fmt.Errorf("dtype %q: must be one of %s", c.DType, strings.Join(TransformersDTypes, ", "))
	}
	if dt == "auto" {
		dt = ""
	}
	q := strings.ToLower(strings.TrimSpace(c.Quantization))
	if q != "" && !slices.Contains(TransformersQuantizations, q) {
		return fmt.Errorf("quantization %q: must be one of %s", c.Quantization, strings.Join(TransformersQuantizations, ", "))
	}
	if q == "none" {
		q = ""
	}
	if q != "" && k.Flavour != FlavourCUDA {
		return fmt.Errorf("quantization %s needs CUDA (bitsandbytes); this label runs transformers on %s", q, k.Flavour)
	}
	t, err := ReadModelTraits(c.ModelPath)
	if err != nil {
		return err
	}
	k.DType, k.Quantization, k.Vision = dt, q, t.Vision
	return nil
}
