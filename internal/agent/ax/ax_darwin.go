//go:build darwin

package ax

import (
	"strconv"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Bindings to ApplicationServices (AXUIElement*) and CoreFoundation through purego, so the build
// stays CGO_ENABLED=0. Release discipline: every Create*/Copy* result is CFReleased by whoever
// receives it; an elem owns one retain, which release gives back.

const (
	axPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	cfPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"

	utf8 = 0x08000100 // kCFStringEncodingUTF8

	kAXValueCGPointType = 1
	kAXValueCGSizeType  = 2
	kCFNumberFloat64    = 6 // kCFNumberFloat64Type

	axSuccess                 = 0
	axErrorInvalidUIElement   = -25202
	axErrorCannotComplete     = -25204
	axErrorAttributeUnsupport = -25205
	axErrorActionUnsupported  = -25206
	axErrorAPIDisabled        = -25211
	axErrorNoValue            = -25212

	messagingTimeout = 2 // seconds an unresponsive app may hold a call
)

var (
	once    sync.Once
	loadErr error

	axIsProcessTrusted        func() bool
	axCreateApplication       func(pid int32) uintptr
	axCreateSystemWide        func() uintptr
	axCopyAttributeValue      func(el, attr uintptr, out *uintptr) int32
	axCopyAttributeNames      func(el uintptr, out *uintptr) int32
	axPerformAction           func(el, action uintptr) int32
	axSetAttributeValue       func(el, attr, value uintptr) int32
	axCopyElementAtPosition   func(app uintptr, x, y float32, out *uintptr) int32
	axSetMessagingTimeout     func(el uintptr, seconds float32) int32
	axValueGetValue           func(v uintptr, typ int32, out unsafe.Pointer) bool
	axValueGetTypeID          func() uintptr
	cfGetTypeID               func(ref uintptr) uintptr
	cfStringGetTypeID         func() uintptr
	cfNumberGetTypeID         func() uintptr
	cfBooleanGetTypeID        func() uintptr
	cfArrayGetCount           func(a uintptr) int
	cfArrayGetValueAtIndex    func(a uintptr, i int) uintptr
	cfStringCreateWithCString func(alloc uintptr, s string, enc uint32) uintptr
	cfStringGetLength         func(s uintptr) int
	cfStringGetMaxSize        func(length int, enc uint32) int
	cfStringGetCString        func(s uintptr, buf *byte, size int, enc uint32) bool
	cfNumberGetValue          func(n uintptr, typ int, out unsafe.Pointer) bool
	cfBooleanGetValue         func(b uintptr) bool
	cfRetain                  func(ref uintptr) uintptr
	cfRelease                 func(ref uintptr)

	kCFBooleanTrue uintptr

	strMu  sync.Mutex
	cfstrs = map[string]uintptr{} // attribute and action names, created once and kept

	// systemWide is kept for the process: its messaging timeout is the global one.
	systemWide elem
)

func load() error {
	once.Do(func() {
		ax, err := purego.Dlopen(axPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = err
			return
		}
		for name, fn := range map[string]any{
			"AXIsProcessTrusted":                &axIsProcessTrusted,
			"AXUIElementCreateApplication":      &axCreateApplication,
			"AXUIElementCreateSystemWide":       &axCreateSystemWide,
			"AXUIElementCopyAttributeValue":     &axCopyAttributeValue,
			"AXUIElementCopyAttributeNames":     &axCopyAttributeNames,
			"AXUIElementPerformAction":          &axPerformAction,
			"AXUIElementSetAttributeValue":      &axSetAttributeValue,
			"AXUIElementCopyElementAtPosition":  &axCopyElementAtPosition,
			"AXUIElementSetMessagingTimeout":    &axSetMessagingTimeout,
			"AXValueGetValue":                   &axValueGetValue,
			"AXValueGetTypeID":                  &axValueGetTypeID,
			"CFGetTypeID":                       &cfGetTypeID,
			"CFStringGetTypeID":                 &cfStringGetTypeID,
			"CFNumberGetTypeID":                 &cfNumberGetTypeID,
			"CFBooleanGetTypeID":                &cfBooleanGetTypeID,
			"CFArrayGetCount":                   &cfArrayGetCount,
			"CFArrayGetValueAtIndex":            &cfArrayGetValueAtIndex,
			"CFStringCreateWithCString":         &cfStringCreateWithCString,
			"CFStringGetLength":                 &cfStringGetLength,
			"CFStringGetMaximumSizeForEncoding": &cfStringGetMaxSize,
			"CFStringGetCString":                &cfStringGetCString,
			"CFNumberGetValue":                  &cfNumberGetValue,
			"CFBooleanGetValue":                 &cfBooleanGetValue,
			"CFRetain":                          &cfRetain,
			"CFRelease":                         &cfRelease,
		} {
			lib := ax
			if name[:2] == "CF" {
				lib = cf
			}
			if _, err := purego.Dlsym(lib, name); err != nil {
				loadErr = err
				return
			}
			purego.RegisterLibFunc(fn, lib, name)
		}
		b, err := purego.Dlsym(cf, "kCFBooleanTrue")
		if err != nil {
			loadErr = err
			return
		}
		kCFBooleanTrue = *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), b))
		systemWide = elem(axCreateSystemWide())
		axSetMessagingTimeout(uintptr(systemWide), messagingTimeout)
	})
	return loadErr
}

// cfstr returns a kept CFString for a constant name.
func cfstr(s string) uintptr {
	strMu.Lock()
	defer strMu.Unlock()
	if r, ok := cfstrs[s]; ok {
		return r
	}
	r := cfStringCreateWithCString(0, s, utf8)
	cfstrs[s] = r
	return r
}

// goString copies a CFString.
func goString(s uintptr) string {
	n := cfStringGetMaxSize(cfStringGetLength(s), utf8) + 1
	buf := make([]byte, n)
	if !cfStringGetCString(s, &buf[0], n, utf8) {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

// axErr maps an AXError to an ax error (nil on success).
func axErr(code int32, what string) error {
	switch code {
	case axSuccess:
		return nil
	case axErrorAPIDisabled:
		return ErrNotTrusted
	case axErrorInvalidUIElement:
		return &Error{Code: CodeStaleRef, Msg: what + ": the element is gone"}
	case axErrorNoValue, axErrorAttributeUnsupport, axErrorActionUnsupported:
		return &Error{Code: CodeNotFound, Msg: what + ": not supported by this element"}
	case axErrorCannotComplete:
		return &Error{Code: CodeNotFound, Msg: what + ": the app did not answer"}
	}
	return &Error{Code: CodeNotFound, Msg: what + ": AXError " + strconv.Itoa(int(code))}
}

// elem is an AXUIElementRef holding one retain.
type elem uintptr

// copyAttr returns an owned attribute value, or 0 with the AX error.
func (e elem) copyAttr(name string) (uintptr, int32) {
	var v uintptr
	code := axCopyAttributeValue(uintptr(e), cfstr(name), &v)
	if code != axSuccess {
		return 0, code
	}
	return v, code
}

func (e elem) attr(name string) string {
	v, _ := e.copyAttr(name)
	if v == 0 {
		return ""
	}
	defer cfRelease(v)
	switch cfGetTypeID(v) {
	case cfStringGetTypeID():
		return goString(v)
	case cfBooleanGetTypeID():
		return strconv.FormatBool(cfBooleanGetValue(v))
	case cfNumberGetTypeID():
		var f float64
		if !cfNumberGetValue(v, kCFNumberFloat64, unsafe.Pointer(&f)) {
			return ""
		}
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

// point reads a CGPoint or CGSize attribute.
func (e elem) point(name string, typ int32) (x, y float64, ok bool) {
	v, _ := e.copyAttr(name)
	if v == 0 {
		return 0, 0, false
	}
	defer cfRelease(v)
	if cfGetTypeID(v) != axValueGetTypeID() {
		return 0, 0, false
	}
	var p [2]float64 // CGPoint{x, y} and CGSize{width, height} are both two CGFloats
	if !axValueGetValue(v, typ, unsafe.Pointer(&p)) {
		return 0, 0, false
	}
	return p[0], p[1], true
}

func (e elem) bounds() *[4]int {
	x, y, ok := e.point("AXPosition", kAXValueCGPointType)
	if !ok {
		return nil
	}
	w, h, ok := e.point("AXSize", kAXValueCGSizeType)
	if !ok {
		return nil
	}
	return &[4]int{round(x), round(y), round(w), round(h)}
}

func round(f float64) int {
	if f < 0 {
		return -int(-f + 0.5)
	}
	return int(f + 0.5)
}

func (e elem) children() []axNode {
	arr, _ := e.copyAttr("AXChildren")
	if arr == 0 {
		return nil
	}
	defer cfRelease(arr)
	n := cfArrayGetCount(arr)
	out := make([]axNode, 0, n)
	for i := range n {
		// The array owns its values: retain each one we hand out.
		out = append(out, elem(cfRetain(cfArrayGetValueAtIndex(arr, i))))
	}
	return out
}

// attrNames lists the attributes the element has.
func (e elem) attrNames() []string {
	var arr uintptr
	if axCopyAttributeNames(uintptr(e), &arr) != axSuccess || arr == 0 {
		return nil
	}
	defer cfRelease(arr)
	n := cfArrayGetCount(arr)
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, goString(cfArrayGetValueAtIndex(arr, i)))
	}
	return out
}

func (e elem) press() error {
	return axErr(axPerformAction(uintptr(e), cfstr("AXPress")), "AXPress")
}

func (e elem) setValue(text string) error {
	s := cfStringCreateWithCString(0, text, utf8)
	if s == 0 {
		return &Error{Code: CodeNotFound, Msg: "AXValue: text is not valid UTF-8"}
	}
	defer cfRelease(s)
	return axErr(axSetAttributeValue(uintptr(e), cfstr("AXValue"), s), "AXValue")
}

func (e elem) focus() error {
	return axErr(axSetAttributeValue(uintptr(e), cfstr("AXFocused"), kCFBooleanTrue), "AXFocused")
}

func (e elem) release() { cfRelease(uintptr(e)) }

// ready loads the bindings and checks the grant. Untrusted calls don't reliably fail with
// kAXErrorAPIDisabled (an app answers kAXErrorCannotComplete), so ask first.
func ready() error {
	if err := load(); err != nil {
		return &Error{Code: CodeUnsupported, Msg: err.Error()}
	}
	if !axIsProcessTrusted() {
		return ErrNotTrusted
	}
	return nil
}

// frontWindow returns the focused window of the app with this pid, else its main window.
func frontWindow(pid int) (axNode, error) {
	if err := ready(); err != nil {
		return nil, err
	}
	app := elem(axCreateApplication(int32(pid)))
	if app == 0 {
		return nil, &Error{Code: CodeNotFound, Msg: "no app with pid " + strconv.Itoa(pid)}
	}
	defer app.release()
	var code int32
	for _, a := range []string{"AXFocusedWindow", "AXMainWindow"} {
		var w uintptr
		if w, code = app.copyAttr(a); w != 0 {
			return elem(w), nil
		}
	}
	if code == axErrorAPIDisabled {
		return nil, ErrNotTrusted
	}
	return nil, &Error{Code: CodeNotFound, Msg: "app " + strconv.Itoa(pid) + " has no focused or main window (AXError " + strconv.Itoa(int(code)) + ")"}
}

func elementAt(x, y float64) (*computeruse.Element, error) {
	if err := ready(); err != nil {
		return nil, err
	}
	var out uintptr
	switch code := axCopyElementAtPosition(uintptr(systemWide), float32(x), float32(y), &out); code {
	case axSuccess:
	case axErrorNoValue:
		return nil, nil
	default:
		return nil, axErr(code, "element at point")
	}
	e := elem(out)
	defer e.release()
	role, secure := identity(e)
	name, _ := describe(e, role, secure)
	return &computeruse.Element{Role: role, Name: name, Secure: secure}, nil
}
