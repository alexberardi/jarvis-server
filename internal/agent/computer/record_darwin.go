//go:build darwin

package computer

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// macOS desktop recording: a listen-only CGEventTap (it sees the user's clicks, keys and
// scrolls but can't change or swallow them) on a run loop of its own thread, through purego
// (the build is CGO_ENABLED=0). The callback only decodes the event and queues it; AX lookups
// and keyframes happen on DesktopSource's worker. It needs the Input Monitoring grant (and
// Accessibility for the elements).

var (
	// ErrInputMonitoring: the event tap needs the Input Monitoring grant.
	ErrInputMonitoring = errors.New("to record the desktop, allow Jarvis Automation Agent in System Settings > " +
		"Privacy & Security > Input Monitoring (the setup page asks for it), then restart the agent")
	// ErrRecordAccessibility: without Accessibility a recording can't name what was clicked.
	ErrRecordAccessibility = errors.New("to record the desktop, allow Jarvis Automation Agent in System Settings > " +
		"Privacy & Security > Accessibility")
)

// NewDesktopSource is the recorder's desktop mode on macOS: keyframes from keyframe (nil: none)
// kept in dir.
func NewDesktopSource(dir string, keyframe func(context.Context) ([]byte, error), log *slog.Logger) Source {
	return &DesktopSource{Dir: dir, Tap: macTap, Log: log,
		Platform: DeskPlatform{ElementAt: ax.RecordedAt, Focused: ax.FocusedElement, Keyframe: keyframe}}
}

const (
	cgSessionEventTap = 1 // kCGSessionEventTap
	cgHeadInsert      = 0 // kCGHeadInsertEventTap
	cgListenOnly      = 1 // kCGEventTapOptionListenOnly

	evLeftMouseDown        = 1
	evRightMouseDown       = 3
	evKeyDown              = 10
	evScrollWheel          = 22
	evTapDisabledTimeout   = 0xFFFFFFFE // kCGEventTapDisabledByTimeout
	evTapDisabledUserInput = 0xFFFFFFFF // kCGEventTapDisabledByUserInput

	fieldKeycode     = 9  // kCGKeyboardEventKeycode
	fieldScrollAxis1 = 11 // kCGScrollWheelEventDeltaAxis1: vertical
	fieldScrollAxis2 = 12 // kCGScrollWheelEventDeltaAxis2: horizontal

	tapRunSlice = 0.25 // seconds per CFRunLoopRunInMode, between checks of stop
)

type tapPoint struct{ X, Y float64 }

var (
	tapOnce sync.Once
	tapErr  error

	cgEventTapCreate                func(tap, place, options uint32, mask uint64, cb, info uintptr) uintptr
	cgEventTapEnable                func(tap uintptr, enable bool)
	cgEventGetLocation              func(ev uintptr) tapPoint
	cgEventGetIntegerValueField     func(ev uintptr, field uint32) int64
	cgEventGetFlags                 func(ev uintptr) uint64
	cgEventKeyboardGetUnicodeString func(ev uintptr, max uintptr, actual *uintptr, buf *uint16)
	cfMachPortCreateRunLoopSource   func(alloc, port uintptr, order int) uintptr
	cfMachPortInvalidate            func(port uintptr)
	cfRunLoopGetCurrent             func() uintptr
	cfRunLoopAddSource              func(loop, src, mode uintptr)
	cfRunLoopRunInMode              func(mode uintptr, seconds float64, returnAfterSource bool) int32
	cfRunLoopStop                   func(loop uintptr)
	cfRelease                       func(ref uintptr)
	isSecureEventInputEnabled       func() bool

	runLoopCommonModes, runLoopDefaultMode uintptr
	tapCallback                            uintptr // one purego callback for the process

	curTap atomic.Pointer[tapState]
)

type tapState struct {
	send func(DeskInput)
	tap  atomic.Uintptr
	loop atomic.Uintptr
	stop atomic.Bool
}

func loadTap() error {
	tapOnce.Do(func() {
		cg, err := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			tapErr = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			tapErr = err
			return
		}
		hit, err := purego.Dlopen("/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			tapErr = err
			return
		}
		for _, f := range []struct {
			lib  uintptr
			name string
			fn   any
		}{
			{cg, "CGEventTapCreate", &cgEventTapCreate},
			{cg, "CGEventTapEnable", &cgEventTapEnable},
			{cg, "CGEventGetLocation", &cgEventGetLocation},
			{cg, "CGEventGetIntegerValueField", &cgEventGetIntegerValueField},
			{cg, "CGEventGetFlags", &cgEventGetFlags},
			{cg, "CGEventKeyboardGetUnicodeString", &cgEventKeyboardGetUnicodeString},
			{cf, "CFMachPortCreateRunLoopSource", &cfMachPortCreateRunLoopSource},
			{cf, "CFMachPortInvalidate", &cfMachPortInvalidate},
			{cf, "CFRunLoopGetCurrent", &cfRunLoopGetCurrent},
			{cf, "CFRunLoopAddSource", &cfRunLoopAddSource},
			{cf, "CFRunLoopRunInMode", &cfRunLoopRunInMode},
			{cf, "CFRunLoopStop", &cfRunLoopStop},
			{cf, "CFRelease", &cfRelease},
			{hit, "IsSecureEventInputEnabled", &isSecureEventInputEnabled},
		} {
			if _, err := purego.Dlsym(f.lib, f.name); err != nil {
				tapErr = err
				return
			}
			purego.RegisterLibFunc(f.fn, f.lib, f.name)
		}
		for name, dst := range map[string]*uintptr{"kCFRunLoopCommonModes": &runLoopCommonModes, "kCFRunLoopDefaultMode": &runLoopDefaultMode} {
			sym, err := purego.Dlsym(cf, name)
			if err != nil {
				tapErr = err
				return
			}
			*dst = *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), sym)) // a C global holding a CFStringRef
		}
		tapCallback = purego.NewCallback(onTapEvent)
	})
	return tapErr
}

// macTap is the DeskTap: it starts the tap on a new locked thread and stops it there.
func macTap(send func(DeskInput)) (func(), error) {
	if err := loadTap(); err != nil {
		return nil, err
	}
	if !macax.ListenTrusted() {
		return nil, ErrInputMonitoring
	}
	if !macax.Trusted() {
		return nil, ErrRecordAccessibility
	}
	st := &tapState{send: send}
	if !curTap.CompareAndSwap(nil, st) {
		return nil, ErrBusy
	}
	ready, exited := make(chan error, 1), make(chan struct{})
	go func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		defer close(exited)
		mask := uint64(1<<evLeftMouseDown | 1<<evRightMouseDown | 1<<evKeyDown | 1<<evScrollWheel)
		tap := cgEventTapCreate(cgSessionEventTap, cgHeadInsert, cgListenOnly, mask, tapCallback, 0)
		if tap == 0 {
			ready <- ErrInputMonitoring // what refuses a listen-only tap
			return
		}
		src := cfMachPortCreateRunLoopSource(0, tap, 0)
		loop := cfRunLoopGetCurrent()
		cfRunLoopAddSource(loop, src, runLoopCommonModes)
		st.tap.Store(tap)
		st.loop.Store(loop)
		cgEventTapEnable(tap, true)
		ready <- nil
		for !st.stop.Load() {
			cfRunLoopRunInMode(runLoopDefaultMode, tapRunSlice, false)
		}
		cgEventTapEnable(tap, false)
		cfMachPortInvalidate(tap)
		cfRelease(src)
		cfRelease(tap)
	}()
	if err := <-ready; err != nil {
		<-exited
		curTap.CompareAndSwap(st, nil)
		return nil, err
	}
	return func() {
		st.stop.Store(true)
		cfRunLoopStop(st.loop.Load())
		<-exited // the callback runs only on that thread: no send after this
		curTap.CompareAndSwap(st, nil)
	}, nil
}

// onTapEvent is the tap's callback, on the tap's thread. A listen-only tap's return value is
// ignored; the event goes back unchanged anyway.
func onTapEvent(_, typ, ev, _ uintptr) uintptr {
	st := curTap.Load()
	if st == nil || st.stop.Load() {
		return ev
	}
	now := time.Now()
	switch uint32(typ) {
	case evTapDisabledTimeout, evTapDisabledUserInput:
		if t := st.tap.Load(); t != 0 {
			cgEventTapEnable(t, true)
		}
	case evLeftMouseDown, evRightMouseDown:
		p := cgEventGetLocation(ev)
		btn := "left"
		if uint32(typ) == evRightMouseDown {
			btn = "right"
		}
		st.send(DeskInput{Kind: InClick, At: now, X: p.X, Y: p.Y, Button: btn})
	case evScrollWheel:
		p := cgEventGetLocation(ev)
		st.send(DeskInput{Kind: InScroll, At: now, X: p.X, Y: p.Y,
			DY: int(cgEventGetIntegerValueField(ev, fieldScrollAxis1)), DX: int(cgEventGetIntegerValueField(ev, fieldScrollAxis2))})
	case evKeyDown:
		code := uint16(cgEventGetIntegerValueField(ev, fieldKeycode))
		st.send(macKeyInput(code, cgEventGetFlags(ev), isSecureEventInputEnabled(), now, func() string { return keyText(ev) }))
	}
	return ev
}

// keyText is the characters a key down types.
func keyText(ev uintptr) string {
	var buf [8]uint16
	var n uintptr
	cgEventKeyboardGetUnicodeString(ev, uintptr(len(buf)), &n, &buf[0])
	if n == 0 || n > uintptr(len(buf)) {
		return ""
	}
	return string(utf16.Decode(buf[:n]))
}
