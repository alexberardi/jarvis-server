package models

import (
	"os"
	"path/filepath"
	"testing"
)

func embeddedTemplates(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, n := range []string{"qwen3.jinja", "qwen3.5.jinja", "qwen3.8.jinja"} {
		b, err := templateFS.ReadFile("templates/" + n)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = string(b)
	}
	return out
}

func TestTemplateSupportsTools(t *testing.T) {
	for n, tpl := range embeddedTemplates(t) {
		if !TemplateSupportsTools(tpl) {
			t.Errorf("%s: no tools", n)
		}
	}
	for _, tpl := range []string{"{{ messages }}", "You can use tools. {{ messages }}"} {
		if TemplateSupportsTools(tpl) {
			t.Errorf("%q: tools", tpl)
		}
	}
}

func TestTemplateHasThinking(t *testing.T) {
	for n, tpl := range embeddedTemplates(t) {
		if !TemplateHasThinking(tpl) {
			t.Errorf("%s: no thinking", n)
		}
	}
	if TemplateHasThinking("{{ messages }}") {
		t.Error("{{ messages }}: thinking")
	}
}

func TestModelChatTemplate(t *testing.T) {
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func(name, path, want string) {
		t.Helper()
		got, err := ModelChatTemplate(path)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", name, got, err, want)
		}
	}
	check("gguf", writeGGUF(t, "GGUF", "G"), "G")

	str := t.TempDir()
	write(str, "tokenizer_config.json", `{"chat_template": "T"}`)
	check("string form", str, "T")
	write(str, "model.safetensors", "")
	check("file in the directory", filepath.Join(str, "model.safetensors"), "T")

	list := t.TempDir()
	write(list, "tokenizer_config.json", `{"chat_template": [{"name": "tool_use", "template": "U"}, {"name": "default", "template": "D"}]}`)
	check("list form", list, "D")
	first := t.TempDir()
	write(first, "tokenizer_config.json", `{"chat_template": [{"name": "a", "template": "A"}, {"name": "b", "template": "B"}]}`)
	check("list without default", first, "A")

	jinja := t.TempDir()
	write(jinja, "tokenizer_config.json", `{"chat_template": "T"}`)
	write(jinja, "chat_template.jinja", "J")
	check("chat_template.jinja wins", jinja, "J")

	check("empty directory", t.TempDir(), "")
}
