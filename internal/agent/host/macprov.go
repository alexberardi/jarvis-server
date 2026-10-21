package host

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
)

// Darwin shells out to osascript (volume) and open(1); windows go through the Accessibility API
// (AX, an ax.Windows on macOS).
//
// Window control needs an Accessibility (TCC) grant for the binary hosting the agent. TCC grants
// are per binary, which is one reason this is a single Go binary rather than a Python script:
// the grant goes to jarvis-agent, not to a Homebrew Python that a patch upgrade silently
// replaces.
//
// Not named *_darwin.go: it compiles everywhere so the contract suite can run it everywhere.
type Darwin struct {
	Sys Sys
	// AX is the Accessibility window control; nil: no window control. Native sets it; tests
	// leave it nil or pass a fake.
	AX MacAX

	log    *slog.Logger // nil: slog.Default()
	silent sync.Map     // pids already logged as not answering
}

// MacAX is the Accessibility window control (ax.Windows).
type MacAX interface {
	Trusted() bool
	Apps() ([]ax.App, error)
	AppWindows(pid int) ([]ax.AppWindow, error)
	Act(pid, index int, action string) error
}

// NewDarwin builds the macOS backend. It touches nothing.
func NewDarwin(s Sys) *Darwin { return &Darwin{Sys: s} }

func (*Darwin) Platform() string { return "darwin" }

func (d *Darwin) Capabilities(context.Context) []string {
	var caps []string
	if d.Sys.LookPath("open") {
		caps = append(caps, CapOpen)
	}
	if d.Sys.LookPath("osascript") {
		caps = append(caps, CapVolumeGet, CapVolumeSet)
	}
	if d.AX != nil && d.AX.Trusted() {
		caps = append(caps, AllWindowCaps...)
	}
	return caps
}

func (d *Darwin) osascript(ctx context.Context, script string) (string, error) {
	if !d.Sys.LookPath("osascript") {
		return "", Errorf(ErrNotSupported, "osascript unavailable")
	}
	out, err := d.Sys.Run(ctx, "osascript", "-e", script)
	if err != nil {
		msg := err.Error()
		// -1719 / -25211 / -1743 are TCC denials (assistive access, automation).
		if strings.Contains(msg, "-1719") || strings.Contains(msg, "-25211") || strings.Contains(msg, "-1743") || strings.Contains(msg, "-1728") || strings.Contains(msg, "not allowed") {
			return "", Errorf(ErrNotSupported, "Accessibility/Automation permission required: %s", msg)
		}
		return "", Errorf(ErrOperationFailed, "%s", msg)
	}
	return out, nil
}

func (d *Darwin) GetVolume(ctx context.Context) (int, error) {
	raw, err := d.osascript(ctx, "output volume of (get volume settings)")
	if err != nil {
		return 0, err
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		// "missing value": the current output device has no software volume (e.g. HDMI).
		return 0, Errorf(ErrOperationFailed, "output device reports no volume (%q)", raw)
	}
	return v, nil
}

func (d *Darwin) ApplyVolume(ctx context.Context, level int) error {
	_, err := d.osascript(ctx, fmt.Sprintf("set volume output volume %d", level))
	return err
}

// Window ids are "<pid>/<index>": the owning process and the window's 1-based position in its
// AXWindows (the order System Events' "window N" used, the frontmost first). Opaque to callers,
// and only as stable as the window list, which is what the contract promises (valid until the
// window set changes). AX windows have no usable id.
var reMacID = regexp.MustCompile(`^(\d{1,10})/(\d{1,4})$`)

// listBudget bounds a window list: an app whose windows aren't in by then is left out. Its own
// messaging timeout (ax.AppTimeout) normally fails it sooner; this also covers a call that
// ignores it.
const listBudget = 300 * time.Millisecond

func (d *Darwin) ListWindows(ctx context.Context) ([]Window, error) {
	if d.AX == nil {
		return nil, Errorf(ErrNotSupported, "window control needs the Accessibility API")
	}
	apps, err := d.AX.Apps()
	if err != nil {
		return nil, macAXError(err, "the window list")
	}
	// Every app at once (a desktop has tens): one that doesn't answer costs the list its
	// timeout once, not once per app in turn.
	type result struct {
		i   int
		ws  []ax.AppWindow
		err error
	}
	done := make(chan result, len(apps))
	for i, a := range apps {
		go func() {
			ws, err := d.AX.AppWindows(a.PID)
			done <- result{i, ws, err}
		}()
	}
	got := make([]*result, len(apps))
	timer := time.NewTimer(listBudget)
	defer timer.Stop()
collect:
	for range apps {
		select {
		case r := <-done:
			got[r.i] = &r
		case <-timer.C:
			break collect
		case <-ctx.Done():
			return nil, Errorf(ErrOperationFailed, "window list: %v", ctx.Err())
		}
	}
	out := []Window{}
	for i, a := range apps {
		r := got[i]
		switch {
		case r == nil:
			d.skipped(a, "did not answer within "+listBudget.String())
			continue
		case errors.Is(r.err, ax.ErrNotTrusted):
			return nil, macAXError(r.err, "the window list")
		case errors.Is(r.err, ax.ErrNoWindow):
			continue // it quit
		case r.err != nil:
			d.skipped(a, r.err.Error())
			continue
		}
		d.silent.Delete(a.PID)
		for j, w := range r.ws {
			out = append(out, Window{ID: fmt.Sprintf("%d/%d", a.PID, j+1), App: a.Name, Title: w.Title, Minimized: w.Minimized})
		}
	}
	return out, nil
}

// skipped logs an app left out of the window list, once until it answers again.
func (d *Darwin) skipped(a ax.App, why string) {
	if _, seen := d.silent.LoadOrStore(a.PID, true); seen {
		return
	}
	l := d.log
	if l == nil {
		l = slog.Default()
	}
	l.Warn("window list: an app did not answer Accessibility; its windows are left out", "app", a.Name, "pid", a.PID, "why", why)
}

// macAXError maps an ax error to the host's.
func macAXError(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ax.ErrNoWindow):
		return Errorf(ErrWindowNotFound, "%s no longer exists", what)
	case errors.Is(err, ax.ErrNotTrusted):
		return Errorf(ErrNotSupported, "Accessibility permission required: %v", err)
	case errors.Is(err, ax.ErrUnsupported):
		return Errorf(ErrNotSupported, "%v", err)
	}
	return Errorf(ErrOperationFailed, "%s: %v", what, err)
}

var macActions = map[WindowAction]string{
	ActionMinimize: ax.ActMinimize,
	ActionFocus:    ax.ActFocus,
	// The window's own close button: the same graceful request a click makes, so the app can
	// prompt to save. Never quit or kill the process.
	ActionClose: ax.ActClose,
}

func (d *Darwin) act(_ context.Context, action WindowAction, id string) error {
	if d.AX == nil {
		return Errorf(ErrNotSupported, "window control needs the Accessibility API")
	}
	m := reMacID.FindStringSubmatch(id)
	if m == nil {
		return Errorf(ErrWindowNotFound, "not a window id: %q", id)
	}
	pid, _ := strconv.Atoi(m[1])
	idx, _ := strconv.Atoi(m[2])
	return macAXError(d.AX.Act(pid, idx, macActions[action]), "window "+id)
}

func (d *Darwin) MinimizeWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionMinimize, id)
}
func (d *Darwin) FocusWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionFocus, id)
}
func (d *Darwin) CloseWindow(ctx context.Context, id string) error {
	return d.act(ctx, ActionClose, id)
}

func (d *Darwin) Open(_ context.Context, target string) error {
	if !d.Sys.LookPath("open") {
		return Errorf(ErrNotSupported, "the open binary is unavailable")
	}
	if err := d.Sys.Start("open", target); err != nil {
		return Errorf(ErrOperationFailed, "could not open %q: %v", target, err)
	}
	return nil
}

// LaunchApp opens a .app bundle with open(1), which starts it or brings it forward.
func (d *Darwin) LaunchApp(_ context.Context, path string) error {
	if !d.Sys.LookPath("open") {
		return Errorf(ErrNotSupported, "the open binary is unavailable")
	}
	if err := d.Sys.Start("open", path); err != nil {
		return Errorf(ErrOperationFailed, "could not launch %q: %v", path, err)
	}
	return nil
}
