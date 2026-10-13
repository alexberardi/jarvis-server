package dictate

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// The real failure (DESIGN §14): the user said "type into omawrite that I need to take the
// dogs for a walk". omawrite is a Qt app whose Hyprland class is "omawrite"; speech-to-text
// heard "Omerite", the model called desktop_type_text with that name, nothing matched, and the
// model told the user "Done. I've typed that into Omerite." anyway.
const (
	omeriteTranscript = "Type into Omerite that I need to take the dogs for a walk."
	omeriteArgs       = `{"app": "Omerite", "text": "I need to take the dogs for a walk"}` // what the model called
)

func omawriteDesktop(withOmawrite bool) *fakeWin {
	w := &fakeWin{active: "term", ws: []Window{
		{ID: "term", Apps: []string{"Alacritty", "Alacritty"}, Title: "~", Recency: 0, Visible: true},
		{ID: "ff", Apps: []string{"firefox", "firefox"}, Title: "Docs", Recency: 1, Visible: true},
	}}
	if withOmawrite {
		w.ws = append(w.ws, Window{ID: "ow", Apps: []string{"omawrite", "omawrite"}, Title: "notes.md", Recency: 2, Visible: false})
	}
	return w
}

func omeriteCall(t *testing.T, win *fakeWin) (map[string]any, *fakeKB, []string) {
	t.Helper()
	if !strings.Contains(omeriteTranscript, "Omerite") {
		t.Fatal("transcript")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(omeriteArgs), &args); err != nil {
		t.Fatal(err)
	}
	kb := &fakeKB{ok: true}
	var island []string
	ts := &Tools{KB: kb, Win: win, Sleep: noSleep, Show: func(s string, _ time.Duration) { island = append(island, s) }}
	return call(t, ts, ToolType, args), kb, island
}

func TestMisheardAppIsTypedIntoTheNearOne(t *testing.T) {
	win := omawriteDesktop(true)
	out, kb, island := omeriteCall(t, win)
	if out["success"] != true || out["matched_app"] != "omawrite" || out["fuzzy_match"] != true || out["heard_as"] != "Omerite" ||
		out["into"] != "Omawrite" {
		t.Fatalf("result %v", out)
	}
	if !slices.Equal(kb.typed, []string{"I need to take the dogs for a walk"}) || len(kb.keys) != 0 || !slices.Equal(win.focused, []string{"ow"}) {
		t.Fatalf("typed %q keys %v focused %v", kb.typed, kb.keys, win.focused)
	}
	if !slices.Equal(island, []string{"Typing into Omawrite: I need to take the dogs for a walk"}) {
		t.Fatalf("island %q", island)
	}
}

func TestMisheardAppWithNothingNearFailsLoudly(t *testing.T) {
	out, kb, island := omeriteCall(t, omawriteDesktop(false))
	if len(kb.typed) != 0 {
		t.Fatalf("typed %q", kb.typed)
	}
	if out["success"] != false || out["error_code"] != "window_not_found" {
		t.Fatalf("result %v", out)
	}
	want := `Nothing was typed: no open window matches "Omerite". Open apps: Alacritty, Firefox. ` +
		"Tell the user nothing was typed and ask which app they meant."
	if out["error"] != want {
		t.Fatalf("error\n got %q\nwant %q", out["error"], want)
	}
	if apps, _ := out["open_apps"].([]string); !slices.Equal(apps, []string{"Alacritty", "Firefox"}) {
		t.Fatalf("open_apps %v", out["open_apps"])
	}
	if !slices.Equal(island, []string{"Couldn't find Omerite — nothing typed"}) {
		t.Fatalf("island %q", island)
	}
	// The output never carries the dictated words.
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "dogs") {
		t.Fatalf("output echoes the text: %s", b)
	}
}

func TestFailureTexts(t *testing.T) {
	kb := &fakeKB{ok: true}
	var island []string
	ts := &Tools{KB: kb, Win: newWin(), Sleep: noSleep, Show: func(s string, _ time.Duration) { island = append(island, s) }}
	out := call(t, ts, ToolType, map[string]any{"text": "x", "app": "kitty"})
	if e := out["error"].(string); !strings.HasPrefix(e, `Nothing was typed: more than one window could be "kitty" (2, listed in candidates)`) ||
		!strings.HasSuffix(e, "ask which one they meant.") {
		t.Errorf("ambiguous: %q", e)
	}
	win := newWin()
	win.focusLag = -1
	ts.Win = win
	out = call(t, ts, ToolPress, map[string]any{"key": "enter", "app": "claude"})
	if out["error"] != "No key was pressed: focus not confirmed: asked Claude to come to the front, but it didn't get the keyboard focus. "+
		"Tell the user no key was pressed and why." {
		t.Errorf("focus: %q", out["error"])
	}
	want := []string{"Which kitty? — nothing typed", "Couldn't switch windows — no key pressed"}
	if !slices.Equal(island, want) {
		t.Errorf("island %q", island)
	}
}
