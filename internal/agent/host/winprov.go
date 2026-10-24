package host

import (
	"context"
	"math"
	"strconv"
)

// Win32 is the slice of the Windows API the Windows backend uses. The real implementation
// (win32_windows.go) is pure Go over syscalls, so the binary stays CGO_ENABLED=0; on other
// OSes NewWin32 returns a stub that reports nothing available. Tests inject a fake.
type Win32 interface {
	// AudioAvailable probes the default render endpoint (a headless VM, a server or some RDP
	// sessions have the API and no playback device).
	AudioAvailable() bool
	VolumeScalar() (float32, error)
	SetVolumeScalar(v float32) error
	// DesktopAvailable reports an interactive window station (false in Session 0).
	DesktopAvailable() bool
	EnumWindows() ([]Window, error) // visible, titled top-level windows; ID = decimal HWND
	IsWindow(hwnd uintptr) bool
	Minimize(hwnd uintptr) error
	Focus(hwnd uintptr) error
	PostClose(hwnd uintptr) error  // WM_CLOSE: a request the app may answer with a save prompt
	ShellOpen(target string) error // ShellExecuteW "open": returns once the shell accepted it
	Maximize(hwnd uintptr) error   // ShowWindow SW_MAXIMIZE
	Restore(hwnd uintptr) error    // ShowWindow SW_RESTORE: out of minimised or maximised
	IsZoomed(hwnd uintptr) bool
	IsIconic(hwnd uintptr) bool
	// WorkAreas are the monitors' work areas (minus the taskbar), left to right.
	WorkAreas() ([]Rect, error)
	WindowRect(hwnd uintptr) (Rect, error)
	SetRect(hwnd uintptr, r Rect) error // SetWindowPos, no z-order change, no activation
}

// Rect is a screen rectangle in Windows' coordinates (right and bottom exclusive).
type Rect struct{ L, T, R, B int }

func (r Rect) w() int { return r.R - r.L }
func (r Rect) h() int { return r.B - r.T }
func (r Rect) contains(x, y int) bool {
	return x >= r.L && x < r.R && y >= r.T && y < r.B
}

// Windows is the Windows backend: WASAPI for volume, user32 for windows, ShellExecute for open.
// Not named *_windows.go: it compiles everywhere so the contract suite can run it everywhere.
type Windows struct {
	API Win32
}

// NewWindows builds the Windows backend. It touches nothing.
func NewWindows(api Win32) *Windows { return &Windows{API: api} }

func (*Windows) Platform() string { return "windows" }

func (w *Windows) Capabilities(context.Context) []string {
	var caps []string
	if w.API.DesktopAvailable() {
		caps = append(caps, CapOpen)
		caps = append(caps, AllWindowCaps...)
		// Native only (Q4): ShowWindow's states and SetWindowPos geometry. No fullscreen (apps
		// do their own) and no workspaces (IVirtualDesktopManager can't list desktops).
		caps = append(caps, CapWindowsUnminimize, CapWindowsMaximize, CapWindowsUnmaximize, CapWindowsSnap)
		if areas, err := w.API.WorkAreas(); err == nil && len(areas) > 1 {
			caps = append(caps, CapWindowsMonitor)
		}
	}
	if w.API.AudioAvailable() {
		caps = append(caps, CapVolumeGet, CapVolumeSet)
	}
	return caps
}

func (w *Windows) GetVolume(context.Context) (int, error) {
	v, err := w.API.VolumeScalar()
	if err != nil {
		return 0, Errorf(ErrOperationFailed, "%v", err)
	}
	return int(math.Round(float64(v) * 100)), nil
}

func (w *Windows) ApplyVolume(_ context.Context, level int) error {
	if err := w.API.SetVolumeScalar(float32(level) / 100); err != nil {
		return Errorf(ErrOperationFailed, "%v", err)
	}
	return nil
}

func (w *Windows) ListWindows(context.Context) ([]Window, error) {
	ws, err := w.API.EnumWindows()
	if err != nil {
		return nil, Errorf(ErrOperationFailed, "%v", err)
	}
	if ws == nil {
		ws = []Window{}
	}
	return ws, nil
}

func (w *Windows) hwnd(id string) (uintptr, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 {
		return 0, Errorf(ErrWindowNotFound, "not a window handle: %q", id)
	}
	h := uintptr(n)
	if !w.API.IsWindow(h) {
		return 0, Errorf(ErrWindowNotFound, "window %s no longer exists", id)
	}
	return h, nil
}

func (w *Windows) do(id string, f func(uintptr) error) error {
	h, err := w.hwnd(id)
	if err != nil {
		return err
	}
	if err := f(h); err != nil {
		return Errorf(ErrOperationFailed, "%v", err)
	}
	return nil
}

func (w *Windows) MinimizeWindow(_ context.Context, id string) error { return w.do(id, w.API.Minimize) }
func (w *Windows) FocusWindow(_ context.Context, id string) error    { return w.do(id, w.API.Focus) }

// CloseWindow posts WM_CLOSE and never TerminateProcess: a misheard command must not be able
// to lose someone's unsaved work.
func (w *Windows) CloseWindow(_ context.Context, id string) error { return w.do(id, w.API.PostClose) }

func (w *Windows) Open(_ context.Context, target string) error {
	if err := w.API.ShellOpen(target); err != nil {
		return Errorf(ErrOperationFailed, "could not open %q: %v", target, err)
	}
	return nil
}

// LaunchApp opens a Start Menu shortcut through the shell, exactly as a click would.
func (w *Windows) LaunchApp(_ context.Context, path string) error {
	if err := w.API.ShellOpen(path); err != nil {
		return Errorf(ErrOperationFailed, "could not launch %q: %v", path, err)
	}
	return nil
}

// ArrangeWindow: ShowWindow for the states, SetWindowPos into the monitors' work areas for snap
// and moving to another monitor (the geometry Windows' own snap sets; snap assist just won't
// offer the other half).
func (w *Windows) ArrangeWindow(_ context.Context, id string, a WindowAction, target string) error {
	h, err := w.hwnd(id)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		if err != nil {
			return Errorf(ErrOperationFailed, "%v", err)
		}
		return nil
	}
	switch a {
	case ActionMaximize:
		return fail(w.API.Maximize(h))
	case ActionUnminimize:
		if !w.API.IsIconic(h) {
			return nil
		}
		return fail(w.API.Restore(h))
	case ActionUnmaximize:
		if !w.API.IsZoomed(h) {
			return nil
		}
		return fail(w.API.Restore(h))
	case ActionSnapLeft, ActionSnapRight, ActionMoveToMonitor:
	default:
		return Errorf(ErrNotSupported, "%s is not available on Windows", a)
	}
	areas, err := w.API.WorkAreas()
	if err != nil || len(areas) == 0 {
		return Errorf(ErrOperationFailed, "no monitor work area: %v", err)
	}
	if w.API.IsZoomed(h) || w.API.IsIconic(h) {
		if err := w.API.Restore(h); err != nil {
			return fail(err)
		}
	}
	r, err := w.API.WindowRect(h)
	if err != nil {
		return fail(err)
	}
	cur := 0
	for i, area := range areas {
		if area.contains(r.L+r.w()/2, r.T+r.h()/2) {
			cur = i
			break
		}
	}
	if a != ActionMoveToMonitor {
		area := areas[cur]
		half := area.w() / 2
		x := area.L
		if a == ActionSnapRight {
			x += half
		}
		return fail(w.API.SetRect(h, Rect{L: x, T: area.T, R: x + half, B: area.B}))
	}
	dst := cur
	switch target {
	case "next":
		dst = (cur + 1) % len(areas)
	case "previous":
		dst = (cur + len(areas) - 1) % len(areas)
	default:
		n, err := strconv.Atoi(target)
		if err != nil {
			return Errorf(ErrInvalidTarget, "on Windows a monitor is next, previous or its number (got %q)", target)
		}
		if n > len(areas) {
			return Errorf(ErrInvalidTarget, "there is no monitor %d (%d connected)", n, len(areas))
		}
		dst = n - 1
	}
	src, to := areas[cur], areas[dst]
	// Same place relative to the work area, shrunk to fit.
	nw, nh := min(r.w(), to.w()), min(r.h(), to.h())
	x := min(to.L+(r.L-src.L), to.R-nw)
	y := min(to.T+(r.T-src.T), to.B-nh)
	return fail(w.API.SetRect(h, Rect{L: max(x, to.L), T: max(y, to.T), R: max(x, to.L) + nw, B: max(y, to.T) + nh}))
}
