package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// Placement-and-load identity of an engine instance (LD1): two labels whose configs produce
// the same key share one supervised process. The port and API key are per instance, not part
// of the key.
type instanceKey struct {
	Kind        Kind    `json:"kind"`
	Binary      string  `json:"binary"`
	Flavour     Flavour `json:"flavour"`
	Model       string  `json:"model"`
	MMProj      string  `json:"mmproj,omitempty"`
	Context     int     `json:"ctx,omitempty"`
	Parallel    int     `json:"np,omitempty"`
	GPULayers   int     `json:"ngl"`
	Devices     string  `json:"devices,omitempty"`
	SplitMode   string  `json:"sm,omitempty"`
	TensorSplit string  `json:"ts,omitempty"`
	KVCacheType string  `json:"kv,omitempty"`
	FlashAttn   string  `json:"fa,omitempty"`
	Embedding   bool    `json:"embedding,omitempty"`
	// ChatTemplate is the pinned template's file, named by its digest, so another template is
	// another instance (ID12).
	ChatTemplate string `json:"tpl,omitempty"`
	ExtraArgs    string `json:"extra,omitempty"`
	// GPUMemory, ToolParser, ReasoningParser and Vision are a vLLM engine's (vllmTune).
	GPUMemory       float64 `json:"gpu_mem,omitempty"`
	ToolParser      string  `json:"tools,omitempty"`
	ReasoningParser string  `json:"reasoning,omitempty"`
	Vision          bool    `json:"vision,omitempty"`
	// DType and Quantization are a Transformers engine's (transformersTune).
	DType        string `json:"dtype,omitempty"`
	Quantization string `json:"quant,omitempty"`
}

func (k instanceKey) hash() string {
	b, _ := json.Marshal(k)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func keyFor(c LabelConfig, f Flavour, binary string) instanceKey {
	k := instanceKey{
		Kind: c.Kind, Binary: binary, Flavour: f, Model: c.ModelPath, MMProj: c.MMProjPath,
		GPULayers: c.GPULayers, Devices: normList(c.GPUDevices), ExtraArgs: strings.TrimSpace(c.ExtraArgs),
		FlashAttn: c.FlashAttn,
	}
	if _, ok := c.Kind.Python(); ok {
		// The engine takes none of llama.cpp's placement or cache flags, so they don't
		// split one model into two engines (LD1). Context bounds a reply (PyEngine.Args).
		// On CUDA (vLLM) the devices are the engine's: CUDA_VISIBLE_DEVICES.
		pk := instanceKey{Kind: c.Kind, Binary: binary, Flavour: f, Model: c.ModelPath, Context: c.Context,
			Parallel: 1, ExtraArgs: strings.TrimSpace(c.ExtraArgs)}
		if f == FlavourCUDA {
			pk.Devices = normList(c.GPUDevices)
		}
		return pk
	}
	if c.Kind == KindLlama {
		k.Context, k.Parallel = c.Context, c.Parallel
		k.SplitMode, k.TensorSplit = c.SplitMode, normList(c.TensorSplit)
		k.KVCacheType, k.Embedding = c.KVCacheType, c.Embedding
	}
	if f == FlavourCPU {
		k.GPULayers, k.Devices, k.SplitMode, k.TensorSplit = 0, "", "", ""
	}
	return k
}

// EngineIdentity is what decides, from settings alone, whether two labels' local configs run
// one engine (LD1): equal identities share one. The resolver's instance key adds the build. A
// label the GPU can't hold a second copy for may run on another's engine anyway (Resolver.Fits);
// a running label's Endpoint.Engine is then the truth.
func EngineIdentity(c LabelConfig) string {
	if c.ModelPath == "" {
		c.ModelPath = c.Model
	}
	return keyFor(c, Flavour(c.GPUBackend), "").hash()
}

func normList(s string) string {
	var parts []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ",")
}

// How much host RAM llama-server's server prompt cache may hold, in MiB. Upstream's own
// default is 8192 (`-cram`, ggml-org/llama.cpp#16391), which every install pays on top of the
// model's VRAM: on the devbox the 9B held 8.5 GiB of anonymous RAM for it, and a 16 GiB box
// swaps. We keep the cache — it saves re-processing the system prompt when live, background
// and computer turns alternate on one slot — but size it from the host: a tenth of RAM, at
// most cacheRAMCapMiB and at least cacheRAMFloorMiB. The floor also stands in for a host whose
// RAM the OS won't say (sysinfo returns 0): a tenth of nothing would disable the cache.
const (
	cacheRAMCapMiB   = 2048
	cacheRAMFloorMiB = 256
)

// hostMemory is the host's physical RAM in bytes, 0 when unknown (tests replace it).
var hostMemory = sysinfo.TotalMemory

// cacheRAMMiB is the --cache-ram value for a chat engine on this host.
func cacheRAMMiB() int {
	mib := int(hostMemory() / (1 << 20) / 10)
	return min(max(mib, cacheRAMFloorMiB), cacheRAMCapMiB)
}

// args builds the engine command line for an instance key (01 §11 "Local slot spec").
func (k instanceKey) args(port int, alias string) ([]string, error) {
	extra, err := SplitArgs(k.ExtraArgs)
	if err != nil {
		return nil, fmt.Errorf("extra_args: %w", err)
	}
	if e, ok := k.Kind.Python(); ok {
		n := 0
		if k.Devices != "" {
			n = len(strings.Split(k.Devices, ","))
		}
		a := append([]string{"-I", "-m", e.Module}, e.Args(PyArgs{Model: k.Model, Port: port, Flavour: k.Flavour, Context: k.Context,
			GPUMemory: k.GPUMemory, Devices: n, ToolParser: k.ToolParser, ReasoningParser: k.ReasoningParser,
			DType: k.DType, Quantization: k.Quantization})...)
		return append(a, extra...), nil
	}
	a := []string{"-m", k.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	switch k.Kind {
	case KindWhisper:
		if k.GPULayers == 0 || k.Flavour == FlavourCPU {
			a = append(a, "--no-gpu")
		}
		if k.FlashAttn == "off" {
			// -nfa; whisper-server enables flash attention by default.
			a = append(a, "-nfa")
		}
	default:
		a = append(a, "--alias", alias, "--no-webui")
		if k.Context > 0 {
			a = append(a, "-c", strconv.Itoa(k.Context))
		}
		if k.Parallel > 0 {
			a = append(a, "-np", strconv.Itoa(k.Parallel))
		}
		a = append(a, "-ngl", strconv.Itoa(k.GPULayers))
		if k.GPULayers == 0 && k.Flavour != FlavourCPU {
			// A GPU build told to stay on the CPU: don't touch the devices at all.
			a = append(a, "-dev", "none")
		}
		if k.Embedding {
			// MiniLM takes at most 512 tokens; a whole input must fit one ubatch.
			a = append(a, "--embedding", "--pooling", "mean", "-b", "2048", "-ub", "2048")
		} else {
			a = append(a, "--jinja")
			if k.ChatTemplate != "" {
				// Before the extra args, so an operator's own --chat-template-file wins.
				a = append(a, "--chat-template-file", k.ChatTemplate)
			}
		}
		if k.MMProj != "" {
			a = append(a, "--mmproj", k.MMProj)
		}
		if !slices.Contains(extra, "--cache-ram") && !slices.Contains(extra, "-cram") {
			// Sized from the host rather than left at upstream's 8 GiB; off for embeddings,
			// which never reuse a prompt. An operator's own value in extra_args wins, and
			// ours is left out entirely so `ps` and admin's argv say what the engine runs.
			// llama-server rejects the `--cache-ram=N` form, so the exact token is enough.
			if k.Embedding {
				a = append(a, "--cache-ram", "0")
			} else {
				a = append(a, "--cache-ram", strconv.Itoa(cacheRAMMiB()))
			}
		}
		if k.KVCacheType != "" && k.KVCacheType != "f16" {
			a = append(a, "-ctk", k.KVCacheType, "-ctv", k.KVCacheType)
		}
		if k.FlashAttn != "" && k.FlashAttn != "auto" {
			a = append(a, "-fa", k.FlashAttn)
		}
		if k.SplitMode != "" {
			a = append(a, "-sm", k.SplitMode)
		}
		if k.TensorSplit != "" {
			a = append(a, "-ts", k.TensorSplit)
		}
	}
	return append(a, extra...), nil
}

// SplitArgs splits a flag string like a POSIX shell would, honouring single and double
// quotes and backslash escapes outside single quotes. Prod's live engine needs
// `--chat-template-kwargs '{"enable_thinking": false}'`.
func SplitArgs(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}
