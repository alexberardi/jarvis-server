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

// ArrangeWindow: not on Windows yet (DESKTOP-COMMANDS.md L1: ShowWindow and SetWindowPos come
// later), and never advertised, so Host never calls it.
func (w *Windows) ArrangeWindow(context.Context, string, WindowAction, string) error {
	return Errorf(ErrNotSupported, "arranging windows is not available on Windows yet")
}
