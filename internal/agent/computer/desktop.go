package computer

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Desktop tasks (plan P3 piece 22): computer_observe and computer_act for target desktop drive a
// desktop app with the real pointer and keyboard. They are attended (COMPUTER-CONTROL.md C9): a
// run starts only while the user is at the computer (screen unlocked, input in the last 5
// minutes), the guard pauses it the moment they touch the mouse or keyboard, and the island and
// tray show it the whole time. Every action is checked here, on the computer, before it runs
// (C8): the app under the point (or with the focus) against the built-in deny list (terminals,
// Jarvis itself, OS password prompts), the batch's blocked apps, and browsers, which only the
// extension drives (C3). An app the task hadn't used yet is allowed and reported (new_app, Q4).
// Inside a task the clipboard may be read and written (C7). macOS first: elements come from
// the accessibility tree, the rest from screenshots.

// Desktop is what desktop tasks need of this computer (NativeDesktop). Tests fake each part.
type Desktop struct {
	AX      AX      // nil: no element snapshots (screenshots only)
	Windows Windows // required
	// Capture captures one window, with its geometry.
	Capture func(ctx context.Context, w DesktopWindow) (screen.Shot, error)
	Input   input.Input   // required
	Monitor input.Monitor // the user's last input: idle check and the guard (nil: neither)
	// Locked reports whether the screen is locked (nil: never).
	Locked    func(ctx context.Context) (bool, error)
	Clipboard Clipboard // nil: no clipboard actions
	// Apps finds and opens installed apps through desktop_open's catalogue (nil: an app that
	// isn't running can't be used).
	Apps Apps
	// Focus gives window id (DesktopWindow.ID) the keyboard focus (nil: observe can't, and
	// keys go wherever the focus is).
	Focus func(ctx context.Context, id string) error
	Now   func() time.Time // nil: time.Now
	// Sleep pauses a batch between a click and the keys that follow it (nil: time.Sleep, bounded
	// by the context).
	Sleep func(ctx context.Context, d time.Duration)
}

// Apps is desktop_open's catalogue (desktop.Tools): FindApp takes an app's name only, never a
// link, a path or a URI; LaunchApp opens what FindApp returned, by its catalogue path.
type Apps interface {
	FindApp(name string) (apps.App, error)
	LaunchApp(ctx context.Context, app apps.App) error
}

// AX is the accessibility tree (package ax).
type AX interface {
	Snapshot(ctx context.Context, pid, max int) (computeruse.Snapshot, error)
	Press(gen int, ref string) error
	SetValue(gen int, ref, text string) error
	Focus(gen int, ref string) error
	// At is the element under global point x, y; Focused the one with the keyboard focus. Nil
	// when there's none.
	At(x, y float64) (*ax.Recorded, error)
	Focused() (*ax.Recorded, error)
}

// nativeAX is package ax as the executor's AX.
type nativeAX struct{}

func (nativeAX) Snapshot(ctx context.Context, pid, max int) (computeruse.Snapshot, error) {
	return ax.Snapshot(ctx, pid, max)
}
func (nativeAX) Press(gen int, ref string) error          { return ax.Press(gen, ref) }
func (nativeAX) SetValue(gen int, ref, text string) error { return ax.SetValue(gen, ref, text) }
func (nativeAX) Focus(gen int, ref string) error          { return ax.Focus(gen, ref) }
func (nativeAX) At(x, y float64) (*ax.Recorded, error)    { return ax.RecordedAt(x, y) }
func (nativeAX) Focused() (*ax.Recorded, error)           { return ax.FocusedElement() }

// Windows lists the windows on screen, front to back.
type Windows interface {
	List(ctx context.Context) ([]DesktopWindow, error)
}

// DesktopWindow is one window, in global points.
type DesktopWindow struct {
	ID         string
	App        string // the owner's name ("Notes")
	PID        int
	Title      string
	Layer      int // 0: an ordinary window; above it panels, prompts, the menu bar
	X, Y, W, H int
	// Focused marks the window with the keyboard focus where the list is the stacking order
	// and the focus can be below the top (Linux: a floating window over the focused tiled
	// one). No window marked: the front one has it (macOS).
	Focused bool
}

func (w DesktopWindow) contains(x, y int) bool {
	return x >= w.X && y >= w.Y && x < w.X+w.W && y < w.Y+w.H
}

// normal: an ordinary window big enough to be one the user works in.
func (w DesktopWindow) normal() bool { return w.Layer == 0 && w.W >= 50 && w.H >= 50 }

// Clipboard reads and writes the clipboard's text.
type Clipboard interface {
	Read(ctx context.Context) (string, error)
	Write(ctx context.Context, text string) error
}

const (
	// attendedIdle: a task doesn't start when nobody used the computer for longer.
	attendedIdle = 5 * time.Minute
	// desktopIdle: after this long without a desktop call the task is taken to have ended (cc
	// went away mid-run): the guard stops and the indicator clears. The next call starts over.
	desktopIdle = 10 * time.Minute
	// launchWait is how long observe waits for a launched app's window.
	launchWait = 10 * time.Second
	// maxCaptures is how many captures per run an action may name.
	maxCaptures = 4
)

// desktopState is the executor's desktop side: one guard (one pointer and keyboard), polling
// while a desktop task runs.
type desktopState struct {
	mu        sync.Mutex
	guard     *Guard
	stopGuard context.CancelFunc // nil: not polling
	idle      *time.Timer
}

func (t *Tools) guard() *Guard {
	t.desk.mu.Lock()
	defer t.desk.mu.Unlock()
	if t.desk.guard == nil {
		t.desk.guard = &Guard{Input: t.Desktop.Input, Monitor: t.Desktop.Monitor, Activity: t.Activity, Now: t.Desktop.Now}
	}
	return t.desk.guard
}

func (t *Tools) dnow() time.Time {
	if t.Desktop != nil && t.Desktop.Now != nil {
		return t.Desktop.Now()
	}
	return time.Now()
}

// attended is nil when the user is at the computer: the screen isn't locked and, unless only
// the lock is asked about, they used it in the last 5 minutes.
func (t *Tools) attended(ctx context.Context, lockOnly bool) error {
	d := t.Desktop
	if d.Locked != nil {
		locked, err := d.Locked(ctx)
		if err != nil {
			return fmt.Errorf("I can't tell whether the screen is locked (%v), so I won't use the mouse and keyboard", err)
		}
		if locked {
			return errors.New("the screen is locked: desktop tasks run while you're at the computer")
		}
	}
	if lockOnly || d.Monitor == nil {
		return nil
	}
	last, err := d.Monitor.Since(ctx)
	if err != nil {
		return fmt.Errorf("I can't tell whether you're at the computer (%v), so I won't use the mouse and keyboard", err)
	}
	if last.IsZero() {
		return errors.New("I haven't seen anyone use this computer since I started watching: desktop tasks run while you're at the computer")
	}
	if idle := t.dnow().Sub(last); idle > attendedIdle {
		return fmt.Errorf("nobody has used this computer for %d minutes: desktop tasks run while you're at the computer",
			int(idle/time.Minute))
	}
	return nil
}

// begin starts (or resumes) run id on the desktop: refused while the user isn't there; then the
// guard watches from now on and the island shows Jarvis in control.
func (t *Tools) begin(ctx context.Context, id string, resume bool) (code string, err error) {
	run := t.runs().Get(id)
	if run.attended && !resume {
		// A run going on: the user may be watching it work, but not with the screen locked.
		if err := t.attended(ctx, true); err != nil {
			return "not_attended", err
		}
		t.touchDesktop()
		return "", nil
	}
	if err := t.attended(ctx, false); err != nil {
		return "not_attended", err
	}
	g := t.guard()
	if run.attended {
		g.Resume(ctx)
	} else {
		g.Begin(ctx)
		t.runs().With(id, func(r *Run) { r.attended = true })
	}
	t.desk.mu.Lock()
	if t.desk.stopGuard == nil {
		gctx, cancel := context.WithCancel(context.Background())
		t.desk.stopGuard = cancel
		go g.Run(gctx)
	}
	t.desk.mu.Unlock()
	if k, _ := t.Activity.Status(); k == Idle {
		t.Activity.Set(Controlling, "Working on your computer")
	}
	t.touchDesktop()
	return "", nil
}

// touchDesktop pushes back the end of an abandoned task.
func (t *Tools) touchDesktop() {
	t.desk.mu.Lock()
	defer t.desk.mu.Unlock()
	if t.desk.idle == nil {
		t.desk.idle = time.AfterFunc(desktopIdle, t.desktopAbandoned)
		return
	}
	t.desk.idle.Reset(desktopIdle)
}

// desktopAbandoned ends the desktop side of every run after desktopIdle without a call.
func (t *Tools) desktopAbandoned() {
	t.runs().forEach(func(r *Run) { r.attended = false })
	t.endDesktop()
}

// endDesktop stops the guard and clears a desktop task's indicator.
func (t *Tools) endDesktop() {
	t.desk.mu.Lock()
	if t.desk.stopGuard != nil {
		t.desk.stopGuard()
		t.desk.stopGuard = nil
	}
	if t.desk.idle != nil {
		t.desk.idle.Stop()
		t.desk.idle = nil
	}
	t.desk.mu.Unlock()
	if k, _ := t.Activity.Status(); k == Controlling || k == Paused {
		t.Activity.Set(Idle, "")
	}
}

// closeDesktop stops the guard (the agent quits; tests).
func (t *Tools) closeDesktop() {
	t.desk.mu.Lock()
	defer t.desk.mu.Unlock()
	if t.desk.stopGuard != nil {
		t.desk.stopGuard()
		t.desk.stopGuard = nil
	}
	if t.desk.idle != nil {
		t.desk.idle.Stop()
		t.desk.idle = nil
	}
}

// appKeyOf is how apps are compared: lower case, letters and digits, a bundle id's last part
// ("com.apple.Notes" and "Notes" are both "notes").
func appKeyOf(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".app")
	if i := strings.LastIndexByte(n, '.'); i >= 0 && strings.Count(n, ".") >= 2 {
		n = n[i+1:]
	}
	var b strings.Builder
	for _, r := range n {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sameApp reports whether one of names is app.
func sameApp(names []string, app string) bool {
	k := appKeyOf(app)
	if k == "" {
		return false
	}
	for _, n := range names {
		if appKeyOf(n) == k {
			return true
		}
	}
	return false
}

// appCheck is whether the app named names may be acted in: not on the built-in deny list (el
// is the element acted on, or nil), not blocked for this task, not a browser.
func appCheck(names []string, el *computeruse.Element, blocked []string) (code string, err error) {
	for _, n := range names {
		if n == "" {
			continue
		}
		if deny, why := computeruse.BuiltinDeny(computeruse.Target{Kind: TargetDesktop, App: n}, el); deny {
			return "denied", errors.New(why)
		}
	}
	for _, b := range blocked {
		if sameApp(names, b) {
			return "blocked_app", fmt.Errorf("%s is blocked for this task", b)
		}
	}
	if winmatch.IsBrowser(names) {
		return "browser_window", fmt.Errorf("%s is a web browser: web pages go through the Jarvis extension, as a browser task", names[0])
	}
	return "", nil
}

// findWindow is app's front ordinary window ("" app: the focused one, else the frontmost). A
// window whose title is the name stands in when no app has it: a toolkit's generic class (Tk
// apps are all "Tk" on X11) doesn't name the app. What may be done in it still goes by its app.
func findWindow(ws []DesktopWindow, app string) (DesktopWindow, bool) {
	if app == "" {
		for _, w := range ws {
			if w.Focused && w.normal() {
				return w, true
			}
		}
	}
	for _, w := range ws {
		if w.normal() && (app == "" || sameApp([]string{w.App}, app)) {
			return w, true
		}
	}
	for _, w := range ws {
		if w.normal() && app != "" && sameApp([]string{w.Title}, app) {
			return w, true
		}
	}
	return DesktopWindow{}, false
}

// window is app's front window, launching the app through desktop_open's catalogue when it has
// none.
func (t *Tools) window(ctx context.Context, app string) (DesktopWindow, string, error) {
	d := t.Desktop
	ws, err := d.Windows.List(ctx)
	if err != nil {
		return DesktopWindow{}, "not_found", err
	}
	if w, ok := findWindow(ws, app); ok {
		return w, "", nil
	}
	if app == "" || d.Apps == nil {
		return DesktopWindow{}, "not_found", fmt.Errorf("%s has no window open", firstNonEmpty(app, "no app"))
	}
	// The app opened is the catalogue's match, checked like the app asked for: a near name of
	// a denied app is that app.
	found, err := d.Apps.FindApp(app)
	if errors.Is(err, host.ErrInvalidTarget) {
		return DesktopWindow{}, "invalid_argument", fmt.Errorf("couldn't open %s: %w", app, err)
	}
	if err != nil {
		return DesktopWindow{}, "not_found", fmt.Errorf("couldn't open %s: %w", app, err)
	}
	if code, err := appCheck([]string{found.Name}, nil, nil); err != nil {
		return DesktopWindow{}, code, err
	}
	app = found.Name
	if err := d.Apps.LaunchApp(ctx, found); err != nil {
		return DesktopWindow{}, "not_found", fmt.Errorf("couldn't open %s: %w", app, err)
	}
	deadline := time.Now().Add(launchWait)
	for {
		if ws, err := d.Windows.List(ctx); err == nil {
			if w, ok := findWindow(ws, app); ok {
				return w, "", nil
			}
		}
		if time.Now().After(deadline) {
			return DesktopWindow{}, "not_found", fmt.Errorf("%s didn't open a window", app)
		}
		select {
		case <-ctx.Done():
			return DesktopWindow{}, "not_found", ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// focus gives w the keyboard focus unless it has it.
func (t *Tools) focus(ctx context.Context, w DesktopWindow) error {
	ws, err := t.Desktop.Windows.List(ctx)
	if err != nil {
		return err
	}
	if f, ok := findWindow(ws, ""); ok && f.ID == w.ID {
		return nil
	}
	if err := t.Desktop.Focus(ctx, w.ID); err != nil {
		return fmt.Errorf("couldn't bring %s to the front: %w", w.App, err)
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func (t *Tools) observeDesktop(ctx context.Context, a observeArgs) map[string]any {
	if t.Desktop == nil {
		return failCode("unsupported", "desktop tasks aren't supported on this computer yet")
	}
	run := t.runs().Get(a.RunID)
	if run.Stopped {
		return failCode("stopped", "the task was stopped")
	}
	app := firstNonEmpty(a.Target.App, run.App)
	// What the task is in is checked before anything is opened or watched.
	if app != "" {
		if code, err := appCheck([]string{app}, nil, nil); err != nil {
			return failCode(code, "%v", err)
		}
	}
	if code, err := t.begin(ctx, a.RunID, false); err != nil {
		return failCode(code, "%v", err)
	}
	w, code, err := t.window(ctx, app)
	if err != nil {
		return failCode(code, "%v", err)
	}
	if code, err := appCheck([]string{w.App}, nil, nil); err != nil {
		return failCode(code, "%v", err)
	}
	out := map[string]any{"success": true, "target": TargetDesktop, "app": w.App, "window_id": w.ID}
	// The app looked at is the one acted in next: its window takes the focus, so keys and
	// typing go to it (a replay's step in another app, #581).
	if app != "" && t.Desktop.Focus != nil {
		if err := t.focus(ctx, w); err != nil {
			out["focus_error"] = err.Error()
		}
	}
	t.runs().With(a.RunID, func(r *Run) {
		if r.App == "" {
			r.App = w.App
		}
		r.PID, r.Window = w.PID, w.ID
		r.knowApp(w.App)
		r.knowApp(app)
	})
	if !a.Snapshot && !a.Screenshot {
		a.Snapshot = true
	}
	if w.Title != "" {
		out["title"] = w.Title
	}
	if a.Snapshot {
		snap := map[string]any{"gen": 0, "elements": []computeruse.SnapElement{}}
		var s *computeruse.Snapshot
		if t.Desktop.AX == nil {
			out["snapshot_error"] = "no accessibility tree on this computer: use the screenshot"
		} else if got, err := t.Desktop.AX.Snapshot(ctx, w.PID, 0); err != nil {
			out["snapshot_error"] = err.Error()
		} else {
			s = &got
			snap = map[string]any{"gen": got.Gen, "elements": got.Elements}
			if got.Title != "" {
				out["title"] = got.Title
			}
		}
		t.runs().With(a.RunID, func(r *Run) { r.Snap = s })
		out["snapshot"] = snap
	}
	if a.Screenshot {
		if t.Desktop.Capture == nil {
			return failCode("capture_failed", "this computer can't take screenshots")
		}
		shot, err := t.Desktop.Capture(ctx, w)
		if err != nil {
			return failCode("capture_failed", "%v", err)
		}
		data, err := screen.EncodeShot(&shot)
		if err != nil {
			return failCode("capture_failed", "%v", err)
		}
		g := shot.Geometry
		var id string
		t.runs().With(a.RunID, func(r *Run) {
			r.captures++
			id = fmt.Sprintf("cap_%d_%s", r.captures, randHex(4))
			r.keepCapture(id, g)
		})
		out["capture"] = map[string]any{"capture_id": id, "width": g.ImageW, "height": g.ImageH,
			"origin_x": g.OriginX, "origin_y": g.OriginY, "logical_w": g.LogicalW, "logical_h": g.LogicalH,
			"scale": g.Scale, "monitor": g.Monitor}
		out[screen.AttachmentsKey] = []any{map[string]any{"mime": "image/jpeg",
			"data": base64.StdEncoding.EncodeToString(data), "label": "screenshot of " + w.App + "'s window"}}
	}
	return out
}

func (t *Tools) actDesktop(ctx context.Context, a actArgs) map[string]any {
	if t.Desktop == nil {
		return failCode("unsupported", "desktop tasks aren't supported on this computer yet")
	}
	if t.runs().Get(a.RunID).Stopped {
		return failCode("stopped", "the task was stopped")
	}
	if code, err := t.begin(ctx, a.RunID, a.Resume); err != nil {
		return failCode(code, "%v", err)
	}
	results := []map[string]any{}
	out := map[string]any{"success": true}
	var newApps []string
	for i, action := range a.Actions {
		r := t.desktopStep(ctx, a, i, action)
		results = append(results, r.out())
		if r.NewApp != "" {
			newApps = append(newApps, r.NewApp)
		}
		if !r.OK {
			// The batch's failure is the action's: callers that read only the top (the
			// tool-call trace) see why too (#581).
			out["success"], out["error_code"], out["error"] = false, cmp.Or(r.Code, "failed"), r.Error
			out["stopped_at"] = i
			if r.Code == "paused" {
				out["paused"] = true
			}
			break
		}
		if i+1 < len(a.Actions) && focusSettleNeeded(action, a.Actions[i+1]) {
			t.sleep(ctx, focusSettle)
		}
	}
	out["results"] = results
	if len(newApps) > 0 {
		out["new_apps"] = newApps
	}
	return out
}

// focusSettle is the pause between a click and the type or keypress that follows it in the same
// batch: the click's focus change reaches the app after the click returns (a Tk field under
// Xwayland takes keys into whichever widget holds focus when they arrive), so keys sent on the
// click's heels land in the old field (#528).
const focusSettle = 250 * time.Millisecond

// focusSettleNeeded: done may have moved the focus and next sends keys to it.
func focusSettleNeeded(done, next computeruse.Action) bool {
	switch done.Kind {
	case "click", "double_click", "triple_click":
	default:
		return false
	}
	return (next.Kind == "type" || next.Kind == "keypress") && next.Ref == ""
}

func (t *Tools) sleep(ctx context.Context, d time.Duration) {
	if t.Desktop != nil && t.Desktop.Sleep != nil {
		t.Desktop.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// desktopStep runs action i of a desktop batch, after every check.
func (t *Tools) desktopStep(ctx context.Context, a actArgs, i int, action computeruse.Action) Result {
	fail := func(code, format string, args ...any) Result {
		return Result{I: i, Code: code, Error: fmt.Sprintf(format, args...)}
	}
	run := t.runs().Get(a.RunID)
	if run.Stopped {
		return fail("stopped", "the task was stopped")
	}
	if err := t.guard().Check(); err != nil {
		return fail("paused", "%v", err)
	}
	if err := action.Validate(); err != nil {
		return fail("invalid_action", "%v", err)
	}
	if deny, why := computeruse.DenyAction(action, computeruse.Target{Kind: TargetDesktop, App: run.App}); deny {
		return fail("denied", "%s", why)
	}
	tgt := computeruse.Target(a.Target)
	tgt.Kind, tgt.App = TargetDesktop, firstNonEmpty(run.App, tgt.App)

	// Where the action lands: a point (mapped from the capture), the focus, or an element.
	var (
		pts   [][2]int
		names []string
		el    *computeruse.Element
		rec   *ax.Recorded
		// secureFocus: text would go to a password field (refused after the deny list).
		secureFocus bool
	)
	switch action.Kind {
	case "click", "double_click", "triple_click", "move", "drag", "scroll":
		if action.Kind == "scroll" && action.X == nil {
			names, rec = t.focusedApp(ctx)
			break
		}
		var g computeruse.Geometry
		var ok bool
		t.runs().With(a.RunID, func(r *Run) { g, ok = r.capture(action.CaptureID) })
		if !ok {
			return fail("invalid_action", "no capture %q in this task: take a screenshot first", action.CaptureID)
		}
		if action.Kind == "drag" {
			for _, p := range action.Path {
				x, y := g.ToGlobal(p[0], p[1])
				pts = append(pts, [2]int{x, y})
			}
		} else {
			x, y := g.ToGlobal(*action.X, *action.Y)
			pts = [][2]int{{x, y}}
		}
		for _, p := range pts {
			n, r, code, err := t.appAt(ctx, p[0], p[1], a.Blocked.Apps)
			if err != nil {
				return fail(code, "%v", err)
			}
			if names == nil {
				names, rec = n, r
			}
		}
	case "keypress", "type":
		if action.Ref == "" {
			names, rec = t.focusedApp(ctx)
			secureFocus = rec != nil && rec.Secure && action.Kind == "type"
			break
		}
		fallthrough
	case "press", "set_value", "select":
		var snapEl *computeruse.SnapElement
		if run.Snap != nil {
			snapEl = run.Snap.Find(action.Ref)
		}
		if snapEl == nil {
			if run.Snap == nil || len(run.Snap.Elements) == 0 {
				return fail("stale_ref", "%q names an element, but this app lists none (screenshots only): "+
					"act by coordinates on the capture (click x,y, then type or keypress)", action.Ref)
			}
			return fail("stale_ref", "%s isn't in the last snapshot: take a new one", action.Ref)
		}
		el = snapEl.Element()
		names = []string{run.App}
		if (action.Kind == "set_value" || action.Kind == "type") && el.Secure {
			return fail("password_field", "%s is a password field: Jarvis doesn't type passwords; the user enters it", action.Ref)
		}
	case "navigate":
		return fail("browser_window", "web pages go through the Jarvis extension, as a browser task, not a desktop one")
	case "zoom":
		return fail("invalid_action", "zoom isn't supported on the desktop: take a screenshot")
	}
	if rec != nil && el == nil {
		el = &computeruse.Element{Role: rec.Role, Name: rec.Name, Secure: rec.Secure}
	}
	if names != nil {
		if code, err := appCheck(names, el, a.Blocked.Apps); err != nil {
			return fail(code, "%v", err)
		}
	}
	if secureFocus {
		return fail("password_field", "the focused field is a password field: Jarvis doesn't type passwords; the user enters it")
	}
	if r, ok := t.checkRisk(ctx, a.RunID, i, action, el, tgt); !ok {
		return r
	}

	var newApp string
	if names != nil {
		t.runs().With(a.RunID, func(r *Run) {
			if !r.knows(names) {
				newApp = displayApp(names)
				for _, n := range names {
					r.knowApp(n)
				}
			}
		})
	}
	t.showing(action.Intent)
	text, code, err := t.doDesktop(ctx, action, run, pts)
	if err != nil {
		return Result{I: i, Code: code, Error: err.Error(), NewApp: newApp}
	}
	return Result{I: i, OK: true, NewApp: newApp, Text: text}
}

// displayApp is how the progress card names an app: its window's name, else its bundle id.
func displayApp(names []string) string {
	for _, n := range names {
		if n != "" && !strings.Contains(n, ".") {
			return n
		}
	}
	return firstNonEmpty(names...)
}

// appAt is the app under global point x, y: the accessibility hit test's and the front ordinary
// window's there. A window in front of it that is on the deny list (a password prompt above
// the app) refuses the point too.
func (t *Tools) appAt(ctx context.Context, x, y int, blocked []string) (names []string, rec *ax.Recorded, code string, err error) {
	if t.Desktop.AX != nil {
		if r, err := t.Desktop.AX.At(float64(x), float64(y)); err == nil && r != nil {
			rec = r
			names = append(names, r.App)
		}
	}
	ws, err := t.Desktop.Windows.List(ctx)
	if err != nil {
		return nil, nil, "failed", fmt.Errorf("couldn't list the windows: %w", err)
	}
	for _, w := range ws {
		if !w.contains(x, y) || w.App == "Window Server" {
			continue
		}
		if w.Layer == 0 {
			names = append([]string{w.App}, names...)
			break
		}
		if code, err := appCheck([]string{w.App}, nil, blocked); code == "denied" {
			return nil, nil, code, err
		}
	}
	if len(names) == 0 {
		return nil, nil, "not_found", fmt.Errorf("there's no window at %d,%d", x, y)
	}
	return names, rec, "", nil
}

// focusedApp is the app keys and text go to: the accessibility focus's and the front window's.
func (t *Tools) focusedApp(ctx context.Context) (names []string, rec *ax.Recorded) {
	if t.Desktop.AX != nil {
		if r, err := t.Desktop.AX.Focused(); err == nil && r != nil {
			rec = r
			names = append(names, r.App)
		}
	}
	if ws, err := t.Desktop.Windows.List(ctx); err == nil {
		if w, ok := findWindow(ws, ""); ok {
			names = append([]string{w.App}, names...)
		}
	}
	return names, rec
}

// doDesktop runs one checked action. text is clipboard_read's.
func (t *Tools) doDesktop(ctx context.Context, action computeruse.Action, run Run, pts [][2]int) (text, code string, err error) {
	d := t.Desktop
	in := t.guard().Guarded()
	axErr := func(err error) (string, string, error) {
		var e *ax.Error
		if errors.As(err, &e) && e.Code != "" {
			return "", e.Code, err
		}
		return "", "failed", err
	}
	inErr := func(err error) (string, string, error) {
		if errors.Is(err, ErrPaused) {
			return "", "paused", err
		}
		return "", "failed", err
	}
	button := firstNonEmpty(action.Button, "left")
	gen := 0
	if run.Snap != nil {
		gen = run.Snap.Gen
	}
	switch action.Kind {
	case "click", "double_click", "triple_click":
		n := map[string]int{"click": 1, "double_click": 2, "triple_click": 3}[action.Kind]
		if err := in.Click(ctx, pts[0][0], pts[0][1], button, n); err != nil {
			return inErr(err)
		}
	case "move":
		if err := in.Move(ctx, pts[0][0], pts[0][1]); err != nil {
			return inErr(err)
		}
	case "drag":
		if err := in.Drag(ctx, pts); err != nil {
			return inErr(err)
		}
	case "scroll":
		x, y := 0, 0
		if len(pts) > 0 {
			x, y = pts[0][0], pts[0][1]
		} else if cx, cy, err := in.Cursor(ctx); err == nil {
			x, y = cx, cy
		}
		if err := in.Scroll(ctx, x, y, action.DX, action.DY); err != nil {
			return inErr(err)
		}
	case "keypress":
		if action.Ref != "" {
			if err := d.AX.Focus(gen, action.Ref); err != nil {
				return axErr(err)
			}
		}
		if err := in.Keys(ctx, action.Keys); err != nil {
			return inErr(err)
		}
	case "type":
		if action.Ref != "" {
			if err := d.AX.SetValue(gen, action.Ref, action.Text); err != nil {
				return axErr(err)
			}
			break
		}
		if err := in.Type(ctx, action.Text); err != nil {
			return inErr(err)
		}
	case "press":
		if d.AX == nil {
			return "", "not_found", errors.New("no accessibility tree on this computer")
		}
		if err := d.AX.Press(gen, action.Ref); err != nil {
			return axErr(err)
		}
	case "set_value", "select":
		if d.AX == nil {
			return "", "not_found", errors.New("no accessibility tree on this computer")
		}
		if err := d.AX.SetValue(gen, action.Ref, action.Text); err != nil {
			return axErr(err)
		}
	case "clipboard_read", "clipboard_write":
		if d.Clipboard == nil {
			return "", "unsupported", errors.New("no clipboard on this computer")
		}
		if action.Kind == "clipboard_write" {
			if err := d.Clipboard.Write(ctx, action.Text); err != nil {
				return "", "failed", fmt.Errorf("couldn't write the clipboard: %w", err)
			}
			break
		}
		s, err := d.Clipboard.Read(ctx)
		if err != nil {
			return "", "failed", fmt.Errorf("couldn't read the clipboard: %w", err)
		}
		if utf8.RuneCountInString(s) > computeruse.MaxText {
			s = string([]rune(s)[:computeruse.MaxText])
		}
		return s, "", nil
	case "wait":
		select {
		case <-time.After(time.Duration(action.MS) * time.Millisecond):
		case <-ctx.Done():
			return "", "timeout", ctx.Err()
		}
	case "screenshot":
		// the next computer_observe takes it
	}
	return "", "", nil
}
