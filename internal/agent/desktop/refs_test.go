package desktop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Newest first, as Hyprland and sway list them: the terminal has focus, Firefox was before it.
var recent = []host.Window{
	{ID: "0xa", Title: "~", App: "foot", Focused: true},
	{ID: "0xb", Title: "Mozilla Firefox", App: "firefox"},
	{ID: "0xc", Title: "general", App: "Slack"},
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func refTools(b *fakeBackend, c *clock) *Tools {
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	ts.Now = c.now
	return ts
}

// Q7: "it" is the window Jarvis last acted on for a minute, then the focused one; "the other
// one" the window focused before this one.
func TestWindowReferences(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	b := &fakeBackend{caps: hypr, windows: recent}
	ts := refTools(b, c)
	do := func(args map[string]any) string {
		t.Helper()
		b.actions = nil
		if out := call(t, ts, ToolWindow, args); out["success"] != true {
			t.Fatalf("%v: %v", args, out)
		}
		return strings.Join(b.actions, ";")
	}
	if got := do(map[string]any{"action": "maximize", "window": "slack"}); got != "maximize 0xc" {
		t.Fatalf("named: %s", got)
	}
	c.t = c.t.Add(30 * time.Second)
	if got := do(map[string]any{"action": "unmaximize", "window": "it"}); got != "unmaximize 0xc" {
		t.Fatalf("it within a minute: %s", got)
	}
	c.t = c.t.Add(61 * time.Second)
	if got := do(map[string]any{"action": "maximize", "window": "that"}); got != "maximize 0xa" {
		t.Fatalf("it after a minute is the focused window: %s", got)
	}
	if got := do(map[string]any{"action": "focus", "window": "the other one"}); got != "focus 0xb" {
		t.Fatalf("the other one: %s", got)
	}
	if got := do(map[string]any{"action": "maximize", "window": "this"}); got != "maximize 0xa" {
		t.Fatalf("this: %s", got)
	}
}

// Without a focus flag the host lists in no known order: "the other one" asks which window.
func TestTheOtherOneNeedsRecencyOrder(t *testing.T) {
	ws := []host.Window{{ID: "1", App: "Terminal"}, {ID: "2", App: "Mail"}}
	b := &fakeBackend{caps: all, windows: ws}
	out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, map[string]any{"action": "focus", "window": "the other one"})
	if out["error_code"] != "invalid_argument" || len(b.actions) != 0 {
		t.Fatalf("%v %v", out, b.actions)
	}
}

// Q9: a window command's outcome goes on the island, marked island_only; a failure only when the
// action isn't supported (not found and ambiguous still go back to the model to ask or retry).
func TestWindowCommandsShowOnTheIsland(t *testing.T) {
	var shown []string
	b := &fakeBackend{caps: hypr, windows: recent}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	ts.Show = func(text string, _ time.Duration) { shown = append(shown, text) }
	out := call(t, ts, ToolWindow, map[string]any{"action": "snap_left", "window": "firefox"})
	if out[node.IslandOnlyKey] != true || out[node.IslandKey] != "Snapped firefox left" {
		t.Fatalf("success: %v", out)
	}
	out = call(t, ts, ToolWindow, map[string]any{"action": "minimize", "window": "firefox"})
	if out[node.IslandOnlyKey] != true || out[node.IslandKey] != UnsupportedMessage || out["success"] != false {
		t.Fatalf("unsupported: %v", out)
	}
	out = call(t, ts, ToolWindow, map[string]any{"action": "focus", "window": "spotify"})
	if out[node.IslandOnlyKey] != nil {
		t.Fatalf("not found is for the model to retry: %v", out)
	}
	out = call(t, ts, ToolClose, map[string]any{"window_id": "the terminal"})
	if out[node.IslandOnlyKey] != true || out[node.IslandKey] != "Closed foot" {
		t.Fatalf("close: %v", out)
	}
	if strings.Join(shown, "|") != "Snapped firefox left|"+UnsupportedMessage+"|Closed foot" {
		t.Fatalf("island showed %q", shown)
	}
}

// Q8: closing one window never asks; closing every window of an app asks when confirmation is
// on, and does it only on the yes.
func TestCloseAllOfAnAppAsksFirst(t *testing.T) {
	ws := []host.Window{{ID: "0xa", App: "foot", Focused: true}, {ID: "0xb", App: "firefox"}, {ID: "0xd", App: "foot", Title: "logs"}}
	b := &fakeBackend{caps: hypr, windows: ws}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmSubmit}
	out := call(t, ts, ToolClose, map[string]any{"window_id": "the terminal", "all_of_app": true})
	if out["status"] != "awaiting_confirmation" || out["success"] != false || len(b.actions) != 0 {
		t.Fatalf("parked: %v %v", out, b.actions)
	}
	if q, _ := out["question"].(string); q != "Close all 2 foot windows?" {
		t.Fatalf("question %q", q)
	}
	res, err := ts.Confirm.Answer(context.Background(), out["confirmation_id"].(string), "yes")
	if err != nil || strings.Join(b.actions, ";") != "close 0xa;close 0xd" || res[node.IslandKey] != "Closed 2 foot windows" {
		t.Fatalf("after yes: %v %v %v", res, err, b.actions)
	}
	b.actions = nil
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmOff}
	out = call(t, ts, ToolClose, map[string]any{"window_id": "foot", "all_of_app": true})
	if out["success"] != true || len(b.actions) != 2 {
		t.Fatalf("full control closes without asking: %v %v", out, b.actions)
	}
}

func TestCloseIsNoLongerRisky(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: all}), OpenPolicy{}, catalogue)
	for _, d := range ts.Available(context.Background()) {
		if d.Name == ToolClose && d.ClientTool["is_risky"] != false {
			t.Fatalf("close is risky: %v", d.ClientTool["is_risky"])
		}
	}
}

// The model often drops "the other one" or "it": with no window in the call, the words the user
// said decide.
func TestWhatWasSaidFillsAnOmittedWindow(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	b := &fakeBackend{caps: hypr, windows: recent}
	ts := refTools(b, c)
	run := func(said string, args map[string]any) string {
		t.Helper()
		b.actions = nil
		out, _ := ts.Call(context.Background(), ToolWindow, args, node.Request{VoiceCommand: said})
		if out["success"] != true {
			t.Fatalf("%q: %v", said, out)
		}
		return strings.Join(b.actions, ";")
	}
	if got := run("switch to the other window", map[string]any{"action": "focus"}); got != "focus 0xb" {
		t.Fatalf("the other window: %s", got)
	}
	run("maximize slack", map[string]any{"action": "maximize", "window": "slack"})
	if got := run("actually restore it", map[string]any{"action": "unmaximize"}); got != "unmaximize 0xc" {
		t.Fatalf("it: %s", got)
	}
	if got := run("maximize this window", map[string]any{"action": "maximize"}); got != "maximize 0xa" {
		t.Fatalf("this: %s", got)
	}
	// A window named in the call wins over the words.
	if got := run("switch to the other window", map[string]any{"action": "focus", "window": "slack"}); got != "focus 0xc" {
		t.Fatalf("named: %s", got)
	}
}

// Q9: an ambiguous name puts the candidates on the island, numbered, as a choice.
func TestAmbiguousNameListsChoicesOnTheIsland(t *testing.T) {
	var shown []string
	ws := []host.Window{{ID: "1", Title: "build", App: "Terminal"}, {ID: "2", Title: "logs", App: "Terminal"}}
	ts := New(host.New(&fakeBackend{caps: hypr, windows: ws}), OpenPolicy{}, catalogue)
	ts.Show = func(text string, _ time.Duration) { shown = append(shown, text) }
	out := call(t, ts, ToolWindow, map[string]any{"action": "focus", "window": "terminal"})
	if out[node.ChoiceKey] != true || out[node.IslandOnlyKey] != true || out["error_code"] != "ambiguous_window" {
		t.Fatalf("%v", out)
	}
	if text, _ := out[node.IslandKey].(string); !strings.HasPrefix(text, "Which one?  1. ") || !strings.Contains(text, "2. ") ||
		len(shown) != 1 || shown[0] != text {
		t.Fatalf("island %q, shown %q", text, shown)
	}
}

// Voice test, 2026-10-24: six terminals shared the title "alex@omarchy:~/jarvis". The list tells
// them apart by workspace, and "the second one" picks the second entry by its place, whatever
// the model passes as the window.
func TestTheSecondOneOfRepeatedTitles(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	ws := []host.Window{
		{ID: "0xa", Title: "alex@omarchy:~/jarvis", App: "foot", Workspace: "1", Focused: true},
		{ID: "0xb", Title: "alex@omarchy:~/jarvis", App: "foot", Workspace: "2"},
		{ID: "0xc", Title: "alex@omarchy:~/jarvis", App: "foot", Workspace: "2"},
	}
	b := &fakeBackend{caps: hypr, windows: ws}
	ts := refTools(b, c)
	out, _ := ts.Call(context.Background(), ToolWindow, map[string]any{"action": "focus", "window": "terminal"}, node.Request{VoiceCommand: "switch to the terminal"})
	text, _ := out[node.IslandKey].(string)
	for _, want := range []string{"1. foot — alex@omarchy:~/jarvis · workspace 1", "2. foot — alex@omarchy:~/jarvis · workspace 2 (1)", "3. foot — alex@omarchy:~/jarvis · workspace 2 (2)"} {
		if !strings.Contains(text, want) {
			t.Fatalf("island %q lacks %q", text, want)
		}
	}
	if n := out["numbered"].([]map[string]any); len(n) != 3 || n[1]["window_id"] != "0xb" {
		t.Fatalf("numbered %v", out["numbered"])
	}
	c.t = c.t.Add(10 * time.Second)
	// The model passes the repeated title back; what was said decides.
	b.actions = nil
	out, _ = ts.Call(context.Background(), ToolWindow, map[string]any{"action": "focus", "window": "alex@omarchy:~/jarvis"}, node.Request{VoiceCommand: "The second one."})
	if out["success"] != true || strings.Join(b.actions, ";") != "focus 0xb" {
		t.Fatalf("the second one: %v %v", out, b.actions)
	}
	for _, said := range []string{"number 3", "the third one", "3", "the last one"} {
		call(t, ts, ToolWindow, map[string]any{"action": "focus", "window": "terminal"}) // the list again
		b.actions = nil
		out, _ = ts.Call(context.Background(), ToolWindow, map[string]any{"action": "focus"}, node.Request{VoiceCommand: said})
		if out["success"] != true || strings.Join(b.actions, ";") != "focus 0xc" {
			t.Fatalf("%q: %v %v", said, out, b.actions)
		}
	}
	// A list gone stale is no longer what "the second one" means.
	call(t, ts, ToolWindow, map[string]any{"action": "focus", "window": "terminal"})
	c.t = c.t.Add(time.Minute)
	b.actions = nil
	out, _ = ts.Call(context.Background(), ToolWindow, map[string]any{"action": "focus", "window": "terminal"}, node.Request{VoiceCommand: "the second one"})
	if out[node.ChoiceKey] != true || len(b.actions) != 0 {
		t.Fatalf("stale list: %v %v", out, b.actions)
	}
}

// Voice test, 2026-10-24: "close all my Firefox windows" was done as a list and three single
// closes, with no question. The words say all: the first close asks, the others get the same
// question, and nothing closes before the yes.
func TestSayingAllClosesOnlyAfterTheYes(t *testing.T) {
	ws := []host.Window{{ID: "0xa", App: "firefox"}, {ID: "0xb", App: "firefox"}, {ID: "0xc", App: "foot", Focused: true}}
	b := &fakeBackend{caps: hypr, windows: ws}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	ts.Confirm = &dictate.Confirmer{Mode: dictate.ConfirmSubmit}
	said := node.Request{VoiceCommand: "Close all my Firefox windows."}
	first, _ := ts.Call(context.Background(), ToolClose, map[string]any{"window_id": "0xa"}, said)
	second, _ := ts.Call(context.Background(), ToolClose, map[string]any{"window_id": "0xb"}, said)
	if first["status"] != "awaiting_confirmation" || second["confirmation_id"] != first["confirmation_id"] || len(b.actions) != 0 {
		t.Fatalf("first %v second %v closed %v", first, second, b.actions)
	}
	if _, err := ts.Confirm.Answer(context.Background(), first["confirmation_id"].(string), "yes"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.actions, ";") != "close 0xa;close 0xb" {
		t.Fatalf("after yes %v", b.actions)
	}
	// Without "all" in the words, one window closes at once.
	b.actions = nil
	call(t, ts, ToolClose, map[string]any{"window_id": "0xc"})
	if strings.Join(b.actions, ";") != "close 0xc" {
		t.Fatalf("single %v", b.actions)
	}
}

func TestRestoreExampleWhereThereIsNoMinimise(t *testing.T) {
	spec, _ := windowSpec(map[string]bool{"windows.focus": true, "windows.unmaximize": true})
	found := false
	for _, e := range spec.Examples {
		if e.VoiceCommand == "restore firefox" && e.Params["action"] == "unmaximize" {
			found = true
		}
	}
	if !found {
		t.Fatal("no restore → unmaximize example on a host without minimise")
	}
	spec, _ = windowSpec(map[string]bool{"windows.focus": true, "windows.unmaximize": true, "windows.unminimize": true})
	for _, e := range spec.Examples {
		if e.VoiceCommand == "restore firefox" {
			t.Fatal("restore is ambiguous where minimise exists")
		}
	}
}

// The answer in the choice window is acted on by the node: the call that ended in the list
// runs again on the window at that place.
func TestAnswerChoiceRerunsTheCallOnTheChosenWindow(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	ws := []host.Window{{ID: "0xa", Title: "build", App: "foot", Focused: true}, {ID: "0xb", Title: "logs", App: "foot"}}
	b := &fakeBackend{caps: hypr, windows: ws}
	ts := refTools(b, c)
	call(t, ts, ToolWindow, map[string]any{"action": "maximize", "window": "the terminal"})
	if _, ok := ts.AnswerChoice(context.Background(), "Small product bottles."); ok {
		t.Fatal("garbage taken as an answer")
	}
	out, ok := ts.AnswerChoice(context.Background(), "The second one, please.")
	if !ok || out["success"] != true || strings.Join(b.actions, ";") != "maximize 0xb" || out[node.IslandOnlyKey] != true {
		t.Fatalf("%v %v %v", ok, out, b.actions)
	}
	if _, ok := ts.AnswerChoice(context.Background(), "the first one"); ok {
		t.Fatal("answered twice")
	}
	for said, want := range map[string]int{"number 2": 2, "two": 2, "one": 1, "number one": 1, "the last one": -1, "3": 3, "that one": 0} {
		if got := answerOrdinal(said); got != want {
			t.Errorf("answerOrdinal(%q) = %d, want %d", said, got, want)
		}
	}
}
