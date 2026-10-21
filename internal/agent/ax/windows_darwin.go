//go:build darwin

package ax

import (
	"runtime"

	"github.com/ebitengine/purego/objc"
)

func trusted() bool { return load() == nil && axIsProcessTrusted() }

// runningApps reads NSWorkspace's runningApplications and keeps the regular ones.
func runningApps() ([]App, error) {
	if err := ready(); err != nil {
		return nil, err
	}
	if !appKit() {
		return nil, &Error{Code: CodeUnsupported, Msg: "AppKit did not load"}
	}
	// Autoreleased objects: drain a pool on the thread that pushed it (as bundleID does).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := poolPush()
	defer poolPop(pool)
	ws := workspace.Send(objc.RegisterName("sharedWorkspace"))
	if ws == 0 {
		return nil, &Error{Code: CodeUnsupported, Msg: "no shared NSWorkspace"}
	}
	arr := ws.Send(objc.RegisterName("runningApplications"))
	n := objc.Send[uint](arr, objc.RegisterName("count"))
	out := make([]App, 0, n)
	for i := range n {
		a := arr.Send(objc.RegisterName("objectAtIndex:"), i)
		// NSApplicationActivationPolicyRegular is 0; accessory and prohibited apps have no windows
		// worth listing (System Events' "background only is false" kept the same set).
		if a == 0 || objc.Send[int](a, objc.RegisterName("activationPolicy")) != 0 {
			continue
		}
		pid := objc.Send[int32](a, objc.RegisterName("processIdentifier"))
		if pid <= 0 {
			continue
		}
		name := ""
		if s := a.Send(objc.RegisterName("localizedName")); s != 0 {
			name = goString(uintptr(s)) // NSString is toll-free bridged to CFString
		}
		out = append(out, App{PID: int(pid), Name: name})
	}
	return out, nil
}

// appElem is the app element for pid, with its own short messaging timeout: the global one
// (messagingTimeout) is what an unresponsive app would otherwise hold each call for.
func appElem(pid int) (elem, error) {
	app := elem(axCreateApplication(int32(pid)))
	if app == 0 {
		return 0, ErrNoWindow
	}
	axSetMessagingTimeout(uintptr(app), AppTimeout)
	return app, nil
}

// windowsOf returns app's AXWindows array (owned; 0 with no windows).
func windowsOf(app elem) (uintptr, error) {
	arr, code := app.copyAttr("AXWindows")
	switch {
	case arr != 0:
		return arr, nil
	case code == axErrorNoValue || code == axErrorAttributeUnsupport:
		return 0, nil
	case code == axErrorInvalidUIElement:
		return 0, ErrNoWindow // the app has quit
	}
	return 0, axErr(code, "AXWindows")
}

func appWindows(pid int) ([]AppWindow, error) {
	if err := ready(); err != nil {
		return nil, err
	}
	app, err := appElem(pid)
	if err != nil {
		return nil, err
	}
	defer app.release()
	arr, err := windowsOf(app)
	if err != nil || arr == 0 {
		return []AppWindow{}, err
	}
	defer cfRelease(arr)
	n := cfArrayGetCount(arr)
	out := make([]AppWindow, 0, n)
	for i := range n {
		w := elem(cfArrayGetValueAtIndex(arr, i)) // the array keeps it
		axSetMessagingTimeout(uintptr(w), AppTimeout)
		title := ""
		v, code := w.copyAttr("AXTitle")
		switch {
		case code == axErrorCannotComplete:
			// It stopped answering mid-list: don't wait out the rest of its windows one by one.
			return nil, axErr(code, "AXTitle")
		case v != 0:
			if cfGetTypeID(v) == cfStringGetTypeID() {
				title = goString(v)
			}
			cfRelease(v)
		}
		out = append(out, AppWindow{Title: title, Minimized: w.attr("AXMinimized") == "true"})
	}
	return out, nil
}

func windowAct(pid, index int, action string) error {
	if err := ready(); err != nil {
		return err
	}
	app, err := appElem(pid)
	if err != nil {
		return err
	}
	defer app.release()
	arr, err := windowsOf(app)
	if err != nil {
		return err
	}
	if arr == 0 {
		return ErrNoWindow
	}
	defer cfRelease(arr)
	if index < 1 || index > cfArrayGetCount(arr) {
		return ErrNoWindow
	}
	w := elem(cfArrayGetValueAtIndex(arr, index-1))
	axSetMessagingTimeout(uintptr(w), AppTimeout)
	switch action {
	case ActMinimize:
		return axErr(axSetAttributeValue(uintptr(w), cfstr("AXMinimized"), kCFBooleanTrue), "AXMinimized")
	case ActFocus:
		// What System Events' "perform action AXRaise" plus "set frontmost" did.
		if err := axErr(axPerformAction(uintptr(w), cfstr("AXRaise")), "AXRaise"); err != nil {
			return err
		}
		return axErr(axSetAttributeValue(uintptr(app), cfstr("AXFrontmost"), kCFBooleanTrue), "AXFrontmost")
	case ActClose:
		b, code := w.copyAttr("AXCloseButton")
		if b == 0 {
			return axErr(code, "AXCloseButton")
		}
		btn := elem(b)
		defer btn.release()
		return btn.press()
	}
	return &Error{Code: CodeUnsupported, Msg: "unknown window action " + action}
}
