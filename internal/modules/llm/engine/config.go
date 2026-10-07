package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Labels (LD1). live and background are LLM labels; embeddings runs llama-server --embedding;
// stt runs whisper-server (D7). Track B forces any other requested model name to live.
const (
	LabelLive       = "live"
	LabelBackground = "background"
	LabelEmbeddings = "embeddings"
	LabelSTT        = "stt"
)

// Model kinds, as the model manager records them.
const (
	ModelLLM       = "llm"
	ModelMMProj    = "mmproj"
	ModelEmbedding = "embedding"
	ModelSTT       = "stt"
)

// Engine modes of a label (llm.<label>.engine).
const (
	ModeLocal  = "local"
	ModeRemote = "remote"
	ModeShared = "shared" // background only: use exactly the live label's engine
	ModeOff    = "off"
)

// LabelDef describes one label: its settings prefix, engine kind and what it runs.
type LabelDef struct {
	Name      string
	Prefix    string // settings key prefix, e.g. "llm.live"
	Kind      Kind
	ModelKind string
	// DefaultModel is used when the label names no model and this installed model exists.
	DefaultModel string
	Defaults     map[string]any // per-label setting defaults that differ from the common ones
}

// LabelDefs lists every label in display order.
var LabelDefs = []LabelDef{
	// Thinking defaults (llm.<label>.reasoning_budget, LD8) are request-path settings owned by
	// the llm module itself, not engine settings.
	{Name: LabelLive, Prefix: "llm.live", Kind: KindLlama, ModelKind: ModelLLM},
	{Name: LabelBackground, Prefix: "llm.background", Kind: KindLlama, ModelKind: ModelLLM},
	{Name: LabelEmbeddings, Prefix: "llm.embeddings", Kind: KindLlama, ModelKind: ModelEmbedding,
		DefaultModel: "all-minilm-l6-v2",
		Defaults:     map[string]any{"context": int64(512), "parallel": int64(4), "gpu_layers": int64(0)}},
	{Name: LabelSTT, Prefix: "stt", Kind: KindWhisper, ModelKind: ModelSTT},
}

// LabelDefFor returns a label's definition.
func LabelDefFor(name string) (LabelDef, bool) {
	for _, d := range LabelDefs {
		if d.Name == name {
			return d, true
		}
	}
	return LabelDef{}, false
}

// field is one per-label setting.
type field struct {
	name string
	typ  settings.Type
	def  any
	desc string
	opts []any
	// only limits the field to labels of these model kinds (nil = all).
	only   []string
	secret bool
}

var fields = []field{
	{name: "engine", typ: settings.String, def: ModeLocal, desc: "local (supervised engine), remote (OpenAI-compatible URL), shared (background: use live's engine) or off",
		opts: []any{ModeLocal, ModeRemote, ModeShared, ModeOff}},
	{name: "model", typ: settings.String, def: "", desc: "Installed model id, or an absolute path to a model file"},
	{name: "mmproj", typ: settings.String, def: "", desc: "Vision projector: empty = the model's own if installed, \"none\" = no vision, or an installed id / absolute path",
		only: []string{ModelLLM}},
	{name: "context", typ: settings.Int, def: int64(0), desc: "Context size (-c, shared by the parallel slots); 0 = the model's catalog default",
		only: []string{ModelLLM, ModelEmbedding}},
	{name: "parallel", typ: settings.Int, def: int64(1), desc: "Parallel request slots (-np)", only: []string{ModelLLM, ModelEmbedding}},
	{name: "gpu_backend", typ: settings.String, def: "auto", desc: "auto, cpu, cuda, rocm, vulkan or metal",
		opts: []any{"auto", "cpu", "cuda", "rocm", "vulkan", "metal"}},
	{name: "gpu_devices", typ: settings.String, def: "", desc: "Device indexes, e.g. \"1\" or \"0,1\"; empty = auto"},
	{name: "split_mode", typ: settings.String, def: "", desc: "Multi-GPU split: none, layer or row; empty = engine default",
		only: []string{ModelLLM, ModelEmbedding}},
	{name: "tensor_split", typ: settings.String, def: "", desc: "Per-device proportions, e.g. \"1,1\"", only: []string{ModelLLM, ModelEmbedding}},
	{name: "gpu_layers", typ: settings.Int, def: int64(999), desc: "Layers to offload (-ngl); 0 = CPU only. For stt, 0 disables the GPU"},
	{name: "kv_cache_type", typ: settings.String, def: "f16", desc: "KV cache type (f16, q8_0, q4_0)", only: []string{ModelLLM},
		opts: []any{"f16", "q8_0", "q4_0"}},
	{name: "flash_attn", typ: settings.String, def: "auto", desc: "Flash attention: auto, on or off", opts: []any{"auto", "on", "off"}},
	{name: "extra_args", typ: settings.String, def: "", desc: "Extra engine flags (shell-quoted)"},
	{name: "remote_url", typ: settings.String, def: "", desc: "Remote OpenAI-compatible base URL, e.g. https://api.openai.com/v1",
		only: []string{ModelLLM, ModelEmbedding}},
	{name: "remote_model", typ: settings.String, def: "", desc: "Model name sent to the remote endpoint", only: []string{ModelLLM, ModelEmbedding}},
	{name: "remote_api_key", typ: settings.String, def: "", desc: "API key for the remote endpoint", secret: true,
		only: []string{ModelLLM, ModelEmbedding}},
	{name: "remote_vision", typ: settings.Bool, def: false, desc: "Whether the remote model accepts images", only: []string{ModelLLM}},
}

func (f field) appliesTo(d LabelDef) bool {
	if f.name == "engine" && d.ModelKind == ModelSTT {
		return true
	}
	if len(f.only) == 0 {
		return true
	}
	for _, k := range f.only {
		if k == d.ModelKind {
			return true
		}
	}
	return false
}

// Global setting keys owned by this package.
const (
	KeyHFToken         = "llm.hf_token"
	KeyHFEndpoint      = "llm.hf_endpoint"
	KeyLlamaBaseURL    = "llm.engine_base_url"
	KeyWhisperBaseURL  = "stt.engine_base_url"
	KeyLlamaEnginePath = "llm.engine_path"
	KeyWhisperPath     = "stt.engine_path"
)

// BaseURLKey and PathKey name a kind's release-URL and binary-override settings.
func BaseURLKey(k Kind) string {
	if k == KindWhisper {
		return KeyWhisperBaseURL
	}
	return KeyLlamaBaseURL
}

func PathKey(k Kind) string {
	if k == KindWhisper {
		return KeyWhisperPath
	}
	return KeyLlamaEnginePath
}

// SettingDefinitions returns every setting this package and the model manager read. The llm
// module passes them to settings.New together with its own; all are requires_reload, and a
// change takes effect on the resolver's next pass (no process restart).
func SettingDefinitions() []settings.Definition {
	var out []settings.Definition
	for _, d := range LabelDefs {
		cat := "engine." + d.Name
		for _, f := range fields {
			if !f.appliesTo(d) {
				continue
			}
			def := f.def
			if v, ok := d.Defaults[f.name]; ok {
				def = v
			}
			opts := f.opts
			if f.name == "engine" && d.Name != LabelBackground {
				opts = []any{ModeLocal, ModeRemote, ModeOff}
				if d.Kind == KindWhisper {
					opts = []any{ModeLocal, ModeOff}
				}
			}
			out = append(out, settings.Definition{
				Key: d.Prefix + "." + f.name, Category: cat, Type: f.typ, Default: def,
				Description: f.desc, RequiresReload: true, IsSecret: f.secret, Options: opts,
			})
		}
	}
	out = append(out,
		settings.Definition{Key: KeyHFToken, Category: "engine.downloads", Type: settings.String, Default: "",
			Description: "Hugging Face token for gated repos", IsSecret: true, EnvFallback: "HF_TOKEN"},
		settings.Definition{Key: KeyHFEndpoint, Category: "engine.downloads", Type: settings.String, Default: "https://huggingface.co",
			Description: "Hugging Face endpoint (mirror)", EnvFallback: "HF_ENDPOINT"},
		settings.Definition{Key: KeyLlamaBaseURL, Category: "engine.downloads", Type: settings.String, Default: Releases[KindLlama].DefaultBaseURL,
			Description: "llama.cpp release download base URL"},
		settings.Definition{Key: KeyWhisperBaseURL, Category: "engine.downloads", Type: settings.String, Default: Releases[KindWhisper].DefaultBaseURL,
			Description: "whisper.cpp release download base URL"},
		settings.Definition{Key: KeyLlamaEnginePath, Category: "engine.downloads", Type: settings.String, Default: "",
			Description: "Use this llama-server binary instead of a downloaded build", RequiresReload: true},
		settings.Definition{Key: KeyWhisperPath, Category: "engine.downloads", Type: settings.String, Default: "",
			Description: "Use this whisper-server binary instead of a downloaded build (e.g. Homebrew's on macOS)", RequiresReload: true},
	)
	return out
}

// LabelConfig is one label's effective configuration.
type LabelConfig struct {
	Label     string `json:"label"`
	Kind      Kind   `json:"kind"`
	ModelKind string `json:"model_kind"`
	Engine    string `json:"engine"`
	// SharedWith names the label whose engine this one uses (engine=shared).
	SharedWith string `json:"shared_with,omitempty"`

	Model      string `json:"model"`
	ModelPath  string `json:"model_path"`
	MMProj     string `json:"mmproj"`
	MMProjPath string `json:"mmproj_path"`

	Context     int    `json:"context"`
	Parallel    int    `json:"parallel"`
	GPUBackend  string `json:"gpu_backend"`
	GPUDevices  string `json:"gpu_devices"`
	SplitMode   string `json:"split_mode"`
	TensorSplit string `json:"tensor_split"`
	GPULayers   int    `json:"gpu_layers"`
	KVCacheType string `json:"kv_cache_type"`
	FlashAttn   string `json:"flash_attn"`
	ExtraArgs   string `json:"extra_args"`
	Embedding   bool   `json:"embedding"`

	RemoteURL    string `json:"remote_url"`
	RemoteModel  string `json:"remote_model"`
	RemoteAPIKey string `json:"-"`
	RemoteVision bool   `json:"remote_vision"`

	// Problem says why the label can't run as configured (missing model file, …).
	Problem string `json:"problem,omitempty"`
}

// ModelInfo is what the model manager knows about an installed model.
type ModelInfo struct {
	ID             string
	Kind           string
	Path           string
	MMProjID       string // the model's own projector, when installed with one
	ContextDefault int
}

// ErrModelNotFound is returned by a ModelLookup for an unknown or not-ready id.
var ErrModelNotFound = errors.New("model not installed")

// ModelLookup resolves installed model ids (the model manager's store).
type ModelLookup interface {
	LookupModel(ctx context.Context, id string) (ModelInfo, error)
}

// ConfigSource yields label configurations.
type ConfigSource interface {
	Label(ctx context.Context, label string) (LabelConfig, error)
}

// SettingsSource reads label configuration from the llm module's settings.
type SettingsSource struct {
	Settings *settings.Service
	Models   ModelLookup
}

// ErrUnknownLabel is returned for a label that isn't in LabelDefs.
var ErrUnknownLabel = errors.New("unknown label")

func (s SettingsSource) str(ctx context.Context, k string) string {
	return strings.TrimSpace(s.Settings.String(ctx, k, settings.Scope{}))
}

func (s SettingsSource) num(ctx context.Context, k string) int {
	return int(s.Settings.Int(ctx, k, settings.Scope{}))
}

// Label resolves one label.
func (s SettingsSource) Label(ctx context.Context, label string) (LabelConfig, error) {
	d, ok := LabelDefFor(label)
	if !ok {
		return LabelConfig{}, fmt.Errorf("%w %q", ErrUnknownLabel, label)
	}
	p := d.Prefix + "."
	has := func(name string) bool { _, ok := s.Settings.Definition(p + name); return ok }
	str := func(name string) string {
		if !has(name) {
			return ""
		}
		return s.str(ctx, p+name)
	}
	num := func(name string) int {
		if !has(name) {
			return 0
		}
		return s.num(ctx, p+name)
	}
	c := LabelConfig{
		Label: label, Kind: d.Kind, ModelKind: d.ModelKind, Engine: strings.ToLower(str("engine")),
		Model: str("model"), MMProj: str("mmproj"),
		Context: num("context"), Parallel: num("parallel"),
		GPUBackend: strings.ToLower(str("gpu_backend")), GPUDevices: str("gpu_devices"),
		SplitMode: str("split_mode"), TensorSplit: str("tensor_split"), GPULayers: num("gpu_layers"),
		KVCacheType: str("kv_cache_type"), FlashAttn: str("flash_attn"),
		ExtraArgs: str("extra_args"), Embedding: d.ModelKind == ModelEmbedding,
		RemoteURL: str("remote_url"), RemoteModel: str("remote_model"), RemoteAPIKey: str("remote_api_key"),
	}
	if has("remote_vision") {
		c.RemoteVision = s.Settings.Bool(ctx, p+"remote_vision", settings.Scope{})
	}
	if c.Engine == "" {
		c.Engine = ModeLocal
	}
	if c.Engine == ModeShared {
		if label != LabelBackground {
			c.Problem = "engine=shared is only valid for the background label"
			return c, nil
		}
		live, err := s.Label(ctx, LabelLive)
		if err != nil {
			return c, err
		}
		live.Label = label
		live.SharedWith = LabelLive
		return live, nil
	}
	if c.Engine != ModeLocal {
		return c, nil
	}
	s.resolveModels(ctx, d, &c)
	return c, nil
}

func (s SettingsSource) resolveModels(ctx context.Context, d LabelDef, c *LabelConfig) {
	ref := c.Model
	if ref == "" && d.DefaultModel != "" && s.Models != nil {
		if _, err := s.Models.LookupModel(ctx, d.DefaultModel); err == nil {
			ref = d.DefaultModel
			c.Model = ref
		}
	}
	if ref == "" {
		return
	}
	var info ModelInfo
	if filepath.IsAbs(ref) {
		if _, err := os.Stat(ref); err != nil {
			c.Problem = fmt.Sprintf("model file %s: %v", ref, err)
			return
		}
		info = ModelInfo{ID: ref, Path: ref}
	} else {
		if s.Models == nil {
			c.Problem = "model " + ref + " is not installed"
			return
		}
		var err error
		info, err = s.Models.LookupModel(ctx, ref)
		if err != nil {
			c.Problem = fmt.Sprintf("model %s: %v", ref, err)
			return
		}
	}
	c.ModelPath = info.Path
	if c.Context == 0 && d.ModelKind != ModelSTT {
		c.Context = info.ContextDefault
		if c.Context == 0 {
			c.Context = 8192
		}
	}
	if d.ModelKind != ModelLLM {
		c.MMProj = ""
		return
	}
	switch strings.ToLower(c.MMProj) {
	case "none", "off":
		c.MMProj = ""
		return
	case "":
		c.MMProj = info.MMProjID
	}
	if c.MMProj == "" {
		return
	}
	if filepath.IsAbs(c.MMProj) {
		if _, err := os.Stat(c.MMProj); err != nil {
			c.Problem = fmt.Sprintf("mmproj file %s: %v", c.MMProj, err)
			return
		}
		c.MMProjPath = c.MMProj
		return
	}
	mi, err := s.Models.LookupModel(ctx, c.MMProj)
	if err != nil {
		if info.MMProjID == c.MMProj {
			// The model's own projector isn't installed (yet): run text-only.
			c.MMProj = ""
			return
		}
		c.Problem = fmt.Sprintf("mmproj %s: %v", c.MMProj, err)
		return
	}
	c.MMProjPath = mi.Path
}
