//go:build darwin

package input

import (
	"errors"
	"sync"
	"time"

	"github.com/ebitengine/purego"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// Native is the CGEvent backend.
func Native() Input { return &Mac{Trusted: macax.Trusted, Post: cgPoster{dictate.NativeMacPoster()}} }

// cgPoster posts pointer and key events with CoreGraphics over purego (no cgo); Unicode text
// goes through dictate's poster.
type cgPoster struct{ dictate.MacPoster }

const (
	cgFramework = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	cfFramework = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cgHIDTap    = 0 // kCGHIDEventTap
	cgUnitLine  = 1 // kCGScrollEventUnitLine
	cgClicks    = 1 // kCGMouseEventClickState
	// cgEventGap paces events as dictate does: a burst faster than an app's event loop drops some.
	cgEventGap = 2 * time.Millisecond
)

// cgSource is the CGEventSourceRef every event is created with (0: the default source).
var cgSource uintptr

type cgPoint struct{ X, Y float64 }

var (
	cgOnce sync.Once
	cgErr  error

	cgEventCreate                  func(source uintptr) uintptr
	cgEventCreateMouseEvent        func(source uintptr, typ uint32, pt cgPoint, button uint32) uintptr
	cgEventCreateScrollWheelEvent2 func(source uintptr, units uint32, count uint32, w1, w2, w3 int32) uintptr
	cgEventCreateKeyboardEvent     func(source uintptr, key uint16, down bool) uintptr
	cgEventSetIntegerValueField    func(ev uintptr, field uint32, v int64)
	cgEventSetFlags                func(ev uintptr, flags uint64)
	cgEventGetLocation             func(ev uintptr) cgPoint
	cgEventPost                    func(tap uint32, ev uintptr)
	cfRelease                      func(ref uintptr)
)

func loadCG() error {
	cgOnce.Do(func() {
		cg, err := purego.Dlopen(cgFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cgErr = err
			return
		}
		cf, err := purego.Dlopen(cfFramework, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cgErr = err
			return
		}
		purego.RegisterLibFunc(&cgEventCreate, cg, "CGEventCreate")
		purego.RegisterLibFunc(&cgEventCreateMouseEvent, cg, "CGEventCreateMouseEvent")
		purego.RegisterLibFunc(&cgEventCreateScrollWheelEvent2, cg, "CGEventCreateScrollWheelEvent2")
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventSetIntegerValueField, cg, "CGEventSetIntegerValueField")
		purego.RegisterLibFunc(&cgEventSetFlags, cg, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventGetLocation, cg, "CGEventGetLocation")
		purego.RegisterLibFunc(&cgEventPost, cg, "CGEventPost")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
	})
	return cgErr
}

var errNoEvent = errors.New("CGEventCreate failed")

// post posts and releases ev, then waits the gap.
func post(ev uintptr) error {
	if ev == 0 {
		return errNoEvent
	}
	cgEventPost(cgHIDTap, ev)
	cfRelease(ev)
	time.Sleep(cgEventGap)
	return nil
}

func (cgPoster) Mouse(typ uint32, x, y float64, button uint32, clicks int64) error {
	if err := loadCG(); err != nil {
		return err
	}
	ev := cgEventCreateMouseEvent(cgSource, typ, cgPoint{x, y}, button)
	if ev != 0 && clicks > 0 {
		cgEventSetIntegerValueField(ev, cgClicks, clicks)
	}
	return post(ev)
}

func (cgPoster) Wheel(wheel1, wheel2 int32) error {
	if err := loadCG(); err != nil {
		return err
	}
	return post(cgEventCreateScrollWheelEvent2(cgSource, cgUnitLine, 2, wheel1, wheel2, 0))
}

func (cgPoster) KeyEvent(code uint16, down bool, flags uint64) error {
	if err := loadCG(); err != nil {
		return err
	}
	ev := cgEventCreateKeyboardEvent(cgSource, code, down)
	if ev != 0 {
		cgEventSetFlags(ev, flags) // also clears modifiers the user holds
	}
	return post(ev)
}

func (cgPoster) Location() (x, y float64, err error) {
	if err := loadCG(); err != nil {
		return 0, 0, err
	}
	ev := cgEventCreate(cgSource)
	if ev == 0 {
		return 0, 0, errNoEvent
	}
	p := cgEventGetLocation(ev)
	cfRelease(ev)
	return p.X, p.Y, nil
}
