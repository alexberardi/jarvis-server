//go:build darwin

package dictate

import (
	"errors"
	"sync"
	"time"

	"github.com/ebitengine/purego"
)

// cgPoster posts keyboard events with CoreGraphics over purego (no cgo).
type cgPoster struct{}

const (
	cgFramework   = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	cfFramework   = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	cgHIDEventTap = 0       // kCGHIDEventTap
	cgFlagShift   = 0x20000 // kCGEventFlagMaskShift
	// cgEventGap paces events: a burst faster than an app's event loop can drop characters.
	cgEventGap = 2 * time.Millisecond
)

var (
	cgOnce sync.Once
	cgErr  error

	cgEventCreateKeyboardEvent      func(source uintptr, key uint16, down bool) uintptr
	cgEventKeyboardSetUnicodeString func(ev uintptr, n uint64, s *uint16)
	cgEventSetFlags                 func(ev uintptr, flags uint64)
	cgEventPost                     func(tap uint32, ev uintptr)
	cgRelease                       func(ref uintptr)
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
		purego.RegisterLibFunc(&cgEventCreateKeyboardEvent, cg, "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&cgEventKeyboardSetUnicodeString, cg, "CGEventKeyboardSetUnicodeString")
		purego.RegisterLibFunc(&cgEventSetFlags, cg, "CGEventSetFlags")
		purego.RegisterLibFunc(&cgEventPost, cg, "CGEventPost")
		purego.RegisterLibFunc(&cgRelease, cf, "CFRelease")
	})
	return cgErr
}

var errNoEvent = errors.New("CGEventCreateKeyboardEvent failed")

func (cgPoster) post(key uint16, down bool, units []uint16, flags uint64) error {
	ev := cgEventCreateKeyboardEvent(0, key, down)
	if ev == 0 {
		return errNoEvent
	}
	if len(units) > 0 {
		cgEventKeyboardSetUnicodeString(ev, uint64(len(units)), &units[0])
	}
	if flags != 0 {
		cgEventSetFlags(ev, flags)
	}
	cgEventPost(cgHIDEventTap, ev)
	cgRelease(ev)
	time.Sleep(cgEventGap)
	return nil
}

func (p cgPoster) Unicode(units []uint16) error {
	if err := loadCG(); err != nil {
		return err
	}
	if err := p.post(0, true, units, 0); err != nil {
		return err
	}
	return p.post(0, false, units, 0)
}

func (p cgPoster) Key(code uint16, shift bool) error {
	if err := loadCG(); err != nil {
		return err
	}
	var flags uint64
	if shift {
		flags = cgFlagShift
	}
	if err := p.post(code, true, nil, flags); err != nil {
		return err
	}
	return p.post(code, false, nil, flags)
}

func nativeMacPoster() MacPoster { return cgPoster{} }
