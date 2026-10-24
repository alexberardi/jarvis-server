package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

type fakeBackend struct {
	caps     []string
	volume   int
	opened   []string
	launched []string
	actions  []string
	windows  []host.Window // nil: one editor window
}

func (f *fakeBackend) Platform() string                           { return "linux" }
func (f *fakeBackend) Capabilities(context.Context) []string      { return f.caps }
func (f *fakeBackend) GetVolume(context.Context) (int, error)     { return f.volume, nil }
func (f *fakeBackend) ApplyVolume(_ context.Context, l int) error { f.volume = l; return nil }
func (f *fakeBackend) ListWindows(context.Context) ([]host.Window, error) {
	if f.windows != nil {
		return f.windows, nil
	}
	return []host.Window{{ID: "0x1", Title: "Doc", App: "editor"}}, nil
}
func (f *fakeBackend) MinimizeWindow(_ context.Context, id string) error { return f.act("min " + id) }
func (f *fakeBackend) FocusWindow(_ context.Context, id string) error    { return f.act("focus " + id) }
func (f *fakeBackend) CloseWindow(_ context.Context, id string) error    { return f.act("close " + id) }
func (f *fakeBackend) act(s string) error                                { f.actions = append(f.actions, s); return nil }
func (f *fakeBackend) ArrangeWindow(_ context.Context, id string, a host.WindowAction, target string) error {
	return f.act(strings.TrimSpace(string(a) + " " + id + " " + target))
}
func (f *fakeBackend) LaunchApp(_ context.Context, p string) error {
	f.launched = append(f.launched, p)
	return nil
}
func (f *fakeBackend) Open(_ context.Context, t string) error {
	f.opened = append(f.opened, t)
	return nil
}

var all = []string{"open", "volume.get", "volume.set", "windows.list", "windows.minimize", "windows.focus", "windows.close"}

func names(defs []node.ToolDef) string {
	var n []string
	for _, d := range defs {
		n = append(n, d.Name)
	}
	return strings.Join(n, ",")
}

func TestToolsFollowCapabilitiesPerCall(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	if got := len(ts.Available(context.Background())); got != 7 {
		t.Fatalf("full host offers %d tools", got)
	}
	b.caps = []string{"open", "volume.get", "volume.set"} // Wayland, or AX revoked
	if got := names(ts.Available(context.Background())); got != "desktop_open,desktop_list_apps,desktop_get_volume,desktop_set_volume" {
		t.Fatalf("offered %s", got)
	}
}

func TestRenderedSchemasSatisfyCC(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: all}), OpenPolicy{}, catalogue)
	for _, d := range ts.Available(context.Background()) {
		// CommandDefinition requires command_name, description (strings) and parameters (list).
		if _, ok := d.Command["command_name"].(string); !ok {
			t.Fatalf("%s: command_name", d.Name)
		}
		if _, ok := d.Command["description"].(string); !ok {
			t.Fatalf("%s: description", d.Name)
		}
		if _, ok := d.Command["parameters"].([]any); !ok {
			t.Fatalf("%s: parameters must be a list", d.Name)
		}
		fn := d.ClientTool["function"].(map[string]any)
		if fn["name"] != d.Name || strings.ContainsAny(d.Name, ". ") {
			t.Fatalf("bad function name %v", fn["name"])
		}
		if _, err := json.Marshal(d.ClientTool); err != nil {
			t.Fatal(err)
		}
	}
}

func call(t *testing.T, ts *Tools, name string, args map[string]any) map[string]any {
	t.Helper()
	out, ok := ts.Call(context.Background(), name, args, node.Request{})
	if !ok {
		t.Fatalf("%s not handled", name)
	}
	return out
}

func TestSetVolumeClampsAndAcceptsStrings(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	for _, tc := range []struct {
		in   any
		want int
	}{{150.0, 100}, {-3.0, 0}, {"30", 30}, {"45%", 45}, {1e300, 100}} {
		out := call(t, ts, ToolSetVolume, map[string]any{"volume": tc.in})
		if out["success"] != true || out["volume"] != tc.want || b.volume != tc.want {
			t.Fatalf("volume %v -> %v (backend %d)", tc.in, out, b.volume)
		}
	}
	out := call(t, ts, ToolSetVolume, map[string]any{"volume": "loud"})
	if out["success"] != false || out["error_code"] != "invalid_argument" {
		t.Fatalf("non-number: %v", out)
	}
}

func TestOpenPolicy(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	for _, bad := range []string{"/usr/bin/xterm", `C:\Windows\System32\cmd.exe`, "~/bin/x", "--help",
		"file:///etc/passwd", "steam://run/570", "https://", "javascript:alert(1)", ""} {
		out := call(t, ts, ToolOpen, map[string]any{"target": bad})
		if out["success"] != false || out["error_code"] != "invalid_argument" {
			t.Fatalf("%q allowed: %v", bad, out)
		}
	}
	if len(b.opened)+len(b.launched) != 0 {
		t.Fatalf("refused targets reached the OS: %v %v", b.opened, b.launched)
	}
	out := call(t, ts, ToolOpen, map[string]any{"target": "https://example.com/x?y=1"})
	if out["success"] != true || len(b.opened) != 1 {
		t.Fatalf("https refused: %v", out)
	}
	anyTs := New(host.New(b), OpenPolicy{AllowAny: true}, catalogue)
	if out := call(t, anyTs, ToolOpen, map[string]any{"target": "steam://open/games"}); out["success"] != true {
		t.Fatalf("AllowAny refused: %v", out)
	}
}

func catalogue() ([]apps.App, error) {
	return []apps.App{
		{Name: "Steam", Path: "/Applications/Steam.app"},
		{Name: "Microsoft Word", Path: "/Applications/Microsoft Word.app"},
		{Name: "Microsoft Excel", Path: "/Applications/Microsoft Excel.app"},
	}, nil
}

func TestOpenLaunchesInstalledAppsByName(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	out := call(t, ts, ToolOpen, map[string]any{"target": "steam"})
	if out["success"] != true || out["launched"] != "Steam" {
		t.Fatalf("steam: %v", out)
	}
	if strings.Join(b.launched, ";") != "/Applications/Steam.app" || len(b.opened) != 0 {
		t.Fatalf("launched %v opened %v", b.launched, b.opened)
	}
	out = call(t, ts, ToolOpen, map[string]any{"target": "microsoft"})
	if out["error_code"] != "ambiguous" || len(out["candidates"].([]string)) != 2 {
		t.Fatalf("ambiguous: %v", out)
	}
	out = call(t, ts, ToolOpen, map[string]any{"target": "Photoshop"})
	if out["error_code"] != "app_not_found" {
		t.Fatalf("not found: %v", out)
	}
	if len(b.launched) != 1 {
		t.Fatalf("a failed resolution launched something: %v", b.launched)
	}
	if out := call(t, New(host.New(b), OpenPolicy{}, nil), ToolOpen, map[string]any{"target": "steam"}); out["error_code"] != "app_not_found" {
		t.Fatalf("no catalogue: %v", out)
	}
}

func TestListApps(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: all}), OpenPolicy{}, catalogue)
	out := call(t, ts, ToolListApps, map[string]any{"query": "MICRO"})
	if out["success"] != true || strings.Join(out["apps"].([]string), ",") != "Microsoft Word,Microsoft Excel" || out["count"] != 2 {
		t.Fatalf("list: %v", out)
	}
	if out := call(t, ts, ToolListApps, nil); len(out["apps"].([]string)) != 3 {
		t.Fatalf("list all: %v", out)
	}
}

func TestUnsupportedIsAnOutputNotACrash(t *testing.T) {
	ts := New(host.New(&fakeBackend{caps: []string{"open"}}), OpenPolicy{}, catalogue)
	out := call(t, ts, ToolClose, map[string]any{"window_id": "0x1"})
	if out["success"] != false || out["error_code"] != "not_supported" {
		t.Fatalf("got %v", out)
	}
}

func TestWindowToolsPassIDsThrough(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	if out := call(t, ts, ToolClose, map[string]any{"window_id": "0x1"}); out["success"] != true {
		t.Fatal(out)
	}
	if out := call(t, ts, ToolWindow, map[string]any{"action": "focus"}); out["error_code"] != "invalid_argument" {
		t.Fatal(out)
	}
	if strings.Join(b.actions, ";") != "close 0x1" {
		t.Fatalf("actions %v", b.actions)
	}
	if _, ok := ts.Call(context.Background(), "nope", nil, node.Request{}); ok {
		t.Fatal("unknown tool handled")
	}
}

// The model often guesses an id from the app's name ("text-edit-window", "terminal-1", #419):
// a name resolves against the window list, so one turn does it.
func TestWindowToolsResolveNames(t *testing.T) {
	ws := []host.Window{
		{ID: "33410/1", Title: "alex — zsh", App: "Terminal"},
		{ID: "643/1", Title: "Untitled", App: "TextEdit"},
		{ID: "77/1", Title: "Inbox", App: "Mail", Minimized: true},
	}
	for _, c := range []struct{ tool, id, want string }{
		{"focus", "text-edit-window", "focus 643/1"},
		{"focus", "TextEdit", "focus 643/1"},
		{"focus", "the TextEdit window", "focus 643/1"},
		{ToolClose, "terminal-1", "close 33410/1"},
		{ToolClose, "the terminal", "close 33410/1"},
		{"minimize", "zsh", "min 33410/1"},
		{"focus", "mail", "focus 77/1"},
		{"focus", "643/1", "focus 643/1"},
	} {
		b := &fakeBackend{caps: all, windows: ws}
		tool, args := ToolWindow, map[string]any{"window": c.id, "action": c.tool}
		if c.tool == ToolClose {
			tool, args = ToolClose, map[string]any{"window_id": c.id}
		}
		out := call(t, New(host.New(b), OpenPolicy{}, catalogue), tool, args)
		if out["success"] != true || strings.Join(b.actions, ";") != c.want {
			t.Errorf("%s %q: got %v, actions %v; want %s", c.tool, c.id, out, b.actions, c.want)
		}
		if c.id != "643/1" && out["matched_window"] == nil {
			t.Errorf("%s %q: no matched_window in %v", c.tool, c.id, out)
		}
	}
}

func TestWindowToolsAmbiguousOrUnknownNameListsWindows(t *testing.T) {
	ws := []host.Window{
		{ID: "1/1", Title: "build", App: "Terminal"},
		{ID: "1/2", Title: "logs", App: "Terminal"},
		{ID: "2/1", Title: "Untitled", App: "TextEdit"},
	}
	b := &fakeBackend{caps: all, windows: ws}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	out := call(t, ts, ToolClose, map[string]any{"window_id": "terminal"})
	if out["success"] != false || out["error_code"] != "ambiguous_window" || len(out["candidates"].([]string)) != 2 {
		t.Fatalf("ambiguous: %v", out)
	}
	out = call(t, ts, ToolWindow, map[string]any{"window": "spotify-window", "action": "focus"})
	if out["success"] != false || out["error_code"] != "window_not_found" {
		t.Fatalf("unknown: %v", out)
	}
	if got, _ := out["windows"].([]host.Window); len(got) != 3 {
		t.Fatalf("not found should list the windows to retry with: %v", out)
	}
	if len(b.actions) != 0 {
		t.Fatalf("acted on a guess: %v", b.actions)
	}
}

// A computer task's app is found by name only: never a link, a path or a URI, even with
// --allow-open-any.
func TestFindAppNamesOnly(t *testing.T) {
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{AllowAny: true}, catalogue)
	for _, target := range []string{"https://store.steampowered.com", "mailto:a@example.com", "/Applications/Steam.app", "steam://run/1", "-a Steam"} {
		if _, err := ts.FindApp(target); !errors.Is(err, host.ErrInvalidTarget) {
			t.Errorf("FindApp(%q) = %v, want invalid target", target, err)
		}
	}
	a, err := ts.FindApp("stem")
	if err != nil || a.Path != "/Applications/Steam.app" {
		t.Fatalf("FindApp(stem) = %v, %v", a, err)
	}
	if err := ts.LaunchApp(context.Background(), a); err != nil || len(b.launched) != 1 || b.launched[0] != a.Path || len(b.opened) != 0 {
		t.Errorf("LaunchApp = %v, launched %v opened %v", err, b.launched, b.opened)
	}
}
