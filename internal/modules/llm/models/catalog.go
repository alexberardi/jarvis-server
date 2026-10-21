// Package models is the model manager (LD3): a catalog, Hugging Face lookup, resumable
// install jobs on the durable queue (fetching the matching engine build in the same job),
// the installed-model store, label assignment, and the admin HTTP API (docs/llm/06).
package models

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

//go:embed catalog.json
var catalogJSON []byte

// catalogData is the catalog in use (tests swap in their own).
var catalogData = catalogJSON

// templateFS holds the pinned chat templates (ID12): llama-server renders a catalog model's
// prompts with the template its entry names, never the one inside the GGUF, so a new upload
// (or a catalog bump to one) can't change how requests are rendered without a reviewed change
// here. Each file is the template the GGUF at the entry's pinned revision ships, byte for byte.
//
//go:embed templates/*.jinja
var templateFS embed.FS

// Entry is one catalog model file. Model entries that can see images name their projector
// entry in MMProj.
type Entry struct {
	ID       string `json:"id"`
	Display  string `json:"display"`
	Kind     string `json:"kind"` // llm | mmproj | embedding | stt
	Repo     string `json:"repo,omitempty"`
	Revision string `json:"revision,omitempty"`
	// URL downloads from somewhere other than Hugging Face (sherpa-onnx releases). Archive
	// (tar.bz2, tar.gz, zip) means the file is extracted into a directory, the model's path.
	URL            string `json:"url,omitempty"`
	Archive        string `json:"archive,omitempty"`
	File           string `json:"file"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	MMProj         string `json:"mmproj,omitempty"`
	ContextDefault int    `json:"context_default,omitempty"`
	ContextMax     int    `json:"context_max,omitempty"`
	// KVBytesPerTok is the f16 KV cache per token of context. Hybrid models (Qwen3.5/3.8:
	// one full-attention layer in four, the rest linear attention with a small fixed state)
	// count only their full-attention layers.
	KVBytesPerTok int64 `json:"kv_bytes_per_token,omitempty"`
	// HostBytes is what llama.cpp keeps in system RAM even with every layer on the GPU: the
	// input token-embedding table, when the model has a separate output matrix (a model with
	// tied embeddings needs that table on the card for its output, so it has none). From the
	// GGUF's token_embd.weight.
	HostBytes      int64  `json:"host_bytes,omitempty"`
	PromptProvider string `json:"prompt_provider,omitempty"`
	Thinking       bool   `json:"thinking,omitempty"`
	Dims           int    `json:"dims,omitempty"`
	// ChatTemplate names the pinned chat template (a file under templates/) and
	// ChatTemplateSHA256 its digest; LLM entries only (ID12).
	ChatTemplate       string `json:"chat_template,omitempty"`
	ChatTemplateSHA256 string `json:"chat_template_sha256,omitempty"`
	// FoldSystemMessages: the template accepts a system message only first and needs a user
	// turn, so the llm module folds requests for it (llm.FoldSystemMessages).
	FoldSystemMessages bool `json:"fold_system_messages,omitempty"`
	// Directory: a Hugging Face layout model (config.json + safetensors) for a Python engine
	// (MLX on Apple Silicon). File is DirChoiceFile and Size the folder's approximate total;
	// the install lists the repo at Revision ("" = main) and downloads the folder's files,
	// each checked against the listing's sha256 (SHA256 is empty).
	Directory bool `json:"directory,omitempty"`
	// MinMemoryMB is the least GPU or unified memory the entry needs to be worth installing
	// (a 27B on a 16 GB Mac would leave too little for anything else); 0 = judged by its fit.
	MinMemoryMB int64    `json:"min_memory_mb,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

// Template returns the entry's pinned chat template ("" when it pins none), checked against
// its digest.
func (e Entry) Template() (string, error) {
	if e.ChatTemplate == "" {
		return "", nil
	}
	b, err := templateFS.ReadFile("templates/" + e.ChatTemplate)
	if err != nil {
		return "", fmt.Errorf("models: %s: chat template %s: %w", e.ID, e.ChatTemplate, err)
	}
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != e.ChatTemplateSHA256 {
		return "", fmt.Errorf("models: %s: chat template %s has sha256 %s, catalog pins %s", e.ID, e.ChatTemplate, got, e.ChatTemplateSHA256)
	}
	return string(b), nil
}

// KeptPromptProviders are the providers jarvisd ports (PLAN Appendix B, D11: an unknown
// provider is a hard error).
var KeptPromptProviders = []string{"Qwen3_14B_Compressed", "Qwen3_8B_Compressed", "Qwen3_5_9B_Compressed"}

// Catalog returns the built-in catalog.
func Catalog() []Entry {
	var c []Entry
	if err := json.Unmarshal(catalogData, &c); err != nil {
		panic(fmt.Sprintf("models: bad embedded catalog: %v", err))
	}
	return c
}

// CatalogEntry finds an entry by id.
func CatalogEntry(id string) (Entry, bool) {
	i := slices.IndexFunc(Catalog(), func(e Entry) bool { return e.ID == id })
	if i < 0 {
		return Entry{}, false
	}
	return Catalog()[i], true
}

// EngineKind is the engine that runs a model kind; false for in-binary kinds (tts, speaker).
func EngineKind(modelKind string) (engine.Kind, bool) {
	switch modelKind {
	case engine.ModelSTT:
		return engine.KindWhisper, true
	case engine.ModelLLM, engine.ModelMMProj, engine.ModelEmbedding:
		return engine.KindLlama, true
	}
	return "", false
}

// ValidKind reports whether k is a model kind.
func ValidKind(k string) bool {
	switch k {
	case engine.ModelLLM, engine.ModelMMProj, engine.ModelEmbedding, engine.ModelSTT, KindTTS, KindSpeaker:
		return true
	}
	return false
}
