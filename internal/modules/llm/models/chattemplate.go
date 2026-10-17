package models

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// toolsRef matches a Jinja tag or expression that references tools.
var toolsRef = regexp.MustCompile(`\{[%{][^%}]*\btools\b`)

// TemplateSupportsTools reports whether a chat template renders tools (a Jinja tag or
// expression references them), so the model calls tools natively.
func TemplateSupportsTools(tpl string) bool { return toolsRef.MatchString(tpl) }

// TemplateHasThinking reports whether a chat template has the enable_thinking switch.
func TemplateHasThinking(tpl string) bool { return strings.Contains(tpl, "enable_thinking") }

// ModelChatTemplate returns the chat template a model's files carry: a GGUF's
// tokenizer.chat_template, or for a transformers / MLX directory its chat_template.jinja,
// else tokenizer_config.json's chat_template. A file inside such a directory (a
// .safetensors file) uses its directory. "" with no error when there is none.
func ModelChatTemplate(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	dir := path
	if !fi.IsDir() {
		if strings.EqualFold(filepath.Ext(path), ".gguf") {
			return GGUFChatTemplate(path)
		}
		dir = filepath.Dir(path)
	}
	b, err := os.ReadFile(filepath.Join(dir, "chat_template.jinja"))
	if err == nil {
		return string(b), nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	b, err = os.ReadFile(filepath.Join(dir, "tokenizer_config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var cfg struct {
		ChatTemplate json.RawMessage `json:"chat_template"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", err
	}
	if len(cfg.ChatTemplate) == 0 || string(cfg.ChatTemplate) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(cfg.ChatTemplate, &s) == nil {
		return s, nil
	}
	// The list form: [{name, template}], "default" else the first.
	var list []struct {
		Name     string `json:"name"`
		Template string `json:"template"`
	}
	if err := json.Unmarshal(cfg.ChatTemplate, &list); err != nil {
		return "", err
	}
	for _, t := range list {
		if t.Name == "default" {
			return t.Template, nil
		}
	}
	if len(list) > 0 {
		return list[0].Template, nil
	}
	return "", nil
}
