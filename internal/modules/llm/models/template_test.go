package models

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// strictMarkers are what a template raises on that jarvisd's message layout triggers (ID12).
var strictMarkers = []string{"System message must be at the beginning", "No user query found"}

// Every catalog LLM pins a template matching its digest, the fold flag says exactly whether
// that template is strict, and every shipped template is used.
func TestCatalogChatTemplates(t *testing.T) {
	used := map[string]bool{}
	for _, e := range Catalog() {
		if e.Kind != engine.ModelLLM {
			if e.ChatTemplate != "" || e.FoldSystemMessages {
				t.Errorf("%s (%s) pins a chat template", e.ID, e.Kind)
			}
			continue
		}
		tpl, err := e.Template()
		if err != nil || tpl == "" {
			t.Errorf("%s: %v (template %q)", e.ID, err, e.ChatTemplate)
			continue
		}
		used[e.ChatTemplate] = true
		strict := false
		for _, m := range strictMarkers {
			strict = strict || strings.Contains(tpl, m)
		}
		if strict != e.FoldSystemMessages {
			t.Errorf("%s: template strict=%v but fold_system_messages=%v", e.ID, strict, e.FoldSystemMessages)
		}
	}
	files, _ := fs.Glob(templateFS, "templates/*.jinja")
	for _, f := range files {
		if !used[filepath.Base(f)] {
			t.Errorf("%s is not used by any catalog entry", f)
		}
	}
	// The A10 F10 models.
	for _, id := range []string{"qwen3.5-9b", "qwen3.8-27b"} {
		if e, _ := CatalogEntry(id); !e.FoldSystemMessages {
			t.Errorf("%s must fold", id)
		}
	}
}

func TestTemplateDigestMismatch(t *testing.T) {
	_, err := Entry{ID: "x", ChatTemplate: "qwen3.jinja", ChatTemplateSHA256: strings.Repeat("0", 64)}.Template()
	if err == nil || !strings.Contains(err.Error(), "catalog pins") {
		t.Fatalf("err %v", err)
	}
	if _, err := (Entry{ID: "x", ChatTemplate: "missing.jinja"}).Template(); err == nil {
		t.Fatal("missing template accepted")
	}
	if tpl, err := (Entry{ID: "x"}).Template(); tpl != "" || err != nil {
		t.Fatal(tpl, err)
	}
}

// pinTestCatalog is testCatalog with "tiny" pinned to the strict Qwen 3.5 template.
func (e *env) pinTestCatalog() Entry {
	e.testCatalog()
	cat := Catalog()
	q35, _ := entryFromEmbedded("qwen3.5-9b")
	for i := range cat {
		if cat[i].ID == "tiny" {
			cat[i].ChatTemplate, cat[i].ChatTemplateSHA256, cat[i].FoldSystemMessages = q35.ChatTemplate, q35.ChatTemplateSHA256, true
		}
	}
	b, _ := json.Marshal(cat)
	catalogData = b // testCatalog's cleanup restores the real one
	tiny, _ := CatalogEntry("tiny")
	return tiny
}

func entryFromEmbedded(id string) (Entry, bool) {
	var c []Entry
	_ = json.Unmarshal(catalogJSON, &c)
	for _, e := range c {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

func TestLookupModelAndLabelFold(t *testing.T) {
	e := newEnv(t)
	tiny := e.pinTestCatalog()
	want, _ := tiny.Template()
	p := filepath.Join(t.TempDir(), "tiny.gguf")
	os.WriteFile(p, []byte("x"), 0o644)
	e.mgr.Store.Upsert(e.ctx, Model{ID: "tiny", Kind: "llm", CatalogID: "tiny", Path: p, State: StateReady, Files: []File{{Name: "tiny.gguf"}}})
	e.mgr.Store.Upsert(e.ctx, Model{ID: "mine", Kind: "llm", Path: p, State: StateReady, External: true, Files: []File{{Name: "tiny.gguf"}}})

	info, err := e.mgr.Store.LookupModel(e.ctx, "tiny")
	if err != nil || info.ChatTemplate != want || !info.FoldSystemMessages {
		t.Fatalf("catalog model: %+v %v", info, err)
	}
	if info, _ := e.mgr.Store.LookupModel(e.ctx, "mine"); info.ChatTemplate != "" || info.FoldSystemMessages {
		t.Fatalf("hand-registered model got a pin: %+v", info)
	}

	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store}
	label := func() engine.LabelConfig {
		c, err := src.Label(e.ctx, "live")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	sc := settings.Scope{}
	e.set.Set(e.ctx, "llm.live.model", "tiny", sc)
	if c := label(); !c.FoldSystemMessages || c.ChatTemplate != want {
		t.Fatalf("auto, strict model: %+v", c)
	}
	e.set.Set(e.ctx, "llm.live.fold_system_messages", "off", sc)
	if c := label(); c.FoldSystemMessages || c.ChatTemplate != want {
		t.Fatalf("off: %+v", c)
	}
	e.set.Set(e.ctx, "llm.live.model", "mine", sc)
	e.set.Set(e.ctx, "llm.live.fold_system_messages", "auto", sc)
	if c := label(); c.FoldSystemMessages || c.ChatTemplate != "" {
		t.Fatalf("auto, unflagged model: %+v", c)
	}
	e.set.Set(e.ctx, "llm.live.fold_system_messages", "on", sc)
	if c := label(); !c.FoldSystemMessages {
		t.Fatalf("on: %+v", c)
	}
	// Remote: only when on.
	e.set.Set(e.ctx, "llm.live.engine", "remote", sc)
	e.set.Set(e.ctx, "llm.live.remote_url", "http://x/v1", sc)
	if c := label(); !c.FoldSystemMessages {
		t.Fatalf("remote on: %+v", c)
	}
	e.set.Set(e.ctx, "llm.live.fold_system_messages", "auto", sc)
	e.set.Set(e.ctx, "llm.live.model", "tiny", sc)
	if c := label(); c.FoldSystemMessages || c.ChatTemplate != "" {
		t.Fatalf("remote auto: %+v", c)
	}
	// Shared background follows live's engine, flag included.
	e.set.Set(e.ctx, "llm.live.engine", "local", sc)
	e.set.Set(e.ctx, "llm.background.engine", "shared", sc)
	if c, _ := src.Label(e.ctx, "background"); !c.FoldSystemMessages || c.ChatTemplate != want {
		t.Fatalf("shared background: %+v", c)
	}
	if _, ok := e.set.Definition("llm.embeddings.fold_system_messages"); ok {
		t.Fatal("embeddings label has a fold setting")
	}
}

func TestRegisterCatalogFile(t *testing.T) {
	e := newEnv(t)
	tiny := e.pinTestCatalog()
	body := e.hub.files["/acme/Tiny-GGUF/resolve/rev1/Tiny-Q4_K_M.gguf"]
	if len(body) == 0 {
		t.Fatal("hub file path changed")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "copy.gguf")
	os.WriteFile(good, body, 0o644)

	var re *RequestError
	short := filepath.Join(dir, "short.gguf")
	os.WriteFile(short, body[:10], 0o644)
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: short, CatalogID: "tiny"}); !errors.As(err, &re) || !strings.Contains(re.Msg, "bytes") {
		t.Fatalf("wrong size: %v", err)
	}
	other := filepath.Join(dir, "other.gguf")
	changed := append([]byte(nil), body...)
	changed[0] ^= 1
	os.WriteFile(other, changed, 0o644)
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: other, CatalogID: "tiny"}); !errors.As(err, &re) || !strings.Contains(re.Msg, "sha256") {
		t.Fatalf("wrong content: %v", err)
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: good, CatalogID: "nope"}); err == nil {
		t.Fatal("unknown catalog id accepted")
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: good, CatalogID: "tiny", Kind: "stt"}); err == nil {
		t.Fatal("kind mismatch accepted")
	}

	m, err := e.mgr.Register(e.ctx, RegisterRequest{Path: good, CatalogID: "tiny", ID: "tiny"})
	if err != nil {
		t.Fatal(err)
	}
	if m.CatalogID != "tiny" || m.Kind != "llm" || m.PromptProvider != tiny.PromptProvider || m.ContextDefault != tiny.ContextDefault ||
		m.Display != tiny.Display || !m.External || m.Path != good {
		t.Fatalf("registered %+v", m)
	}
	info, err := e.mgr.Store.LookupModel(e.ctx, "tiny")
	if err != nil || info.ChatTemplate == "" || !info.FoldSystemMessages {
		t.Fatalf("lookup %+v %v", info, err)
	}
	// Deleting a registered catalog file leaves the file alone.
	if err := e.mgr.Delete(e.ctx, "tiny", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatal("registered file deleted")
	}
}
