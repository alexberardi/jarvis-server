//go:build darwin

package island

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
)

// The stop hotkey on macOS: Carbon's RegisterEventHotKey, which needs no Accessibility or
// Input Monitoring permission. Its presses arrive as Carbon events on the application's event
// target, which AppKit's event loop (runApp) dispatches.

const (
	hiToolbox           = "/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox"
	kEventHotKeyPressed = 5
)

var kEventClassKeyboard = fourCC("keyb")

type eventTypeSpec struct{ class, kind uint32 }

var (
	hotkeyOnce sync.Once
	hotkeyErr  error
	hotkeyEv   atomic.Pointer[AppEvents]

	getApplicationEventTarget func() uintptr
	installEventHandler       func(target, handler uintptr, n uint32, list *eventTypeSpec, data uintptr, out *uintptr) int32
	registerEventHotKey       func(code, mods uint32, id uint64, target uintptr, opts uint32, out *uintptr) int32
)

// registerStopHotkey makes ⌃⌥⎋ call ev.StopHotkey while the event loop runs. Main thread,
// once per process (a later call only changes where presses go).
func registerStopHotkey(ev AppEvents) error {
	hotkeyEv.Store(&ev)
	hotkeyOnce.Do(func() { hotkeyErr = installStopHotkey() })
	return hotkeyErr
}

func installStopHotkey() error {
	lib, err := purego.Dlopen(hiToolbox, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&getApplicationEventTarget, lib, "GetApplicationEventTarget")
	purego.RegisterLibFunc(&installEventHandler, lib, "InstallEventHandler")
	purego.RegisterLibFunc(&registerEventHotKey, lib, "RegisterEventHotKey")
	handler := purego.NewCallback(func(_, _, _ uintptr) uintptr {
		if ev := hotkeyEv.Load(); ev != nil {
			ev.hotkey()
		}
		return 0 // noErr
	})
	target := getApplicationEventTarget()
	spec := eventTypeSpec{kEventClassKeyboard, kEventHotKeyPressed}
	var ref uintptr
	if st := installEventHandler(target, handler, 1, &spec, 0, &ref); st != 0 {
		return fmt.Errorf("InstallEventHandler: OSStatus %d", st)
	}
	// EventHotKeyID{signature, id} by value: 8 bytes, one register, signature in the low half.
	id := uint64(hotkeySig) | 1<<32
	var hk uintptr
	if st := registerEventHotKey(stopKeyCode, stopModifiers, id, target, 0, &hk); st != 0 {
		return fmt.Errorf("RegisterEventHotKey: OSStatus %d (another app may hold %s)", st, StopHotkeyLabel)
	}
	return nil
}
