//go:build darwin

package screen

import (
	"errors"
	"sort"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// windowList reads CGWindowListCopyWindowInfo: every window, on-screen ones front to back
// first. Titles need the Screen Recording grant; without it they are empty, which only affects
// the label and title matching. purego, no cgo.

const (
	cgPath = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	cfPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"

	optAll                   = 0
	optExcludeDesktopElement = 1 << 4
	cfNumberSInt64Type       = 4
	cfNumberDoubleType       = 13
	cfStringEncodingUTF8     = 0x08000100
)

var (
	fwOnce sync.Once
	fwErr  error

	cgWindowListCopyWindowInfo func(option uint32, relativeTo uint32) uintptr
	cfArrayGetCount            func(arr uintptr) int
	cfArrayGetValueAtIndex     func(arr uintptr, i int) uintptr
	cfDictionaryGetValue       func(dict, key uintptr) uintptr
	cfNumberGetValue           func(num uintptr, typ int, out unsafe.Pointer) bool
	cfStringGetLength          func(s uintptr) int
	cfStringGetCString         func(s uintptr, buf unsafe.Pointer, size int, enc uint32) bool
	cfStringCreateWithCString  func(alloc uintptr, s string, enc uint32) uintptr
	cfGetTypeID                func(ref uintptr) uint
	cfStringGetTypeID          func() uint
	cfNumberGetTypeID          func() uint
	cfBooleanGetTypeID         func() uint
	cfBooleanGetValue          func(b uintptr) bool
	cfDictionaryGetTypeID      func() uint
	cfRelease                  func(ref uintptr)

	keyLayer, keyNumber, keyOwner, keyName, keyBounds, keyOnscreen, keyWidth, keyHeight uintptr
)

func loadFW() error {
	fwOnce.Do(func() {
		cg, err := purego.Dlopen(cgPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			fwErr = err
			return
		}
		cf, err := purego.Dlopen(cfPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			fwErr = err
			return
		}
		purego.RegisterLibFunc(&cgWindowListCopyWindowInfo, cg, "CGWindowListCopyWindowInfo")
		purego.RegisterLibFunc(&cfArrayGetCount, cf, "CFArrayGetCount")
		purego.RegisterLibFunc(&cfArrayGetValueAtIndex, cf, "CFArrayGetValueAtIndex")
		purego.RegisterLibFunc(&cfDictionaryGetValue, cf, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&cfNumberGetValue, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&cfStringGetLength, cf, "CFStringGetLength")
		purego.RegisterLibFunc(&cfStringGetCString, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&cfStringCreateWithCString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cfGetTypeID, cf, "CFGetTypeID")
		purego.RegisterLibFunc(&cfStringGetTypeID, cf, "CFStringGetTypeID")
		purego.RegisterLibFunc(&cfNumberGetTypeID, cf, "CFNumberGetTypeID")
		purego.RegisterLibFunc(&cfBooleanGetTypeID, cf, "CFBooleanGetTypeID")
		purego.RegisterLibFunc(&cfBooleanGetValue, cf, "CFBooleanGetValue")
		purego.RegisterLibFunc(&cfDictionaryGetTypeID, cf, "CFDictionaryGetTypeID")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		// The kCGWindow* key constants are CFStrings whose values are their own names.
		keyLayer = cfStringCreateWithCString(0, "kCGWindowLayer", cfStringEncodingUTF8)
		keyNumber = cfStringCreateWithCString(0, "kCGWindowNumber", cfStringEncodingUTF8)
		keyOwner = cfStringCreateWithCString(0, "kCGWindowOwnerName", cfStringEncodingUTF8)
		keyName = cfStringCreateWithCString(0, "kCGWindowName", cfStringEncodingUTF8)
		keyBounds = cfStringCreateWithCString(0, "kCGWindowBounds", cfStringEncodingUTF8)
		keyOnscreen = cfStringCreateWithCString(0, "kCGWindowIsOnscreen", cfStringEncodingUTF8)
		keyWidth = cfStringCreateWithCString(0, "Width", cfStringEncodingUTF8) // the bounds dictionary's keys
		keyHeight = cfStringCreateWithCString(0, "Height", cfStringEncodingUTF8)
		if keyLayer == 0 || keyNumber == 0 || keyOwner == 0 || keyName == 0 || keyBounds == 0 || keyOnscreen == 0 ||
			keyWidth == 0 || keyHeight == 0 {
			fwErr = errors.New("CoreFoundation: could not create window-info keys")
		}
	})
	return fwErr
}

func windowList() ([]MacWindow, error) {
	if err := loadFW(); err != nil {
		return nil, err
	}
	arr := cgWindowListCopyWindowInfo(optAll|optExcludeDesktopElement, 0)
	if arr == 0 {
		return nil, errors.New("no window list")
	}
	defer cfRelease(arr)
	n := cfArrayGetCount(arr)
	out := make([]MacWindow, 0, n)
	for i := 0; i < n; i++ {
		d := cfArrayGetValueAtIndex(arr, i)
		id, ok := cfInt(cfDictionaryGetValue(d, keyNumber))
		if !ok || id <= 0 {
			continue
		}
		layer, ok := cfInt(cfDictionaryGetValue(d, keyLayer))
		if !ok {
			layer = -1 // not a normal window
		}
		w := MacWindow{ID: uint32(id), Layer: int(layer), Title: cfString(cfDictionaryGetValue(d, keyName)),
			App: cfString(cfDictionaryGetValue(d, keyOwner)), OnScreen: cfBool(cfDictionaryGetValue(d, keyOnscreen))}
		if b := cfDictionaryGetValue(d, keyBounds); b != 0 && cfGetTypeID(b) == cfDictionaryGetTypeID() {
			w.W, _ = cfFloat(cfDictionaryGetValue(b, keyWidth))
			w.H, _ = cfFloat(cfDictionaryGetValue(b, keyHeight))
		}
		out = append(out, w)
	}
	// On-screen windows come front to back; keep them first whatever the list did.
	sort.SliceStable(out, func(a, b int) bool { return out[a].OnScreen && !out[b].OnScreen })
	return out, nil
}

func cfBool(ref uintptr) bool {
	return ref != 0 && cfGetTypeID(ref) == cfBooleanGetTypeID() && cfBooleanGetValue(ref)
}

func cfFloat(ref uintptr) (float64, bool) {
	if ref == 0 || cfGetTypeID(ref) != cfNumberGetTypeID() {
		return 0, false
	}
	var v float64
	if !cfNumberGetValue(ref, cfNumberDoubleType, unsafe.Pointer(&v)) {
		return 0, false
	}
	return v, true
}

func cfInt(ref uintptr) (int64, bool) {
	if ref == 0 || cfGetTypeID(ref) != cfNumberGetTypeID() {
		return 0, false
	}
	var v int64
	if !cfNumberGetValue(ref, cfNumberSInt64Type, unsafe.Pointer(&v)) {
		return 0, false
	}
	return v, true
}

func cfString(ref uintptr) string {
	if ref == 0 || cfGetTypeID(ref) != cfStringGetTypeID() {
		return ""
	}
	size := cfStringGetLength(ref)*4 + 1 // UTF-16 units → at most 4 UTF-8 bytes each
	buf := make([]byte, size)
	if !cfStringGetCString(ref, unsafe.Pointer(&buf[0]), size, cfStringEncodingUTF8) {
		return ""
	}
	for i, c := range buf {
		if c == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}
