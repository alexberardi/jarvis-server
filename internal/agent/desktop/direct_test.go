package desktop

import (
	"context"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestParseSwitch(t *testing.T) {
	for said, want := range map[string]switchRequest{
		"Switch to the Claude tab.":                     {target: "Claude", views: true},
		"switch to the Discord tab in Firefox.":         {target: "Discord", app: "Firefox", views: true},
		"Go to Discord":                                 {target: "Discord", views: true, windows: true},
		"switch to the terminal window":                 {target: "terminal", windows: true},
		"Hey Jarvis, switch over to Jarvis Automation.": {target: "Jarvis Automation", views: true, windows: true},
	} {
		got, ok := parseSwitch(said)
		if !ok || got != want {
			t.Errorf("%q: %+v %v, want %+v", said, got, ok, want)
		}
	}
	for _, said := range []string{"switch to the Claude tab and type hello", "what's the weather",
		"switch to it", "go to the other one", "switch the lights off", "open Discord"} {
		if r, ok := parseSwitch(said); ok {
			t.Errorf("%q parsed as %+v: the model's", said, r)
		}
	}
}

// Tonight's session (2026-10-25), replayed: what stage 1 did with each request.
func TestDirectSwitches(t *testing.T) {
	b := &fakeBackend{caps: hypr, windows: []host.Window{
		{ID: "0xf", Title: "YouTube — Mozilla Firefox", App: "firefox", Focused: true},
		{ID: "0xd", Title: "Discord", App: "discord"},
		{ID: "0x7", Title: "~/jarvis", App: "foot"},
	}}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	fv := &fakeViews{views: firefoxTabs}
	ts.Views = fv
	ctx := context.Background()

	// An exact title wins over the tab that only contains it.
	out, ok := ts.Direct(ctx, "Switch to the Jarvis Automation tab.")
	if !ok || out["success"] != true || !slices.Equal(fv.switched, []string{"Jarvis Automation"}) {
		t.Fatalf("exact: %v %v %v", out, ok, fv.switched)
	}
	// A misheard tab still lands.
	fv.switched = nil
	if out, ok := ts.Direct(ctx, "Switch to the Cloud Fair tab."); !ok || out["success"] != true || len(fv.switched) != 1 {
		t.Fatalf("misheard: %v %v", out, fv.switched)
	}
	// A window, not a tab.
	if out, ok := ts.Direct(ctx, "switch to the terminal"); !ok || out["success"] != true || !slices.Contains(b.actions, "focus 0x7") {
		t.Fatalf("window: %v %v", out, b.actions)
	}
	// "Discord" is the Discord app's window and a Firefox tab: ask, then act on the answer.
	fv.switched = nil
	out, ok = ts.Direct(ctx, "Switch to Discord.")
	if !ok || out[node.ChoiceKey] != true {
		t.Fatalf("both: %v %v", out, ok)
	}
	if out, ok := ts.AnswerChoice(ctx, "the second one"); !ok || out["success"] != true || len(fv.switched) != 1 {
		t.Fatalf("answered: %v %v %v", out, ok, fv.switched)
	}
	// Not a switch: on to command-center.
	if _, ok := ts.Direct(ctx, "what tabs do I have open"); ok {
		t.Fatal("a question handled locally")
	}
	// Nothing open by that name: on to command-center (it may be an app to open).
	if _, ok := ts.Direct(ctx, "switch to Spotify"); ok {
		t.Fatal("an unknown name handled locally")
	}
}
