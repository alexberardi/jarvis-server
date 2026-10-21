package dictate

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// HostWindows focuses through the host backend's window control (X11 wmctrl, macOS System
// Events, Windows user32), which already validates its own ids, and asks the platform which
// window is in front so a focus is confirmed before anything is typed.
type HostWindows struct {
	H *host.Host
	// ActiveFn is the id of the window in front, in whatever form SameFn compares
	// (xdotool's decimal X11 id, macOS's frontmost pid, a decimal HWND). Nil: unavailable.
	ActiveFn func(ctx context.Context) (string, error)
	SameFn   func(id, active string) bool
	// Why is the message when ActiveFn is missing.
	Why string
	// HistoryFn sets the listed windows' Recency from the platform's focus history and says
	// whether it could (focushistory.go); nil: none.
	HistoryFn func(ctx context.Context, ws []Window) bool
}

func (w *HostWindows) Available(ctx context.Context) (bool, string) {
	if w.H == nil || w.ActiveFn == nil {
		if w.Why != "" {
			return false, w.Why
		}
		return false, "switching to a named app isn't supported on this desktop"
	}
	if !w.H.Has(ctx, host.CapWindowsList) || !w.H.Has(ctx, host.CapWindowsFocus) {
		return false, "switching to a named app needs window control, which is off on this computer (see jarvis-agent status)"
	}
	return true, ""
}

func (w *HostWindows) List(ctx context.Context) ([]Window, error) {
	hs, err := w.H.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Window, len(hs))
	for i, h := range hs {
		out[i] = Window{ID: h.ID, Apps: []string{h.App}, Title: h.Title, Recency: i, Visible: !h.Minimized}
	}
	return out, nil
}

func (w *HostWindows) Focus(ctx context.Context, id string) error {
	return w.H.WindowDo(ctx, host.ActionFocus, id)
}

func (w *HostWindows) Active(ctx context.Context) (string, error) { return w.ActiveFn(ctx) }

func (w *HostWindows) Same(id, active string) bool {
	if w.SameFn != nil {
		return w.SameFn(id, active)
	}
	return id != "" && id == active
}

// SameX11 compares wmctrl's 0x… id with xdotool's decimal one.
func SameX11(id, active string) bool {
	a, err1 := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(id), "0x"), 16, 64)
	b, err2 := strconv.ParseUint(strings.TrimSpace(active), 10, 64)
	return err1 == nil && err2 == nil && a == b && a != 0
}

// SameMacPid: a macOS window id is pid/index; the front is known by its app's pid (raising a
// window also makes its app frontmost).
func SameMacPid(id, active string) bool {
	pid, _, ok := strings.Cut(id, "/")
	return ok && active != "" && pid == strings.TrimSpace(active)
}

// X11Active is the focused X11 window as a decimal id: from the window manager
// (_NET_ACTIVE_WINDOW) when the built-in client can ask it, else from xdotool.
func X11Active(x Exec, wm X11ActiveWM) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if wm != nil {
			if ok, _ := wm.WMAvailable(ctx); ok {
				id, err := wm.Active(ctx)
				if err != nil {
					return "", err
				}
				return strconv.FormatUint(uint64(id), 10), nil
			}
		}
		return x.Run(ctx, Cmd{Name: "xdotool", Args: []string{"getactivewindow"}})
	}
}

// X11ActiveWM is the built-in EWMH client's part X11Active uses (*x11.Display).
type X11ActiveWM interface {
	WMAvailable(ctx context.Context) (bool, string)
	Active(ctx context.Context) (uint32, error)
}

// MacFrontPid is the frontmost app's pid via System Events (the Automation grant setup asks for;
// window control itself goes through Accessibility).
func MacFrontPid(x Exec) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		return x.Run(ctx, Cmd{Name: "osascript", Args: []string{"-e",
			`tell application "System Events" to get unix id of first process whose frontmost is true`}})
	}
}

// utf16Chunks splits text into UTF-16 runs of at most n units without splitting a surrogate
// pair (CGEventKeyboardSetUnicodeString takes at most 20 units per event).
func utf16Chunks(text string, n int) [][]uint16 {
	u := utf16.Encode([]rune(text))
	var out [][]uint16
	for len(u) > 0 {
		k := min(n, len(u))
		if k < len(u) && k > 1 && utf16.IsSurrogate(rune(u[k-1])) && u[k-1] >= 0xD800 && u[k-1] < 0xDC00 {
			k-- // don't end a chunk on a high surrogate
		}
		out = append(out, u[:k])
		u = u[k:]
	}
	return out
}
