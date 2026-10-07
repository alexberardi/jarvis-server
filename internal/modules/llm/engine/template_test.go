package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Pinned chat templates (ID12): the engine is launched with the template file, ahead of the
// operator's extra args; embeddings and whisper never get one.
func TestEngineArgvChatTemplate(t *testing.T) {
	c := LabelConfig{Kind: KindLlama, Model: "qwen3.5-9b", ModelPath: "/m/q.gguf", Context: 8192, Parallel: 1, GPULayers: 999,
		ExtraArgs: "--chat-template-file /mine.jinja"}
	k := keyFor(c, FlavourCUDA, "/e/bin")
	k.ChatTemplate = "/t/abc.jinja"
	args, err := k.args(9000, c.Model)
	if err != nil {
		t.Fatal(err)
	}
	want := `-m /m/q.gguf --host 127.0.0.1 --port 9000 --alias qwen3.5-9b --no-webui -c 8192 -np 1 -ngl 999 --jinja --chat-template-file /t/abc.jinja --chat-template-file /mine.jinja`
	if got := strings.Join(args, " "); got != want {
		t.Fatalf("argv\n got: %s\nwant: %s", got, want)
	}

	emb := keyFor(LabelConfig{Kind: KindLlama, ModelPath: "/m/e.gguf", Embedding: true}, FlavourCPU, "/e/bin")
	emb.ChatTemplate = "/t/abc.jinja"
	args, _ = emb.args(9000, "e")
	if slices.Contains(args, "--chat-template-file") {
		t.Fatalf("embedding engine got a template: %v", args)
	}
}

func TestTemplateFile(t *testing.T) {
	r := &Resolver{TemplateDir: filepath.Join(t.TempDir(), "templates")}
	p1, err := r.templateFile("{{ a }}")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p1); string(b) != "{{ a }}" {
		t.Fatalf("content %q", b)
	}
	if filepath.Dir(p1) != r.TemplateDir || !strings.HasSuffix(p1, ".jinja") {
		t.Fatalf("path %s", p1)
	}
	again, _ := r.templateFile("{{ a }}")
	if again != p1 {
		t.Fatal("same template, different file")
	}
	p2, _ := r.templateFile("{{ b }}")
	if p2 == p1 {
		t.Fatal("different templates share a file")
	}
	// A tampered file is rewritten.
	os.WriteFile(p1, []byte("tampered"), 0o644)
	if p, err := r.templateFile("{{ a }}"); err != nil || p != p1 {
		t.Fatal(p, err)
	}
	if b, _ := os.ReadFile(p1); string(b) != "{{ a }}" {
		t.Fatalf("not rewritten: %q", b)
	}
	entries, _ := os.ReadDir(r.TemplateDir)
	if len(entries) != 2 {
		t.Fatalf("%d files, want 2 (no temp files left)", len(entries))
	}
}

func TestResolvePinnedTemplateAndFold(t *testing.T) {
	g := newRig(t)
	g.r.TemplateDir = filepath.Join(t.TempDir(), "templates")
	m := g.model("qwen35.gguf")
	live := g.llm(LabelLive, m)
	live.ChatTemplate = "STRICT TEMPLATE"
	live.FoldSystemMessages = true
	g.src.set(live)

	ep := g.ready(LabelLive)
	if !ep.FoldSystemMessages {
		t.Fatal("fold flag not on the endpoint")
	}
	st := g.starts()
	if len(st) != 1 {
		t.Fatalf("%d starts", len(st))
	}
	i := slices.Index(st[0].Args, "--chat-template-file")
	if i < 0 || i+1 >= len(st[0].Args) {
		t.Fatalf("no template flag: %v", st[0].Args)
	}
	if b, err := os.ReadFile(st[0].Args[i+1]); err != nil || string(b) != "STRICT TEMPLATE" {
		t.Fatalf("template file %q %v", b, err)
	}

	// Another template is another engine.
	live.ChatTemplate = "OTHER TEMPLATE"
	g.src.set(live)
	if ep2 := g.ready(LabelLive); ep2.Engine == ep.Engine {
		t.Fatal("template change kept the engine")
	}

	// No template: no flag; the fold flag follows the config.
	plain := g.llm(LabelBackground, g.model("qwen3.gguf"))
	g.src.set(plain)
	if ep := g.ready(LabelBackground); ep.FoldSystemMessages {
		t.Fatal("unflagged model folds")
	}
	found := false
	for _, s := range g.starts() {
		if slices.Contains(s.Args, plain.ModelPath) {
			found = true
			if slices.Contains(s.Args, "--chat-template-file") {
				t.Fatalf("unpinned model got a template: %v", s.Args)
			}
		}
	}
	if !found {
		t.Fatal("background engine never started")
	}

	// Remote endpoints carry the flag as configured.
	g.src.set(LabelConfig{Label: LabelLive, Kind: KindLlama, ModelKind: ModelLLM, Engine: ModeRemote,
		RemoteURL: "http://x/v1", RemoteModel: "m", FoldSystemMessages: true})
	if ep := g.ready(LabelLive); !ep.Remote || !ep.FoldSystemMessages {
		t.Fatalf("remote %+v", ep)
	}
}
