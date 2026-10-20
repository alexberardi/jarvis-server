package computer

import (
	"context"
	"image"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeOS is the host desktop_open runs on: it records what is opened and launched (the rest of
// host.Backend is unused).
type fakeOS struct {
	host.Backend
	opened, launched []string
	onLaunch         func(path string)
}

func (f *fakeOS) Platform() string                      { return "darwin" }
func (f *fakeOS) Capabilities(context.Context) []string { return []string{host.CapOpen} }
func (f *fakeOS) Open(_ context.Context, t string) error {
	f.opened = append(f.opened, t)
	return nil
}
func (f *fakeOS) LaunchApp(_ context.Context, p string) error {
	f.launched = append(f.launched, p)
	if f.onLaunch != nil {
		f.onLaunch(p)
	}
	return nil
}

// catalogue is the installed apps desktop_open can launch.
var catalogue = []apps.App{
	{Name: "Notes", Path: "/System/Applications/Notes.app", Kind: apps.MacApp},
	{Name: "Terminal", Path: "/System/Applications/Utilities/Terminal.app", Kind: apps.MacApp},
	{Name: "Safari", Path: "/Applications/Safari.app", Kind: apps.MacApp},
}

// fakeAX is the accessibility tree: one snapshot, the element at a point by the window under
// it, and the focused element.
type fakeAX struct {
	snap    computeruse.Snapshot
	pids    []int    // Snapshot's pids
	acts    []string // "op ref value"
	focused *ax.Recorded
	at      func(x, y float64) *ax.Recorded
}

func (f *fakeAX) Snapshot(_ context.Context, pid, _ int) (computeruse.Snapshot, error) {
	f.pids = append(f.pids, pid)
	return f.snap, nil
}

func (f *fakeAX) act(gen int, op, ref, value string) error {
	if gen != f.snap.Gen {
		return &ax.Error{Code: ax.CodeStaleRef, Msg: "stale"}
	}
	f.acts = append(f.acts, strings.TrimSpace(op+" "+ref+" "+value))
	return nil
}

func (f *fakeAX) Press(gen int, ref string) error          { return f.act(gen, "press", ref, "") }
func (f *fakeAX) SetValue(gen int, ref, text string) error { return f.act(gen, "set_value", ref, text) }
func (f *fakeAX) Focus(gen int, ref string) error          { return f.act(gen, "focus", ref, "") }
func (f *fakeAX) Focused() (*ax.Recorded, error)           { return f.focused, nil }
func (f *fakeAX) At(x, y float64) (*ax.Recorded, error) {
	if f.at == nil {
		return nil, nil
	}
	return f.at(x, y), nil
}

// fakeWindows is the window list, front to back.
type fakeWindows struct{ ws []DesktopWindow }

func (f *fakeWindows) List(context.Context) ([]DesktopWindow, error) { return f.ws, nil }

// logInput is fakeInput that also logs what it posted.
type logInput struct {
	*fakeInput
	mu  sync.Mutex
	log []string
}

func (l *logInput) note(s string) {
	l.mu.Lock()
	l.log = append(l.log, s)
	l.mu.Unlock()
}

func (l *logInput) Click(ctx context.Context, x, y int, b string, n int) error {
	l.note("click " + itoa(x) + "," + itoa(y) + " " + b + " " + itoa(n))
	return l.fakeInput.Click(ctx, x, y, b, n)
}

func (l *logInput) Keys(ctx context.Context, chord []string) error {
	l.note("keys " + strings.Join(chord, "+"))
	return l.fakeInput.Keys(ctx, chord)
}

func (l *logInput) Type(ctx context.Context, text string) error {
	l.note("type " + text)
	return l.fakeInput.Type(ctx, text)
}

func (l *logInput) posted() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.log...)
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

type fakeClip struct{ text string }

func (c *fakeClip) Read(context.Context) (string, error)       { return c.text, nil }
func (c *fakeClip) Write(_ context.Context, text string) error { c.text = text; return nil }

// The desktop: Notes at 100,200 (400×300 points), Finder, Safari and Terminal beside it, and
// nothing else.
var (
	dNotes    = DesktopWindow{ID: "11", App: "Notes", PID: 501, Title: "Groceries", X: 100, Y: 200, W: 400, H: 300}
	dFinder   = DesktopWindow{ID: "12", App: "Finder", PID: 502, Title: "Downloads", X: 600, Y: 200, W: 300, H: 300}
	dSafari   = DesktopWindow{ID: "13", App: "Safari", PID: 503, Title: "Shop", X: 1000, Y: 200, W: 300, H: 300}
	dTerminal = DesktopWindow{ID: "14", App: "Terminal", PID: 504, Title: "zsh", X: 100, Y: 600, W: 400, H: 200}
)

type desktopFixture struct {
	tl    *Tools
	ax    *fakeAX
	wins  *fakeWindows
	in    *logInput
	clock *fakeClock
	mon   *fakeMonitor
	clip  *fakeClip
	shots []DesktopWindow
	lock  bool
	os    *fakeOS
}

func newDesktop(t *testing.T) *desktopFixture {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	f := &desktopFixture{
		ax: &fakeAX{snap: computeruse.Snapshot{Gen: 4, Title: "Groceries", Elements: []computeruse.SnapElement{
			{Ref: "a1", Role: "AXButton", Name: "New Note"},
			{Ref: "a2", Role: "AXTextArea", Name: "Body"},
			{Ref: "a3", Role: "AXSecureTextField", Name: "Password"},
		}}},
		wins:  &fakeWindows{ws: []DesktopWindow{dNotes, dFinder, dSafari, dTerminal}},
		in:    &logInput{fakeInput: &fakeInput{clock: clock}},
		clock: clock,
		mon:   &fakeMonitor{},
		clip:  &fakeClip{},
	}
	f.mon.set(clock.Now().Add(-time.Minute)) // the user was there a minute ago
	// The element under a point is the window's app's, like the AX hit test.
	f.ax.at = func(x, y float64) *ax.Recorded {
		for _, w := range f.wins.ws {
			if w.contains(int(x), int(y)) {
				return &ax.Recorded{Role: "AXGroup", PID: w.PID, App: "com.example." + strings.ToLower(w.App)}
			}
		}
		return nil
	}
	f.ax.focused = &ax.Recorded{Role: "AXTextArea", Name: "Body", PID: dNotes.PID, App: "com.apple.Notes"}
	d := &Desktop{
		AX: f.ax, Windows: f.wins, Input: f.in, Monitor: f.mon, Clipboard: f.clip, Now: clock.Now,
		Capture: func(_ context.Context, w DesktopWindow) (screen.Shot, error) {
			f.shots = append(f.shots, w)
			// A Retina window: 2 pixels per point.
			g := computeruse.Geometry{OriginX: w.X, OriginY: w.Y, LogicalW: w.W, LogicalH: w.H, Scale: 2}
			return screen.Shot{Image: image.NewRGBA(image.Rect(0, 0, 2*w.W, 2*w.H)), App: w.App, Title: w.Title, Geometry: g}, nil
		},
		Locked: func(context.Context) (bool, error) { return f.lock, nil },
	}
	// desktop_open's own catalogue and policy, even with --allow-open-any.
	f.os = &fakeOS{}
	d.Apps = desktop.New(host.New(f.os), desktop.OpenPolicy{AllowAny: true}, func() ([]apps.App, error) { return catalogue, nil })
	f.tl = &Tools{Switch: NewSwitch(true, nil), Desktop: d, Activity: &Activity{}, Home: t.TempDir()}
	t.Cleanup(f.tl.closeDesktop)
	return f
}

// screenCapture gives run cr_1 a capture of the whole 1600×1000 screen, "cap_screen".
func screenCapture(f *desktopFixture) {
	f.tl.runs().With("cr_1", func(r *Run) {
		r.keepCapture("cap_screen", computeruse.Geometry{LogicalW: 1600, LogicalH: 1000, ImageW: 1600, ImageH: 1000, Scale: 1})
	})
}

func desktopTarget(app string) map[string]any { return map[string]any{"kind": "desktop", "app": app} }

// observe looks at app's window, with a screenshot, and returns the capture id.
func (f *desktopFixture) observe(t *testing.T, app string) (map[string]any, string) {
	t.Helper()
	out := call(t, f.tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": desktopTarget(app),
		"snapshot": true, "screenshot": true})
	if out["success"] != true {
		return out, ""
	}
	capture, _ := out["capture"].(map[string]any)
	id, _ := capture["capture_id"].(string)
	return out, id
}

func (f *desktopFixture) act(t *testing.T, app string, actions ...map[string]any) map[string]any {
	t.Helper()
	return call(t, f.tl, computeruse.ToolAct, map[string]any{"run_id": "cr_1", "target": desktopTarget(app),
		"actions": actions, "blocked": map[string]any{"apps": []string{}}})
}

func click(capture string, x, y int) map[string]any {
	return map[string]any{"kind": "click", "intent": "click there", "x": x, "y": y, "capture_id": capture}
}

func firstResult(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	rs, _ := out["results"].([]map[string]any)
	if len(rs) == 0 {
		t.Fatalf("no results: %v", out)
	}
	return rs[0]
}

func results(out map[string]any) []map[string]any {
	rs, _ := out["results"].([]map[string]any)
	return rs
}

func TestDesktopMapsCoordinates(t *testing.T) {
	f := newDesktop(t)
	out, capID := f.observe(t, "Notes")
	if out["success"] != true || capID == "" {
		t.Fatalf("observe = %v", out)
	}
	if out["app"] != "Notes" || out["title"] != "Groceries" || out["target"] != TargetDesktop {
		t.Errorf("observe = %v, want Notes' window", out)
	}
	if len(f.ax.pids) != 1 || f.ax.pids[0] != dNotes.PID {
		t.Errorf("snapshot of pids %v, want Notes' %d", f.ax.pids, dNotes.PID)
	}
	snap, _ := out["snapshot"].(map[string]any)
	if snap["gen"] != 4 {
		t.Errorf("snapshot = %v", snap)
	}
	capture := out["capture"].(map[string]any)
	if capture["width"] != 800 || capture["height"] != 600 || capture["origin_x"] != 100 || capture["scale"] != 2.0 {
		t.Errorf("capture = %v, want 800×600 of the window at 100,200", capture)
	}
	// The image's centre is the window's centre on the screen: (100+200, 200+150).
	res := f.act(t, "Notes", click(capID, 400, 300),
		map[string]any{"kind": "double_click", "intent": "select a word", "x": 0, "y": 0, "capture_id": capID},
		map[string]any{"kind": "press", "intent": "new note", "ref": "a1"},
		map[string]any{"kind": "set_value", "intent": "write the list", "ref": "a2", "text": "milk"},
	)
	if res["success"] != true {
		t.Fatalf("act = %v", res)
	}
	want := []string{"click 300,350 left 1", "click 100,200 left 2"}
	if got := f.in.posted(); strings.Join(got, ";") != strings.Join(want, ";") {
		t.Errorf("posted %v, want %v", got, want)
	}
	if got := strings.Join(f.ax.acts, ";"); got != "press a1;set_value a2 milk" {
		t.Errorf("AX acts %q", got)
	}
	// Without a capture id the run's last capture is the one meant; with an unknown one, nothing.
	res = f.act(t, "Notes", click("cap_nope", 1, 1))
	if r := firstResult(t, res); r["code"] != "invalid_action" {
		t.Errorf("unknown capture = %v, want invalid_action", r)
	}
	if k, text := f.tl.Activity.Status(); k != Controlling || text == "" {
		t.Errorf("activity = %v %q, want controlling with the intent", k, text)
	}
}

func TestDesktopLaunchesApp(t *testing.T) {
	f := newDesktop(t)
	f.wins.ws = []DesktopWindow{dFinder}
	f.os.onLaunch = func(string) { f.wins.ws = append([]DesktopWindow{dNotes}, f.wins.ws...) }
	// A near name opens the catalogue's app and waits for its window.
	out, _ := f.observe(t, "Note")
	if out["success"] != true || out["app"] != "Notes" {
		t.Fatalf("observe = %v", out)
	}
	if len(f.os.launched) != 1 || f.os.launched[0] != "/System/Applications/Notes.app" {
		t.Errorf("launched %v, want Notes from the catalogue", f.os.launched)
	}
}

// What a desktop task opens is an installed app by name: never a link, a path or a URI, and a
// near name of a denied app is that app.
func TestDesktopLaunchRefusesLinks(t *testing.T) {
	for _, c := range []struct{ app, code string }{
		{"https://blocked.example/x", "invalid_argument"},
		{"mailto:someone@example.com", "invalid_argument"},
		{"/System/Applications/Utilities/Terminal.app", "invalid_argument"},
		{"x-apple.systempreferences:com.apple.preference.security", "invalid_argument"},
		{"Terminl", "denied"},
		{"Safar", "browser_window"},
	} {
		f := newDesktop(t)
		f.wins.ws = []DesktopWindow{dFinder}
		out, _ := f.observe(t, c.app)
		if out["error_code"] != c.code {
			t.Errorf("observe %q = %v, want %s", c.app, out, c.code)
		}
		if len(f.os.opened)+len(f.os.launched) != 0 {
			t.Errorf("observe %q opened %v launched %v, want nothing", c.app, f.os.opened, f.os.launched)
		}
	}
}

func TestTerminalDenied(t *testing.T) {
	f := newDesktop(t)
	if out, _ := f.observe(t, "Terminal"); out["error_code"] != "denied" {
		t.Errorf("observe Terminal = %v, want denied", out)
	}
	f.observe(t, "Notes")
	// A click through a capture of the whole screen that lands in the terminal below Notes.
	screenCapture(f)
	res := f.act(t, "Notes", click("cap_screen", 300, 700))
	if r := firstResult(t, res); r["code"] != "denied" || !strings.Contains(r["error"].(string), "terminal") {
		t.Errorf("click on Terminal = %v, want denied", r)
	}
	if got := f.in.posted(); len(got) != 0 {
		t.Errorf("posted %v, want nothing", got)
	}
	// Typing while the terminal has the focus.
	f.ax.focused = &ax.Recorded{Role: "AXTextArea", PID: dTerminal.PID, App: "com.apple.Terminal"}
	res = f.act(t, "Notes", map[string]any{"kind": "type", "intent": "x", "text": "rm -rf ~"})
	if r := firstResult(t, res); r["code"] != "denied" {
		t.Errorf("type into Terminal = %v, want denied", r)
	}
	if got := f.in.posted(); len(got) != 0 {
		t.Errorf("posted %v, want nothing", got)
	}
}

func TestBrowserWindowRefused(t *testing.T) {
	f := newDesktop(t)
	if out, _ := f.observe(t, "Safari"); out["error_code"] != "browser_window" {
		t.Errorf("observe Safari = %v, want browser_window", out)
	}
	f.observe(t, "Notes")
	screenCapture(f)
	res := f.act(t, "Notes", click("cap_screen", 1100, 300))
	if r := firstResult(t, res); r["code"] != "browser_window" {
		t.Errorf("click on Safari = %v, want browser_window", r)
	}
	res = f.act(t, "Notes", map[string]any{"kind": "navigate", "intent": "open the shop", "url": "https://shop.example.com"})
	if r := firstResult(t, res); r["code"] != "browser_window" {
		t.Errorf("navigate on a desktop task = %v, want browser_window", r)
	}
	if got := f.in.posted(); len(got) != 0 {
		t.Errorf("posted %v, want nothing", got)
	}
}

func TestPasswordPromptDenied(t *testing.T) {
	f := newDesktop(t)
	_, capID := f.observe(t, "Notes")
	// The OS asks for the password over Notes: its window is in front, at a higher layer.
	prompt := DesktopWindow{ID: "20", App: "SecurityAgent", PID: 90, X: 150, Y: 250, W: 300, H: 200, Layer: 1000}
	f.wins.ws = append([]DesktopWindow{prompt}, f.wins.ws...)
	f.ax.at = func(float64, float64) *ax.Recorded {
		return &ax.Recorded{Role: "AXSecureTextField", Secure: true, PID: 90, App: "com.apple.SecurityAgent"}
	}
	res := f.act(t, "Notes", click(capID, 400, 300))
	if r := firstResult(t, res); r["code"] != "denied" || !strings.Contains(r["error"].(string), "authentication") {
		t.Errorf("click on the password prompt = %v, want denied", r)
	}
	// A prompt the accessibility hit test can't see: its window alone refuses the point.
	f.ax.at = func(float64, float64) *ax.Recorded { return nil }
	res = f.act(t, "Notes", click(capID, 400, 300))
	if r := firstResult(t, res); r["code"] != "denied" || !strings.Contains(r["error"].(string), "authentication") {
		t.Errorf("click on a password prompt AX can't see = %v, want denied", r)
	}
	f.ax.focused = &ax.Recorded{Role: "AXSecureTextField", Secure: true, PID: 90, App: "com.apple.SecurityAgent"}
	res = f.act(t, "Notes", map[string]any{"kind": "type", "intent": "x", "text": "hunter2"})
	if r := firstResult(t, res); r["code"] != "denied" {
		t.Errorf("type into the password prompt = %v, want denied", r)
	}
	// A password field in an ordinary app: never typed into.
	f.wins.ws = f.wins.ws[1:]
	f.ax.focused = &ax.Recorded{Role: "AXSecureTextField", Secure: true, PID: dNotes.PID, App: "com.apple.Notes"}
	res = f.act(t, "Notes", map[string]any{"kind": "type", "intent": "x", "text": "hunter2"})
	if r := firstResult(t, res); r["code"] != "password_field" {
		t.Errorf("type into a password field = %v, want password_field", r)
	}
	res = f.act(t, "Notes", map[string]any{"kind": "set_value", "intent": "x", "ref": "a3", "text": "hunter2"})
	if r := firstResult(t, res); r["code"] != "password_field" {
		t.Errorf("set_value on a secure field = %v, want password_field", r)
	}
	if got := f.in.posted(); len(got) != 0 {
		t.Errorf("posted %v, want nothing", got)
	}
}

func TestBlockedApp(t *testing.T) {
	f := newDesktop(t)
	f.observe(t, "Notes")
	screenCapture(f)
	res := call(t, f.tl, computeruse.ToolAct, map[string]any{"run_id": "cr_1", "target": desktopTarget("Notes"),
		"actions": []map[string]any{click("cap_screen", 700, 300)}, "blocked": map[string]any{"apps": []string{"finder"}}})
	if r := firstResult(t, res); r["code"] != "blocked_app" {
		t.Errorf("click on a blocked app = %v, want blocked_app", r)
	}
}

func TestNewAppReported(t *testing.T) {
	f := newDesktop(t)
	_, capID := f.observe(t, "Notes")
	screenCapture(f)
	res := f.act(t, "Notes", click(capID, 10, 10), click("cap_screen", 700, 300), click("cap_screen", 710, 310))
	rs := results(res)
	if len(rs) != 3 || res["success"] != true {
		t.Fatalf("act = %v", res)
	}
	if rs[0]["new_app"] != nil {
		t.Errorf("Notes, the task's app, reported new: %v", rs[0])
	}
	if rs[1]["new_app"] != "Finder" {
		t.Errorf("first click in Finder = %v, want new_app Finder", rs[1])
	}
	if rs[2]["new_app"] != nil {
		t.Errorf("second click in Finder reported new again: %v", rs[2])
	}
	if apps, _ := res["new_apps"].([]string); len(apps) != 1 || apps[0] != "Finder" {
		t.Errorf("new_apps = %v, want [Finder]", res["new_apps"])
	}
}

func TestClipboardOnlyInTasks(t *testing.T) {
	f := newDesktop(t)
	f.observe(t, "Notes")
	res := f.act(t, "Notes",
		map[string]any{"kind": "clipboard_write", "intent": "copy the list", "text": "milk, eggs"},
		map[string]any{"kind": "keypress", "intent": "paste", "keys": []string{"cmd", "v"}},
		map[string]any{"kind": "clipboard_read", "intent": "read it back"},
	)
	if res["success"] != true {
		t.Fatalf("act = %v", res)
	}
	if f.clip.text != "milk, eggs" {
		t.Errorf("clipboard = %q", f.clip.text)
	}
	if rs := results(res); rs[2]["text"] != "milk, eggs" {
		t.Errorf("clipboard_read = %v, want the text", rs[2])
	}
	if got := f.in.posted(); len(got) != 1 || got[0] != "keys cmd+v" {
		t.Errorf("posted %v, want the paste chord", got)
	}
	// Not in a browser task (the extension's page, C3), and no tool of the agent's reads or
	// writes it outside computer_act.
	b := &fakeBrowser{}
	bt := &Tools{Browser: b, Switch: NewSwitch(true, nil), Desktop: f.tl.Desktop, Home: t.TempDir()}
	call(t, bt, computeruse.ToolObserve, map[string]any{"run_id": "cr_2", "target": browserTarget, "url": store})
	out := call(t, bt, computeruse.ToolAct, map[string]any{"run_id": "cr_2", "target": browserTarget,
		"actions": []map[string]any{{"kind": "clipboard_read", "intent": "x"}}})
	if r := firstResult(t, out); r["ok"] == true {
		t.Errorf("clipboard in a browser task = %v, want refused", r)
	}
	for _, d := range f.tl.Available(context.Background()) {
		if strings.Contains(d.Name, "clipboard") {
			t.Errorf("tool %s offers the clipboard outside computer_act", d.Name)
		}
	}
}

func TestNotAttendedRefused(t *testing.T) {
	f := newDesktop(t)
	f.lock = true
	if out, _ := f.observe(t, "Notes"); out["error_code"] != "not_attended" {
		t.Errorf("locked screen: observe = %v, want not_attended", out)
	}
	f.lock = false
	f.mon.set(f.clock.Now().Add(-6 * time.Minute))
	if out, _ := f.observe(t, "Notes"); out["error_code"] != "not_attended" {
		t.Errorf("idle 6 min: observe = %v, want not_attended", out)
	}
	res := f.act(t, "Notes", map[string]any{"kind": "keypress", "intent": "x", "keys": []string{"enter"}})
	if res["error_code"] != "not_attended" {
		t.Errorf("idle 6 min: act = %v, want not_attended", res)
	}
	if got := f.in.posted(); len(got) != 0 {
		t.Errorf("posted %v, want nothing", got)
	}
	if k, _ := f.tl.Activity.Status(); k != Idle {
		t.Errorf("activity = %v, want idle: nothing started", k)
	}
	// The user is back: the task starts, and the guard watches from now on.
	f.mon.set(f.clock.Now().Add(-10 * time.Second))
	if out, _ := f.observe(t, "Notes"); out["success"] != true {
		t.Fatalf("attended: observe = %v", out)
	}
	if k, _ := f.tl.Activity.Status(); k != Controlling {
		t.Errorf("activity = %v, want controlling", k)
	}
	// A run going on isn't refused for the user's idle time: they're watching it work.
	f.clock.Advance(10 * time.Minute)
	f.mon.set(f.clock.Now().Add(-10 * time.Minute))
	res = f.act(t, "Notes", map[string]any{"kind": "keypress", "intent": "x", "keys": []string{"enter"}})
	if res["success"] != true {
		t.Errorf("a run going on: act = %v", res)
	}
	// But a locked screen stops it.
	f.lock = true
	res = f.act(t, "Notes", map[string]any{"kind": "keypress", "intent": "x", "keys": []string{"enter"}})
	if res["error_code"] != "not_attended" {
		t.Errorf("locked mid-run: act = %v, want not_attended", res)
	}
}

func TestDesktopPausedAndResumed(t *testing.T) {
	f := newDesktop(t)
	f.observe(t, "Notes")
	// The user grabs the mouse.
	f.clock.Advance(time.Second)
	f.mon.set(f.clock.Now())
	f.tl.guard().poll(context.Background())
	res := f.act(t, "Notes", map[string]any{"kind": "keypress", "intent": "save", "keys": []string{"cmd", "s"}})
	if res["paused"] != true || firstResult(t, res)["code"] != "paused" {
		t.Errorf("act while paused = %v, want paused", res)
	}
	if k, _ := f.tl.Activity.Status(); k != Paused {
		t.Errorf("activity = %v, want paused", k)
	}
	f.clock.Advance(time.Second)
	res = call(t, f.tl, computeruse.ToolAct, map[string]any{"run_id": "cr_1", "target": desktopTarget("Notes"), "resume": true,
		"actions": []map[string]any{{"kind": "keypress", "intent": "save", "keys": []string{"cmd", "s"}}}})
	if res["success"] != true {
		t.Errorf("resumed act = %v", res)
	}
	if k, _ := f.tl.Activity.Status(); k != Controlling {
		t.Errorf("activity = %v, want controlling again", k)
	}
	// computer_stop ends it: the indicator clears.
	call(t, f.tl, computeruse.ToolStop, map[string]any{"run_id": "cr_1"})
	if k, _ := f.tl.Activity.Status(); k != Idle {
		t.Errorf("after stop: activity = %v, want idle", k)
	}
}

func TestDesktopUnsupported(t *testing.T) {
	tl := &Tools{Switch: NewSwitch(true, nil)}
	out := call(t, tl, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": desktopTarget("Notes")})
	if out["error_code"] != "unsupported" {
		t.Errorf("no desktop: observe = %v, want unsupported", out)
	}
	off := &Tools{Switch: NewSwitch(false, nil), Desktop: &Desktop{}}
	out = call(t, off, computeruse.ToolObserve, map[string]any{"run_id": "cr_1", "target": desktopTarget("Notes")})
	if out["error_code"] != "computer_control_off" {
		t.Errorf("switch off: observe = %v", out)
	}
}

func TestClipboardCommands(t *testing.T) {
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	cases := []struct {
		name, goos  string
		env         func(string) string
		read, write string
	}{
		{"macOS", "darwin", env(), "pbpaste", "pbcopy"},
		{"Wayland", "linux", env("WAYLAND_DISPLAY", "wayland-1", "DISPLAY", ":0"), "wl-paste --no-newline", "wl-copy"},
		{"X11", "linux", env("DISPLAY", ":0"), "xclip -selection clipboard -out", "xclip -selection clipboard -in"},
	}
	for _, c := range cases {
		cb := clipboardFor(c.goos, c.env)
		if cb == nil {
			t.Fatalf("%s: no clipboard", c.name)
		}
		var ran []string
		var fed string
		cb.run = func(_ context.Context, stdin *string, argv []string) ([]byte, error) {
			ran = append(ran, strings.Join(argv, " "))
			if stdin != nil {
				fed = *stdin
			}
			return []byte("copied"), nil
		}
		if err := cb.Write(context.Background(), "milk"); err != nil || fed != "milk" {
			t.Errorf("%s: write fed %q, %v", c.name, fed, err)
		}
		if got, err := cb.Read(context.Background()); err != nil || got != "copied" {
			t.Errorf("%s: read = %q, %v", c.name, got, err)
		}
		if len(ran) != 2 || ran[0] != c.write || ran[1] != c.read {
			t.Errorf("%s: ran %q, want %q then %q", c.name, ran, c.write, c.read)
		}
	}
	if clipboardFor("linux", env()) != nil || clipboardFor("windows", env()) != nil {
		t.Error("a clipboard without a session")
	}
}
