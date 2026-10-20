//go:build darwin

package computer

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"

	"github.com/ebitengine/purego"

	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// NativeDesktop is desktop tasks on macOS: the accessibility tree (ax), the window list and
// screencapture (screen), CGEvent input, the HID idle time, the session's lock state and
// pbcopy/pbpaste. apps opens an app through desktop_open's catalogue (nil: none).
func NativeDesktop(apps Apps) *Desktop {
	in := input.Native()
	if in == nil {
		return nil
	}
	mac := &screen.Mac{X: screen.OSExec{}}
	return &Desktop{
		AX: nativeAX{}, Windows: macWindows{}, Input: in, Monitor: input.NativeMonitor(),
		Capture: func(ctx context.Context, w DesktopWindow) (screen.Shot, error) {
			id, err := strconv.ParseUint(w.ID, 10, 32)
			if err != nil {
				return screen.Shot{}, err
			}
			return mac.CaptureWindow(ctx, screen.MacWindow{ID: uint32(id), Title: w.Title, App: w.App, PID: w.PID,
				X: float64(w.X), Y: float64(w.Y), W: float64(w.W), H: float64(w.H), OnScreen: true})
		},
		Locked:    screenLocked,
		Clipboard: NativeClipboard(),
		Apps:      apps,
	}
}

// macWindows is CGWindowListCopyWindowInfo's on-screen windows, front to back.
type macWindows struct{}

func (macWindows) List(context.Context) ([]DesktopWindow, error) {
	ws, err := screen.MacWindows()
	if err != nil {
		return nil, err
	}
	out := make([]DesktopWindow, 0, len(ws))
	for _, w := range ws {
		if !w.OnScreen {
			continue
		}
		out = append(out, DesktopWindow{ID: strconv.FormatUint(uint64(w.ID), 10), App: w.App, PID: w.PID, Title: w.Title,
			Layer: w.Layer, X: int(math.Round(w.X)), Y: int(math.Round(w.Y)), W: int(math.Round(w.W)), H: int(math.Round(w.H))})
	}
	return out, nil
}

// The session's lock state: CGSessionCopyCurrentDictionary's CGSSessionScreenIsLocked, and
// kCGSSessionOnConsoleKey (false while another user's session is in front). purego, no cgo.
var (
	lockOnce sync.Once
	lockErr  error

	cgSessionCopyCurrentDictionary func() uintptr
	lockDictGetValue               func(dict, key uintptr) uintptr
	lockStringCreate               func(alloc uintptr, s string, enc uint32) uintptr
	lockGetTypeID                  func(ref uintptr) uint
	lockBooleanTypeID              func() uint
	lockBooleanValue               func(b uintptr) bool
	lockRelease                    func(ref uintptr)

	keyScreenLocked, keyOnConsole uintptr
)

func loadLock() error {
	lockOnce.Do(func() {
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lockErr = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			lockErr = err
			return
		}
		for _, f := range []struct {
			lib  uintptr
			name string
			fn   any
		}{
			{cg, "CGSessionCopyCurrentDictionary", &cgSessionCopyCurrentDictionary},
			{cf, "CFDictionaryGetValue", &lockDictGetValue},
			{cf, "CFStringCreateWithCString", &lockStringCreate},
			{cf, "CFGetTypeID", &lockGetTypeID},
			{cf, "CFBooleanGetTypeID", &lockBooleanTypeID},
			{cf, "CFBooleanGetValue", &lockBooleanValue},
			{cf, "CFRelease", &lockRelease},
		} {
			if _, err := purego.Dlsym(f.lib, f.name); err != nil {
				lockErr = err
				return
			}
			purego.RegisterLibFunc(f.fn, f.lib, f.name)
		}
		const utf8 = 0x08000100
		keyScreenLocked = lockStringCreate(0, "CGSSessionScreenIsLocked", utf8)
		keyOnConsole = lockStringCreate(0, "kCGSSessionOnConsoleKey", utf8)
		if keyScreenLocked == 0 || keyOnConsole == 0 {
			lockErr = errors.New("CoreFoundation: could not create the session keys")
		}
	})
	return lockErr
}

// screenLocked reports whether the user can't see the screen: it is locked, or another user's
// session (or none) is in front.
func screenLocked(context.Context) (bool, error) {
	if err := loadLock(); err != nil {
		return false, err
	}
	d := cgSessionCopyCurrentDictionary()
	if d == 0 {
		return true, nil // no window server session
	}
	defer lockRelease(d)
	boolean := func(key uintptr) (v, ok bool) {
		ref := lockDictGetValue(d, key)
		if ref == 0 || lockGetTypeID(ref) != lockBooleanTypeID() {
			return false, false
		}
		return lockBooleanValue(ref), true
	}
	if locked, ok := boolean(keyScreenLocked); ok && locked {
		return true, nil
	}
	if on, ok := boolean(keyOnConsole); ok && !on {
		return true, nil
	}
	return false, nil
}
