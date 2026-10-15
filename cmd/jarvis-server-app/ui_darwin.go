//go:build darwin

package main

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The dialogs are NSAlerts, over purego like the agent's island and tray (CGO_ENABLED=0). AppKit
// runs on the main thread only: main's goroutine stays on it, and nothing here starts another.

func init() { runtime.LockOSThread() }

const (
	alertFirstButton      = 1000 // NSAlertFirstButtonReturn
	controlStateOn        = 1    // NSControlStateValueOn
	activationPolicyReg   = 0    // NSApplicationActivationPolicyRegular
	modifierFlagOption    = 1 << 19
	alertStyleInformation = 1 // NSAlertStyleInformational
	windowStyleTitled     = 1 // NSWindowStyleMaskTitled: no close button, the work can't be stopped
	backingBuffered       = 2 // NSBackingStoreBuffered
	progressStyleSpinning = 1 // NSProgressIndicatorStyleSpinning
)

// The AppKit geometry structs on 64-bit Apple platforms (CGFloat = float64).
type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	Origin nsPoint
	Size   nsSize
}

func rect(x, y, w, h float64) nsRect { return nsRect{nsPoint{x, y}, nsSize{w, h}} }

func loadAppKit() error {
	for _, fw := range []string{"Foundation", "AppKit"} {
		if _, err := purego.Dlopen("/System/Library/Frameworks/"+fw+".framework/"+fw, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
			return fmt.Errorf("load %s: %w", fw, err)
		}
	}
	if objc.GetClass("NSAlert") == 0 {
		return errors.New("AppKit has no NSAlert")
	}
	return nil
}

func class(name string) objc.ID { return objc.ID(objc.GetClass(name)) }

func send(id objc.ID, sel string, args ...any) objc.ID {
	return id.Send(objc.RegisterName(sel), args...)
}

func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	str := objc.Send[objc.ID](class("NSString"), objc.RegisterName("stringWithUTF8String:"), unsafe.Pointer(&b[0]))
	runtime.KeepAlive(b)
	return str
}

type nativeUI struct{}

func nativeDialogs() (dialogs, error) {
	if err := loadAppKit(); err != nil {
		return nil, err
	}
	app := send(class("NSApplication"), "sharedApplication")
	send(app, "setActivationPolicy:", activationPolicyReg)
	send(app, "activateIgnoringOtherApps:", true)
	return nativeUI{}, nil
}

// optionKeyDown: the Option key is held as the app opens.
func optionKeyDown() bool {
	if loadAppKit() != nil {
		return false
	}
	return objc.Send[uint](class("NSEvent"), objc.RegisterName("modifierFlags"))&modifierFlagOption != 0
}

// newAlert is an alert with its buttons, the first the default (Return); a "Cancel" button
// also answers Escape (AppKit does that by title).
func newAlert(title, text string, buttons ...string) objc.ID {
	a := send(send(class("NSAlert"), "alloc"), "init")
	send(a, "setAlertStyle:", alertStyleInformation)
	send(a, "setMessageText:", nsString(title))
	send(a, "setInformativeText:", nsString(text))
	for _, b := range buttons {
		send(a, "addButtonWithTitle:", nsString(b))
	}
	return a
}

// newCheckbox is a ticked checkbox for an alert's accessory view.
func newCheckbox(title string) objc.ID {
	cb := class("NSButton").Send(objc.RegisterName("checkboxWithTitle:target:action:"), nsString(title), objc.ID(0), objc.SEL(0))
	send(cb, "setState:", controlStateOn)
	return cb
}

func runAlert(alert objc.ID) int {
	send(send(class("NSApplication"), "sharedApplication"), "activateIgnoringOtherApps:", true)
	return objc.Send[int](alert, objc.RegisterName("runModal")) - alertFirstButton
}

func (nativeUI) confirmInstall(title, text, checkbox string) (bool, bool) {
	a := newAlert(title, text, btnInstall, btnCancel)
	cb := newCheckbox(checkbox)
	send(a, "setAccessoryView:", cb)
	if runAlert(a) != 0 {
		return false, false
	}
	return true, objc.Send[int](cb, objc.RegisterName("state")) == controlStateOn
}

func (nativeUI) choose(title, text string, buttons ...string) int {
	return runAlert(newAlert(title, text, buttons...))
}

// newBusyWindow is the progress window: a spinner, a bold title, a line of text. It isn't
// shown yet.
func newBusyWindow(title, text string) (win, spinner objc.ID) {
	win = objc.Send[objc.ID](send(class("NSPanel"), "alloc"), objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
		rect(0, 0, 400, 100), uint64(windowStyleTitled), uint64(backingBuffered), false)
	send(win, "setReleasedWhenClosed:", false)
	send(win, "setTitle:", nsString(productName))
	view := send(win, "contentView")

	spinner = objc.Send[objc.ID](send(class("NSProgressIndicator"), "alloc"), objc.RegisterName("initWithFrame:"), rect(20, 34, 32, 32))
	send(spinner, "setStyle:", progressStyleSpinning)
	send(spinner, "setIndeterminate:", true)
	// Animated off the main thread too, so it keeps turning between the events pumped below.
	send(spinner, "setUsesThreadedAnimation:", true)
	send(view, "addSubview:", spinner)

	head := send(class("NSTextField"), "labelWithString:", nsString(title))
	send(head, "setFont:", objc.Send[objc.ID](class("NSFont"), objc.RegisterName("boldSystemFontOfSize:"), 13.0))
	objc.Send[objc.ID](head, objc.RegisterName("setFrame:"), rect(68, 58, 312, 20))
	send(view, "addSubview:", head)

	body := send(class("NSTextField"), "wrappingLabelWithString:", nsString(text))
	send(body, "setFont:", objc.Send[objc.ID](class("NSFont"), objc.RegisterName("systemFontOfSize:"), 12.0))
	objc.Send[objc.ID](body, objc.RegisterName("setFrame:"), rect(68, 16, 312, 38))
	send(view, "addSubview:", body)

	send(win, "center")
	return win, spinner
}

// busy pumps AppKit's events (so the window draws and macOS never calls the app not
// responding) until done, showing the progress window from when started closes.
func (nativeUI) busy(title, text string, started, done <-chan struct{}) {
	app := send(class("NSApplication"), "sharedApplication")
	mode := nsString("kCFRunLoopDefaultMode") // NSDefaultRunLoopMode
	var win, spinner objc.ID
	for {
		select {
		case <-done:
			if win != 0 {
				send(spinner, "stopAnimation:", objc.ID(0))
				send(win, "orderOut:", objc.ID(0))
			}
			return
		default:
		}
		if win == 0 {
			select {
			case <-started:
				win, spinner = newBusyWindow(title, text)
				send(spinner, "startAnimation:", objc.ID(0))
				send(app, "activateIgnoringOtherApps:", true)
				send(win, "makeKeyAndOrderFront:", objc.ID(0))
			default:
			}
		}
		// Wakes for an event or after a tenth of a second, to look at the channels again.
		until := objc.Send[objc.ID](class("NSDate"), objc.RegisterName("dateWithTimeIntervalSinceNow:"), 0.1)
		ev := objc.Send[objc.ID](app, objc.RegisterName("nextEventMatchingMask:untilDate:inMode:dequeue:"),
			uint64(math.MaxUint64), until, mode, true)
		if ev != 0 {
			send(app, "sendEvent:", ev)
		}
	}
}

// checkDialogs builds every kind of dialog without showing it, and checks what AppKit made of
// it: the selectors exist on this macOS, the checkbox starts ticked, the buttons are there.
func checkDialogs() error {
	if err := loadAppKit(); err != nil {
		return err
	}
	send(class("NSApplication"), "sharedApplication")
	a := newAlert(productName, "check", btnInstall, btnCancel)
	cb := newCheckbox("Let devices on my network reach it")
	send(a, "setAccessoryView:", cb)
	send(a, "layout")
	if s := objc.Send[int](cb, objc.RegisterName("state")); s != controlStateOn {
		return fmt.Errorf("the firewall checkbox starts in state %d, not ticked", s)
	}
	buttons := send(a, "buttons")
	if n := objc.Send[int](buttons, objc.RegisterName("count")); n != 2 {
		return fmt.Errorf("the install dialog has %d buttons, not 2", n)
	}
	b := send(newAlert(productName, "check", btnOpen, btnUninstall, btnCancel), "buttons")
	if n := objc.Send[int](b, objc.RegisterName("count")); n != 3 {
		return fmt.Errorf("the menu dialog has %d buttons, not 3", n)
	}
	win, spinner := newBusyWindow("Installing "+productName+"…", "check")
	if objc.Send[int](spinner, objc.RegisterName("style")) != progressStyleSpinning {
		return errors.New("the progress window's spinner isn't spinning-style")
	}
	if n := objc.Send[int](send(send(win, "contentView"), "subviews"), objc.RegisterName("count")); n != 3 {
		return fmt.Errorf("the progress window has %d views, not 3", n)
	}
	_ = optionKeyDown()
	fmt.Println("dialogs ok")
	return nil
}
