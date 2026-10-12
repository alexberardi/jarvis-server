package screen

import (
	"bytes"
	"context"
	"errors"
	"image"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestNormalizeQuery(t *testing.T) {
	for in, want := range map[string]string{
		"firefox": "firefox", "the Firefox window": "firefox", "  'Slack'  ": "slack", "my terminal": "terminal",
		"the terminal app": "terminal", "Firefox's window": "firefox", "window": "", "this window": "", "the focused window": "", "": "",
	} {
		if got := normalizeQuery(in); got != want {
			t.Errorf("normalizeQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPickWindow(t *testing.T) {
	cands := []candidate{
		{Apps: []string{"foot"}, Title: "build.log", Recency: 0, Visible: true},
		{Apps: []string{"firefox"}, Title: "Docs — Mozilla Firefox", Recency: 4, Visible: true},
		{Apps: []string{"org.mozilla.firefox"}, Title: "Mail — Mozilla Firefox", Recency: 2, Visible: true},
		{Apps: []string{"Slack"}, Title: "general", Recency: 1, Visible: false},
		{Apps: []string{"com.mitchellh.ghostty"}, Title: "vim notes.md", Recency: 3, Visible: true},
		{Apps: []string{"code-oss"}, Title: "screen.go — jarvis", Recency: 5, Visible: true},
	}
	for _, c := range []struct {
		query, want, code string
	}{
		{"firefox", "Mail — Mozilla Firefox", ""},            // two matches: the more recently focused
		{"the Firefox window", "Mail — Mozilla Firefox", ""}, // normalised
		{"FOOT", "build.log", ""},                            // case-insensitive
		{"ghostty", "vim notes.md", ""},                      // reverse-DNS app id
		{"the terminal", "build.log", ""},                    // a kind of app: the most recent terminal
		{"editor", "screen.go — jarvis", ""},
		{"notes.md", "vim notes.md", ""},            // title words
		{"jarvis screen", "screen.go — jarvis", ""}, // all title words, any order
		{"slack", "", CodeWindowNotVisible},
		{"spotify", "", CodeWindowNotFound},
	} {
		i, err := pickWindow(cands, c.query)
		if c.code != "" {
			var we *WindowError
			if !errors.As(err, &we) || we.Code != c.code {
				t.Errorf("%q: err %v, want %s", c.query, err, c.code)
			}
			continue
		}
		if err != nil || cands[i].Title != c.want {
			t.Errorf("%q: picked %d (%v), want %q", c.query, i, err, c.want)
		}
	}
	_, err := pickWindow(cands, "slack")
	if err == nil || err.Error() != "Slack is open but not visible — bring it to the front and ask again" {
		t.Errorf("not visible message: %v", err)
	}
	_, err = pickWindow(cands, "spotify")
	if err == nil || !strings.Contains(err.Error(), `"spotify"`) ||
		!strings.Contains(err.Error(), "Foot, Firefox, Ghostty, Code-oss") || strings.Contains(err.Error(), "Slack") {
		t.Errorf("not found message: %v", err)
	}
	// An app-name hit decides even when only a title would match a visible window.
	_, err = pickWindow([]candidate{
		{Apps: []string{"firefox"}, Title: "x", Visible: false},
		{Apps: []string{"foot"}, Title: "firefox build", Recency: 1, Visible: true},
	}, "firefox")
	if we := (*WindowError)(nil); !errors.As(err, &we) || we.Code != CodeWindowNotVisible {
		t.Errorf("app tier: %v", err)
	}
	// The not-found list stops at five names.
	var many []candidate
	for _, a := range []string{"a1", "b2", "c3", "d4", "e5", "f6", "g7"} {
		many = append(many, candidate{Apps: []string{a}, Visible: true})
	}
	if _, err := pickWindow(many, "zzz"); err == nil || strings.Count(err.Error(), ",") != 4 {
		t.Errorf("five names: %v", err)
	}
}

// hyprClients is `hyprctl -j clients` with a terminal focused (focusHistoryID 0), two Firefox
// windows (workspace 1, shown; workspace 3, on a disabled monitor, so not shown), Slack on
// workspace 2 (not shown) and a group's hidden tab.
const hyprClients = `[
 {"at":[9,33],"size":[531,1308],"title":"build.log","class":"foot","initialClass":"foot",
  "workspace":{"id":1,"name":"1"},"mapped":true,"hidden":false,"focusHistoryID":0,"fullscreen":0},
 {"at":[1211,33],"size":[1180,1308],"title":"Docs — Mozilla Firefox","class":"firefox","initialClass":"firefox",
  "workspace":{"id":1,"name":"1"},"mapped":true,"hidden":false,"focusHistoryID":4,"fullscreen":0},
 {"at":[0,0],"size":[2560,1440],"title":"Old — Mozilla Firefox","class":"firefox","initialClass":"firefox",
  "workspace":{"id":3,"name":"3"},"mapped":true,"hidden":false,"focusHistoryID":1,"fullscreen":0},
 {"at":[0,0],"size":[2560,1440],"title":"general","class":"Slack","initialClass":"Slack",
  "workspace":{"id":2,"name":"2"},"mapped":true,"hidden":false,"focusHistoryID":2,"fullscreen":0},
 {"at":[600,33],"size":[500,600],"title":"other tab","class":"thunderbird","initialClass":"thunderbird",
  "workspace":{"id":1,"name":"1"},"mapped":true,"hidden":true,"focusHistoryID":3,"fullscreen":0}
]`

const hyprMonitors = `[{"name":"HDMI-A-1","focused":true,"activeWorkspace":{"id":1,"name":"1"},"specialWorkspace":{"id":0,"name":""}},
 {"name":"DP-1","focused":false,"activeWorkspace":{"id":3,"name":"3"},"specialWorkspace":{"id":0,"name":""},"disabled":true}]`

func TestHyprlandNamedWindow(t *testing.T) {
	for _, c := range []struct {
		name, window, clients, monitors, grim, app, code string
	}{
		{name: "focused", window: "", grim: "-g 9,33 531x1308", app: "foot"},
		{name: "named, not focused", window: "firefox", grim: "-g 1211,33 1180x1308", app: "firefox"},
		{name: "the terminal", window: "the terminal", grim: "-g 9,33 531x1308", app: "foot"},
		{name: "hidden workspace", window: "slack", code: CodeWindowNotVisible},
		{name: "hidden group tab", window: "thunderbird", code: CodeWindowNotVisible},
		{name: "no match", window: "spotify", code: CodeWindowNotFound},
		// The more recently focused Firefox is on workspace 3; once a monitor shows it, it wins.
		{name: "focus history", window: "firefox", grim: "-g 0,0 2560x1440", app: "firefox",
			monitors: `[{"activeWorkspace":{"id":1}},{"activeWorkspace":{"id":3}}]`},
		// A shown special workspace covers its monitor's regular workspace.
		{name: "special workspace", window: "firefox", code: CodeWindowNotVisible,
			monitors: `[{"activeWorkspace":{"id":1},"specialWorkspace":{"id":-98}}]`},
		// Another window's fullscreen covers the rest of the workspace.
		{name: "under fullscreen", window: "firefox", code: CodeWindowNotVisible,
			clients: strings.Replace(hyprClients, `"focusHistoryID":0,"fullscreen":0`, `"focusHistoryID":0,"fullscreen":2`, 1)},
		// Older Hyprland: fullscreen is a bool.
		{name: "bool fullscreen", window: "foot", grim: "-g 9,33 531x1308", app: "foot",
			clients: strings.Replace(hyprClients, `"focusHistoryID":0,"fullscreen":0`, `"focusHistoryID":0,"fullscreen":true`, 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := hypr()
			f.out["hyprctl -j clients"] = []byte(hyprClients)
			if c.clients != "" {
				f.out["hyprctl -j clients"] = []byte(c.clients)
			}
			f.out["hyprctl -j monitors"] = []byte(hyprMonitors)
			if c.monitors != "" {
				f.out["hyprctl -j monitors"] = []byte(c.monitors)
			}
			f.out["hyprctl -j activewindow"] = []byte(`{"at":[9,33],"size":[531,1308],"title":"build.log","class":"foot"}`)
			shot, err := (&Wayland{X: f}).Capture(context.Background(), ScopeWindow, c.window)
			if c.code != "" {
				var we *WindowError
				if !errors.As(err, &we) || we.Code != c.code {
					t.Fatalf("err = %v, want %s", err, c.code)
				}
				for _, call := range f.calls {
					if call[0] == "grim" {
						t.Errorf("captured anyway: %v", call)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := lastGrim(t, f); g != "-t png -l 1 "+c.grim+" -" || shot.App != c.app {
				t.Errorf("grim %q app %q", g, shot.App)
			}
		})
	}
}

// swayTree: output eDP-1 shows workspace 1 (foot focused, Firefox in a tabbed container behind
// a visible tab), workspace 2 holds Slack; output HDMI-A-1 shows workspace 3 with an XWayland
// app. Focus lists order children most recent first.
const swayTree = `{"id":1,"type":"root","focus":[3,2],"nodes":[
 {"id":2,"type":"output","name":"eDP-1","current_workspace":"1","focus":[10,20],"nodes":[
  {"id":10,"type":"workspace","name":"1","focus":[11,12],"nodes":[
   {"id":11,"type":"con","name":"build.log","app_id":"foot","pid":5,"focused":true,"visible":true,
    "rect":{"x":10,"y":20,"width":300,"height":200}},
   {"id":12,"type":"con","name":"tabs","layout":"tabbed","focus":[14,13],"rect":{"x":310,"y":20,"width":600,"height":800},"nodes":[
    {"id":13,"type":"con","name":"Docs — Mozilla Firefox","app_id":"firefox","pid":6,"visible":false,
     "rect":{"x":310,"y":50,"width":600,"height":770}},
    {"id":14,"type":"con","name":"notes","app_id":"org.gnome.TextEditor","pid":7,"visible":true,
     "rect":{"x":310,"y":50,"width":600,"height":770}}]}]},
  {"id":20,"type":"workspace","name":"2","focus":[21],"nodes":[
   {"id":21,"type":"con","name":"general","app_id":"Slack","pid":8,"visible":false,
    "rect":{"x":0,"y":0,"width":1920,"height":1080}}]}]},
 {"id":3,"type":"output","name":"HDMI-A-1","current_workspace":"3","focus":[30],"nodes":[
  {"id":30,"type":"workspace","name":"3","focus":[32,31],"floating_nodes":[
   {"id":32,"type":"floating_con","name":"Mozilla Firefox","app_id":"firefox","pid":9,"visible":true,
    "rect":{"x":2000,"y":100,"width":800,"height":600}}],"nodes":[
   {"id":31,"type":"con","name":"spreadsheet","window_properties":{"class":"libreoffice-calc","instance":"libreoffice"},"pid":10,"visible":true,
    "rect":{"x":1920,"y":0,"width":1920,"height":1080}}]}]},
 {"id":4,"type":"output","name":"__i3","current_workspace":"__i3_scratch","nodes":[
  {"id":40,"type":"workspace","name":"__i3_scratch","nodes":[
   {"id":41,"type":"con","name":"scratch","app_id":"pavucontrol","pid":11,"rect":{"x":0,"y":0,"width":500,"height":500}}]}]}
]}`

func TestSwayNamedWindow(t *testing.T) {
	for _, c := range []struct {
		window, grim, app, code string
	}{
		{"", "-g 10,20 300x200", "foot", ""},
		{"firefox", "-g 2000,100 800x600", "firefox", ""}, // the hidden tab is skipped, the visible one on HDMI-A-1 wins
		{"text editor", "-g 310,50 600x770", "org.gnome.TextEditor", ""},
		{"libreoffice", "-g 1920,0 1920x1080", "libreoffice-calc", ""}, // XWayland class
		{"slack", "", "", CodeWindowNotVisible},
		{"pavucontrol", "", "", CodeWindowNotVisible}, // the scratchpad
		{"spotify", "", "", CodeWindowNotFound},
	} {
		f := &fakeExec{
			env:   map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"},
			paths: map[string]bool{"swaymsg": true, "grim": true},
			out:   map[string][]byte{"swaymsg -t get_tree": []byte(swayTree), "grim": pngOf(8, 8)},
		}
		shot, err := (&Wayland{X: f}).Capture(context.Background(), ScopeWindow, c.window)
		if c.code != "" {
			var we *WindowError
			if !errors.As(err, &we) || we.Code != c.code {
				t.Errorf("%q: err = %v, want %s", c.window, err, c.code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.window, err)
			continue
		}
		if g := lastGrim(t, f); g != "-t png -l 1 "+c.grim+" -" || shot.App != c.app {
			t.Errorf("%q: grim %q app %q", c.window, g, shot.App)
		}
	}
	// The more recently focused of two visible matches wins: make the floating Firefox's
	// output the older one and both Firefoxes visible.
	tree := strings.Replace(swayTree, `"focus":[3,2]`, `"focus":[2,3]`, 1)
	tree = strings.Replace(tree, `"focus":[14,13]`, `"focus":[13,14]`, 1)
	tree = strings.Replace(tree, `"app_id":"firefox","pid":6,"visible":false`, `"app_id":"firefox","pid":6,"visible":true`, 1)
	f := &fakeExec{
		env:   map[string]string{"WAYLAND_DISPLAY": "w", "SWAYSOCK": "/s"},
		paths: map[string]bool{"swaymsg": true, "grim": true},
		out:   map[string][]byte{"swaymsg -t get_tree": []byte(tree), "grim": pngOf(8, 8)},
	}
	if _, err := (&Wayland{X: f}).Capture(context.Background(), ScopeWindow, "firefox"); err != nil {
		t.Fatal(err)
	}
	if g := lastGrim(t, f); g != "-t png -l 1 -g 310,50 600x770 -" {
		t.Errorf("focus order: grim %q", g)
	}
}

func TestMacWindowChoice(t *testing.T) {
	ws := []MacWindow{
		{ID: 1, App: "Window Server", Layer: 25, W: 3000, H: 30, OnScreen: true}, // menu bar
		{ID: 2, App: "Control Center", Layer: 0, W: 30, H: 22, OnScreen: true},   // a status item
		{ID: 3, App: "Terminal", Title: "zsh", W: 800, H: 600, OnScreen: true},
		{ID: 4, App: "Firefox", Title: "Docs", W: 1200, H: 900, OnScreen: true}, // covered, still capturable
		{ID: 5, App: "Firefox", Title: "Old", W: 1200, H: 900, OnScreen: true},
		{ID: 6, App: "Slack", Title: "general", W: 1200, H: 900, OnScreen: false}, // minimised / another Space
	}
	if w, ok := frontOf(ws); !ok || w.ID != 3 {
		t.Errorf("front = %+v %v", w, ok)
	}
	for q, want := range map[string]uint32{"firefox": 4, "the terminal": 3, "docs": 4, "old": 5} {
		if w, err := namedOf(ws, q); err != nil || w.ID != want {
			t.Errorf("%q: %+v %v, want id %d", q, w, err, want)
		}
	}
	for q, code := range map[string]string{"slack": CodeWindowNotVisible, "control center": CodeWindowNotFound, "spotify": CodeWindowNotFound} {
		var we *WindowError
		if _, err := namedOf(ws, q); !errors.As(err, &we) || we.Code != code {
			t.Errorf("%q: %v, want %s", q, err, code)
		}
	}
	if _, ok := frontOf(ws[:2]); ok {
		t.Error("a front window among menu bars and status items")
	}
}

func TestMacCapturesTheNamedWindow(t *testing.T) {
	f := &fakeExec{out: map[string][]byte{screencapture: nil}, onRun: func(name string, args []string) error {
		return os.WriteFile(args[len(args)-1], pngOf(20, 10), 0o600)
	}}
	m := &Mac{X: f, Trusted: func() bool { return true }, Windows: func() ([]MacWindow, error) {
		return []MacWindow{{ID: 3, App: "Terminal", W: 800, H: 600, OnScreen: true},
			{ID: 4, App: "Firefox", Title: "Docs", W: 1200, H: 900, OnScreen: true},
			{ID: 6, App: "Slack", W: 1200, H: 900}}, nil
	}}
	shot, err := m.Capture(context.Background(), ScopeWindow, "firefox")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[0][1:len(f.calls[0])-1], " "); got != "-x -t png -o -l 4" || shot.App != "Firefox" {
		t.Errorf("args %q, app %q", got, shot.App)
	}
	var we *WindowError
	if _, err := m.Capture(context.Background(), ScopeScreen, "slack"); !errors.As(err, &we) || we.Code != CodeWindowNotVisible {
		t.Errorf("slack: %v", err)
	}
	if len(f.calls) != 1 {
		t.Errorf("captured for a window that isn't visible: %v", f.calls)
	}
}

func TestToolNamedWindowAndLog(t *testing.T) {
	var logs bytes.Buffer
	fc := &fakeCap{ok: true, shot: Shot{Image: image.NewRGBA(image.Rect(0, 0, 8, 8)), App: "firefox",
		Title: strings.Repeat("a very long private title ", 5)}}
	tl := &Tools{C: fc, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	out, _ := tl.Call(context.Background(), ToolRead, map[string]any{"scope": "screen", "window": " the Firefox window "}, node.Request{})
	if out["success"] != true || fc.window != "the Firefox window" || fc.scope != ScopeWindow || out["scope"] != ScopeWindow {
		t.Errorf("named call: %v, capture got %q %q", out["success"], fc.scope, fc.window)
	}
	line := logs.String()
	if !strings.Contains(line, "window=\"the Firefox window\"") || !strings.Contains(line, "app=firefox") ||
		strings.Contains(line, strings.Repeat("a very long private title ", 2)) || strings.Contains(line, "data") {
		t.Errorf("log line: %s", line)
	}
	// A blank or filler name is no name: the focused window.
	if _, _ = tl.Call(context.Background(), ToolRead, map[string]any{"window": "the window"}, node.Request{}); fc.window != "" {
		t.Errorf("filler name passed: %q", fc.window)
	}
	fc.err = &WindowError{Code: CodeWindowNotVisible, Msg: "Firefox is open but not visible — bring it to the front and ask again"}
	out, _ = tl.Call(context.Background(), ToolRead, map[string]any{"window": "firefox"}, node.Request{})
	if out["success"] != false || out["error_code"] != CodeWindowNotVisible || !strings.HasPrefix(out["error"].(string), "Firefox is open") {
		t.Errorf("not visible: %v", out)
	}
	p := spec().Render().ClientTool
	if !strings.Contains(p["function"].(map[string]any)["description"].(string), "firefox") {
		t.Error("description doesn't mention naming a window")
	}
}
