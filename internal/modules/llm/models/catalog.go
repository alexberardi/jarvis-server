// Package models is the model manager (LD3): a catalog, Hugging Face lookup, resumable
// install jobs on the durable queue (fetching the matching engine build in the same job),
// the installed-model store, label assignment, and the admin HTTP API (docs/llm/06).
package models

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

//go:embed catalog.json
var catalogJSON []byte

// catalogData is the catalog in use (tests swap in their own).
var catalogData = catalogJSON

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
	URL            string   `json:"url,omitempty"`
	Archive        string   `json:"archive,omitempty"`
	File           string   `json:"file"`
	Size           int64    `json:"size"`
	SHA256         string   `json:"sha256"`
	MMProj         string   `json:"mmproj,omitempty"`
	ContextDefault int      `json:"context_default,omitempty"`
	ContextMax     int      `json:"context_max,omitempty"`
	KVBytesPerTok  int64    `json:"kv_bytes_per_token,omitempty"`
	PromptProvider string   `json:"prompt_provider,omitempty"`
	Thinking       bool     `json:"thinking,omitempty"`
	Dims           int      `json:"dims,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Notes          string   `json:"notes,omitempty"`
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
