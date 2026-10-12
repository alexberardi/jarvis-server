//go:build darwin

package screen

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// frontWindow reads CGWindowListCopyWindowInfo (front to back) and returns the first normal
// (layer 0) on-screen window: the frontmost window. Titles need the Screen Recording grant;
// without it they are empty, which only affects the label. purego, no cgo.

const (
	cgPath = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
	cfPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"

	optOnScreenOnly          = 1 << 0
	optExcludeDesktopElement = 1 << 4
	cfNumberSInt64Type       = 4
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
	cfRelease                  func(ref uintptr)

	keyLayer, keyNumber, keyOwner, keyName uintptr
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
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		// The kCGWindow* key constants are CFStrings whose values are their own names.
		keyLayer = cfStringCreateWithCString(0, "kCGWindowLayer", cfStringEncodingUTF8)
		keyNumber = cfStringCreateWithCString(0, "kCGWindowNumber", cfStringEncodingUTF8)
		keyOwner = cfStringCreateWithCString(0, "kCGWindowOwnerName", cfStringEncodingUTF8)
		keyName = cfStringCreateWithCString(0, "kCGWindowName", cfStringEncodingUTF8)
		if keyLayer == 0 || keyNumber == 0 || keyOwner == 0 || keyName == 0 {
			fwErr = errors.New("CoreFoundation: could not create window-info keys")
		}
	})
	return fwErr
}

func frontWindow() (FrontWindow, error) {
	if err := loadFW(); err != nil {
		return FrontWindow{}, err
	}
	arr := cgWindowListCopyWindowInfo(optOnScreenOnly|optExcludeDesktopElement, 0)
	if arr == 0 {
		return FrontWindow{}, errors.New("no window list")
	}
	defer cfRelease(arr)
	for i, n := 0, cfArrayGetCount(arr); i < n; i++ {
		d := cfArrayGetValueAtIndex(arr, i)
		layer, ok := cfInt(cfDictionaryGetValue(d, keyLayer))
		if !ok || layer != 0 {
			continue
		}
		id, ok := cfInt(cfDictionaryGetValue(d, keyNumber))
		if !ok || id <= 0 {
			continue
		}
		return FrontWindow{ID: uint32(id), Title: cfString(cfDictionaryGetValue(d, keyName)),
			App: cfString(cfDictionaryGetValue(d, keyOwner))}, nil
	}
	return FrontWindow{}, errors.New("no window in front")
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
