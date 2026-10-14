package desktop

import (
	"slices"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
)

func misheardCatalogue() ([]apps.App, error) {
	return []apps.App{
		{Name: "Omacalc", Path: "/apps/omacalc.desktop", ID: "omacalc"},
		{Name: "Omacut", Path: "/apps/omacut.desktop", ID: "omacut"},
		{Name: "Omawrite", Path: "/apps/omawrite.desktop", ID: "omawrite"},
		{Name: "Neovim", Path: "/apps/nvim.desktop", ID: "nvim"},
		{Name: "Steam", Path: "/apps/steam.desktop", ID: "steam"},
		{Name: "Obsidian", Path: "/apps/obsidian.desktop", ID: "obsidian"},
	}, nil
}

// "Can you open Omerite and drop down a note to update Steam?" (heard for "open omawrite and
// jot down a note to update Steam"): the model listed the apps and couldn't match the name.
// desktop_open takes the name as heard and says what it launched.
func TestOpenMisheardApp(t *testing.T) {
	b := &fakeBackend{caps: all}
	now := time.Unix(100, 0)
	tr := &launches.Tracker{Now: func() time.Time { return now }}
	ts := New(host.New(b), OpenPolicy{}, misheardCatalogue)
	ts.Launched = tr
	for _, heard := range []string{"Omerite", "Oh my right"} {
		b.launched = nil
		out := call(t, ts, ToolOpen, map[string]any{"target": heard})
		if out["success"] != true || out["matched_app"] != "Omawrite" || out["fuzzy_match"] != true || out["heard_as"] != heard {
			t.Fatalf("%q: %v", heard, out)
		}
		if !slices.Equal(b.launched, []string{"/apps/omawrite.desktop"}) {
			t.Fatalf("launched %v", b.launched)
		}
	}
	if l, ok := tr.Recent(time.Minute); !ok || l.Name != "Omawrite" || l.ID != "omawrite" {
		t.Fatalf("launch not recorded: %+v", l)
	}
	out := call(t, ts, ToolOpen, map[string]any{"target": "Steam"})
	if out["matched_app"] != "Steam" || out["fuzzy_match"] != nil || out["heard_as"] != nil {
		t.Fatalf("exact: %v", out)
	}
}

// "Can you open the oven?" never launches Neovim: one sound away is a suggestion only.
func TestOpenSoundNearIsOnlySuggested(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, misheardCatalogue)
	out := call(t, ts, ToolOpen, map[string]any{"target": "the oven"})
	if out["success"] != false || out["error_code"] != "app_not_found" || len(b.launched) != 0 {
		t.Fatalf("%v launched %v", out, b.launched)
	}
	if s, _ := out["suggestions"].([]string); len(s) == 0 || s[0] != "Neovim" {
		t.Fatalf("suggestions %v", out["suggestions"])
	}
}

func TestListAppsLooksANameUp(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: all}), OpenPolicy{}, misheardCatalogue)
	out := call(t, ts, ToolListApps, map[string]any{"query": "Omerite"})
	if out["best_match"] != "Omawrite" || out["heard_as"] != "Omerite" {
		t.Fatalf("%v", out)
	}
	closest, _ := out["closest"].([]apps.Scored)
	if len(closest) == 0 || len(closest) > 3 || closest[0].Name != "Omawrite" {
		t.Fatalf("closest %v", closest)
	}
	out = call(t, ts, ToolListApps, map[string]any{"query": "the oven"})
	if out["best_match"] != nil {
		t.Fatalf("a sound-near name is no best match: %v", out)
	}
	if closest, _ := out["closest"].([]apps.Scored); len(closest) == 0 || closest[0].Name != "Neovim" {
		t.Fatalf("closest %v", out["closest"])
	}
	out = call(t, ts, ToolListApps, map[string]any{})
	if out["count"] != 6 || out["closest"] != nil {
		t.Fatalf("no query: %v", out)
	}
}
