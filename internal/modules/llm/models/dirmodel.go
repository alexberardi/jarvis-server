package models

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A directory model is a Hugging Face layout folder (config.json, *.safetensors, the
// tokenizer and chat template) that a Python engine runs: MLX on Apple Silicon, vLLM or
// Transformers elsewhere (engine.DirEngine). It installs as one model whose Path is the
// folder. A catalog GGUF gets its prompt provider and fold flag from its entry; a directory
// model gets them from its config.json and name (InferDirModel).

// DirChoiceFile is what an install request names as "file" for a repo's directory choice.
const DirChoiceFile = "config.json"

// dirModelFile says whether a repo file belongs to a directory model's install: the files at
// the repo's top level an engine reads (weights, configs, tokenizer, chat template). READMEs,
// images, GGUFs, subfolders (other formats, original checkpoints) and remote code are skipped.
func dirModelFile(name string) bool {
	if strings.Contains(name, "/") {
		return false
	}
	lower := strings.ToLower(name)
	switch path.Ext(lower) {
	case ".safetensors", ".json", ".jinja", ".model", ".tiktoken":
		return true
	case ".txt":
		return lower == "merges.txt" || lower == "vocab.txt"
	}
	return false
}

// dirChoice is a repo's directory model: its config and safetensors weights with the files
// beside them, or false when the repo has none.
func dirChoice(r Repo) (Choice, bool) {
	var files []RepoFile
	hasConfig, hasWeights := false, false
	for _, f := range r.Files {
		if !dirModelFile(f.Name) {
			continue
		}
		hasConfig = hasConfig || f.Name == DirChoiceFile
		hasWeights = hasWeights || strings.HasSuffix(strings.ToLower(f.Name), ".safetensors")
		files = append(files, f)
	}
	if !hasConfig || !hasWeights {
		return Choice{}, false
	}
	slices.SortFunc(files, func(a, b RepoFile) int {
		// config.json first: it names the choice (Choice.File).
		if (a.Name == DirChoiceFile) != (b.Name == DirChoiceFile) {
			if a.Name == DirChoiceFile {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	var size int64
	for _, f := range files {
		size += f.Size
	}
	tr := InferDirModel(path.Base(r.ID), nil)
	return Choice{File: DirChoiceFile, Kind: "llm", Quant: dirQuant(r.ID), Size: size, Shards: files, Directory: true,
		PromptProvider: tr.PromptProvider}, true
}

var dirQuantRe = regexp.MustCompile(`(?i)(?:^|[-_])(\d+bit|bf16|fp16|f16|fp8|mxfp4|awq|gptq)(?:[-_]|$)`)

// dirQuant is the quantisation in a directory model's repo name ("4bit", "BF16"), or "".
func dirQuant(repo string) string {
	m := dirQuantRe.FindStringSubmatch(path.Base(repo))
	if m == nil {
		return ""
	}
	return m[1]
}

// DirTraits is what a directory model's config.json and name say about the settings a
// catalog entry would give it.
type DirTraits struct {
	// Family is "qwen3.5" (Qwen 3.5 and 3.8: hybrid, strict chat template), "qwen3" or "".
	Family string `json:"family,omitempty"`
	// ParamsB is the parameter count in billions (from the name, else the config; 0 = unknown).
	ParamsB float64 `json:"params_b,omitempty"`
	// PromptProvider is the provider its GGUF catalog twin uses; "" = unknown, the user picks.
	PromptProvider string `json:"prompt_provider,omitempty"`
	// FoldSystemMessages: the family's chat template takes a system message only first and
	// needs a user turn, as the catalog says of its GGUF twins.
	FoldSystemMessages bool `json:"fold_system_messages,omitempty"`
}

var (
	// Qwen 3.5 / 3.8 in model_type ("qwen3_5", "qwen3_5_moe", "qwen3_5_text"), architectures
	// ("Qwen3_5ForConditionalGeneration") and names ("Qwen3.5-9B-4bit").
	qwen35Type = regexp.MustCompile(`(?i)^qwen3_?[58](?:_|for|$)`)
	qwen35Name = regexp.MustCompile(`(?i)qwen[-_ ]?3\.[58](?:[^0-9]|$)`)
	qwen3Type  = regexp.MustCompile(`(?i)^qwen3(?:_?moe)?(?:_|for|$)`)
	qwen3Name  = regexp.MustCompile(`(?i)qwen[-_ ]?3(?:[^0-9.]|$)`)
	paramsRe   = regexp.MustCompile(`(?i)(?:^|[-_ ])(\d+(?:\.\d+)?)b(?:[-_ ]|$)`)
)

// InferDirModel reads a directory model's traits from its name (the repo or folder name) and
// config.json (nil = name only).
func InferDirModel(name string, config []byte) DirTraits {
	var cfg dirConfig
	_ = json.Unmarshal(config, &cfg)
	types := []string{cfg.ModelType}
	if cfg.TextConfig != nil {
		types = append(types, cfg.TextConfig.ModelType)
	}
	types = append(types, cfg.Architectures...)
	var tr DirTraits
	for _, t := range types {
		if tr.Family != "" {
			break
		}
		switch {
		case qwen35Type.MatchString(t):
			tr.Family = "qwen3.5"
		case qwen3Type.MatchString(t):
			tr.Family = "qwen3"
		}
	}
	if tr.Family == "" && cfg.ModelType == "" && len(cfg.Architectures) == 0 {
		// No config to go by: the name alone.
		switch {
		case qwen35Name.MatchString(name):
			tr.Family = "qwen3.5"
		case qwen3Name.MatchString(name):
			tr.Family = "qwen3"
		}
	}
	if m := paramsRe.FindStringSubmatch(name); m != nil {
		tr.ParamsB, _ = strconv.ParseFloat(m[1], 64)
	} else {
		tr.ParamsB = cfg.params()
	}
	switch tr.Family {
	case "qwen3.5":
		// As the GGUF catalog: 9B on the 9B provider, the 27B on the 14B one.
		tr.PromptProvider, tr.FoldSystemMessages = "Qwen3_5_9B_Compressed", true
		if tr.ParamsB > 12 {
			tr.PromptProvider = "Qwen3_14B_Compressed"
		}
	case "qwen3":
		tr.PromptProvider = "Qwen3_8B_Compressed"
		if tr.ParamsB > 10 {
			tr.PromptProvider = "Qwen3_14B_Compressed"
		}
	}
	return tr
}

// InferDir is InferDirModel for a folder on disk; zero when it has no config.json.
func InferDir(dir string) DirTraits {
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return DirTraits{}
	}
	name := filepath.Base(dir)
	// An installed repo's folder is "owner--name".
	if _, after, ok := strings.Cut(name, "--"); ok {
		name = after
	}
	return InferDirModel(name, b)
}

// IsModelDir reports whether dir is a Hugging Face layout model: config.json and safetensors.
func IsModelDir(dir string) bool {
	if !fileExists(filepath.Join(dir, "config.json")) {
		return false
	}
	w, _ := filepath.Glob(filepath.Join(dir, "*.safetensors"))
	return len(w) > 0
}

// mlxRepo reports whether a repo is an MLX conversion (mlx-community/…, …-MLX-…): its
// quantised weights load only in MLX, whatever the label's dir_engine says.
func mlxRepo(repo string) bool { return strings.Contains(strings.ToLower(repo), "mlx") }

// ValidPromptProvider reports whether p is one jarvisd ports.
func ValidPromptProvider(p string) bool { return slices.Contains(KeptPromptProviders, p) }

type dirConfig struct {
	ModelType     string   `json:"model_type"`
	Architectures []string `json:"architectures"`
	HiddenSize    int64    `json:"hidden_size"`
	Layers        int64    `json:"num_hidden_layers"`
	Intermediate  int64    `json:"intermediate_size"`
	Vocab         int64    `json:"vocab_size"`
	TextConfig    *struct {
		ModelType    string `json:"model_type"`
		HiddenSize   int64  `json:"hidden_size"`
		Layers       int64  `json:"num_hidden_layers"`
		Intermediate int64  `json:"intermediate_size"`
		Vocab        int64  `json:"vocab_size"`
	} `json:"text_config"`
}

// params estimates a dense transformer's parameter count in billions (0 = unknown):
// attention and MLP per layer plus the embeddings.
func (c dirConfig) params() float64 {
	h, l, i, v := c.HiddenSize, c.Layers, c.Intermediate, c.Vocab
	if h == 0 && c.TextConfig != nil {
		h, l, i, v = c.TextConfig.HiddenSize, c.TextConfig.Layers, c.TextConfig.Intermediate, c.TextConfig.Vocab
	}
	if h == 0 || l == 0 {
		return 0
	}
	n := l*(4*h*h+3*h*i) + 2*v*h
	return float64(n) / 1e9
}
