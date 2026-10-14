package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
)

// The fixture is cut where the agent cuts its own vocabulary.
func TestMaxVocabularyMatchesTheAgent(t *testing.T) {
	if maxVocabulary != dictate.MaxVocabulary {
		t.Fatalf("maxVocabulary %d, the agent's dictate.MaxVocabulary %d", maxVocabulary, dictate.MaxVocabulary)
	}
}

func TestDesktopVocabulary(t *testing.T) {
	root, _ := filepath.Abs("../..")
	v, err := DesktopVocabulary(root)
	if err != nil {
		t.Fatal(err)
	}
	// Open windows first, then the catalogue's one-word names, as the agent orders them.
	if !strings.HasPrefix(v, "Alacritty, Firefox, Omawrite, Aether, Basecamp") || !strings.Contains(v, "Neovim") {
		t.Fatalf("vocabulary %q", v)
	}
	if len(v) > maxVocabulary || strings.Count(v, "Omawrite") != 1 {
		t.Fatalf("vocabulary %q (%d chars)", v, len(v))
	}
	if _, err := DesktopVocabulary(t.TempDir()); err == nil {
		t.Fatal("no fixture, no error")
	}
}

// A desktop scenario (the desktop toolset or a desktop node) with no stt_vocabulary sends the
// fixture's, as a real agent always sends its app names; its own wins, and "" sends none.
func TestScenarioVocabulary(t *testing.T) {
	root, _ := filepath.Abs("../..")
	def, err := DesktopVocabulary(root)
	if err != nil {
		t.Fatal(err)
	}
	scenarioFile := filepath.Join(root, "tools/pipeline/scenarios/x.json")
	own, none := "Zed, Ghostty", ""
	for _, c := range []struct {
		name string
		sc   Scenario
		want string
	}{
		{"node tools", Scenario{Tools: "node"}, ""},
		{"default tools", Scenario{}, ""},
		{"desktop toolset from a scenario file", Scenario{Tools: "../toolsets/desktop.json", File: scenarioFile}, def},
		{"desktop toolset by path (say)", Scenario{Tools: filepath.Join(root, "tools/pipeline/toolsets/desktop.json")}, def},
		{"desktop node", Scenario{Node: "desktop", Tools: "none"}, def},
		{"own vocabulary", Scenario{Tools: "../toolsets/desktop.json", File: scenarioFile, STTVocabulary: &own}, own},
		{"opted out", Scenario{Node: "desktop", STTVocabulary: &none}, ""},
		{"own on a node scenario", Scenario{STTVocabulary: &own}, own},
	} {
		got, err := scenarioVocabulary(root, c.sc, resolveToolSpec(c.sc))
		if err != nil || got != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// A desktop scenario without stt_vocabulary sends the default at /conversation/start.
func TestDriverSendsDesktopVocabulary(t *testing.T) {
	cc := &fakeCC{}
	srv := httptest.NewServer(cc)
	defer srv.Close()
	root, _ := filepath.Abs("../..")
	def, _ := DesktopVocabulary(root)
	d := &Driver{Node: &Node{BaseURL: srv.URL, ID: "node", Key: "key"}, Root: root}
	sc := Scenario{Name: "desk", Tools: filepath.Join(root, "tools/pipeline/toolsets/desktop.json"), Home: "none",
		Turns: []Turn{{Say: "hello"}}}
	if l := d.Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	if nc, _ := cc.started["node_context"].(map[string]any); nc["stt_vocabulary"] != def {
		t.Fatalf("vocabulary not sent: %v", nc)
	}
	none := ""
	sc.STTVocabulary = &none
	if l := d.Run(context.Background(), sc); !l.Pass {
		t.Fatalf("%+v", l)
	}
	if nc, _ := cc.started["node_context"].(map[string]any); nc["stt_vocabulary"] != nil {
		t.Fatalf("opted out, but sent: %v", nc)
	}
}

// LoadScenarios keeps "stt_vocabulary": "" apart from no stt_vocabulary.
func TestLoadScenariosVocabularyOptOut(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "s.json")
	if err := os.WriteFile(f, []byte(`[{"name":"a","turns":[{"say":"hi"}]},{"name":"b","stt_vocabulary":"","turns":[{"say":"hi"}]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	scs, err := LoadScenarios([]string{f})
	if err != nil {
		t.Fatal(err)
	}
	if scs[0].STTVocabulary != nil || scs[1].STTVocabulary == nil || *scs[1].STTVocabulary != "" {
		t.Fatalf("%+v", scs)
	}
}
