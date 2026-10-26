package desktop

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// hypr is a Hyprland-like host: no minimise, every arranging action but unminimize, one monitor.
var hypr = []string{"open", "windows.list", "windows.focus", "windows.close", "windows.maximize",
	"windows.unmaximize", "windows.fullscreen", "windows.snap", "windows.workspace"}

func windowEnum(t *testing.T, ts *Tools) []string {
	t.Helper()
	for _, d := range ts.Available(context.Background()) {
		if d.Name != ToolWindow {
			continue
		}
		props := d.ClientTool["function"].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
		return props["action"].(map[string]any)["enum"].([]string)
	}
	return nil
}

// The action list is the host's, computed per call, plus "unsupported"; minimise never appears
// where there is none, and the tool goes away with the last window capability.
func TestWindowActionsFollowCapabilities(t *testing.T) {
	b := &fakeBackend{caps: hypr}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	want := []string{"focus", "maximize", "unmaximize", "fullscreen", "snap_left", "snap_right", "move_to_workspace", "unsupported"}
	if got := windowEnum(t, ts); !reflect.DeepEqual(got, want) {
		t.Fatalf("enum = %v, want %v", got, want)
	}
	b.caps = append(b.caps, "windows.monitor")
	if got := windowEnum(t, ts); !strings.Contains(strings.Join(got, ","), "move_to_monitor") {
		t.Fatalf("second monitor: enum = %v", got)
	}
	b.caps = []string{"open", "windows.list", "windows.close"}
	if got := windowEnum(t, ts); got != nil {
		t.Fatalf("offered with no window action: %v", got)
	}
}

// Only the examples whose action this host has are offered.
func TestWindowExamplesMatchTheEnum(t *testing.T) {
	spec, _ := windowSpec(map[string]bool{"windows.focus": true, "windows.workspace": true})
	for _, e := range spec.Examples {
		if a := e.Params["action"]; a != "focus" && a != "move_to_workspace" {
			t.Fatalf("example for %v offered", a)
		}
	}
}

func TestUnsupportedActionGetsTheFixedMessage(t *testing.T) {
	for _, a := range []string{"unsupported", "minimize", "keep_on_top", "close"} {
		b := &fakeBackend{caps: hypr}
		out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, map[string]any{"action": a, "window": "Doc"})
		if out["success"] != false || out["error_code"] != "not_supported" || !strings.Contains(out["error"].(string), UnsupportedMessage) {
			t.Fatalf("%s: %v", a, out)
		}
		if len(b.actions) != 0 {
			t.Fatalf("%s acted: %v", a, b.actions)
		}
	}
}

func TestNoWindowMeansTheFocusedOne(t *testing.T) {
	ws := []host.Window{{ID: "0xa", Title: "~", App: "foot"}, {ID: "0xb", Title: "Mozilla Firefox", App: "firefox", Focused: true}}
	for _, name := range []string{"", "this", "this window", "current", "the window in front"} {
		b := &fakeBackend{caps: hypr, windows: ws}
		out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, map[string]any{"action": "maximize", "window": name})
		if out["success"] != true || strings.Join(b.actions, ";") != "maximize 0xb" || out["matched_window"] != "firefox — Mozilla Firefox" {
			t.Fatalf("%q: %v %v", name, out, b.actions)
		}
	}
	b := &fakeBackend{caps: hypr, windows: []host.Window{{ID: "0xa", App: "foot"}}}
	out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, map[string]any{"action": "maximize"})
	if out["error_code"] != "invalid_argument" || len(b.actions) != 0 {
		t.Fatalf("nothing focused: %v %v", out, b.actions)
	}
}

func TestMovesCarryTheirTarget(t *testing.T) {
	ws := []host.Window{{ID: "0xb", Title: "general", App: "Slack", Focused: true}}
	caps := append(append([]string{}, hypr...), "windows.monitor")
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "move_to_workspace", "window": "slack", "workspace": "2"}, "move_to_workspace 0xb 2"},
		{map[string]any{"action": "move_to_workspace", "workspace": 3.0}, "move_to_workspace 0xb 3"},
		{map[string]any{"action": "move_to_monitor", "monitor": "Next"}, "move_to_monitor 0xb next"},
		{map[string]any{"action": "move_to_monitor"}, "move_to_monitor 0xb next"},
	} {
		b := &fakeBackend{caps: caps, windows: ws}
		out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, c.args)
		if out["success"] != true || strings.Join(b.actions, ";") != c.want {
			t.Fatalf("%v: %v %v", c.args, out, b.actions)
		}
	}
	b := &fakeBackend{caps: caps, windows: ws}
	out := call(t, New(host.New(b), OpenPolicy{}, catalogue), ToolWindow, map[string]any{"action": "move_to_workspace"})
	if out["error_code"] != "invalid_argument" || len(b.actions) != 0 {
		t.Fatalf("no workspace: %v %v", out, b.actions)
	}
}
