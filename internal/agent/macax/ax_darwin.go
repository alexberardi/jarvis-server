//go:build darwin

// Package macax asks macOS whether this process holds the Accessibility (TCC) grant, and can
// make macOS itself add the process to System Settings → Privacy & Security → Accessibility and
// show its "Open System Settings" prompt. Adding a bare binary with "+" doesn't work on macOS
// 26; an app calling AXIsProcessTrustedWithOptions does.
//
// It uses purego (dlopen) so the build stays CGO_ENABLED=0.
package macax

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const (
	axPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
	cfPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
	// kCFStringEncodingUTF8
	utf8 = 0x08000100
)

var (
	once    sync.Once
	loadErr error

	axIsProcessTrusted            func() bool
	axIsProcessTrustedWithOptions func(options uintptr) bool
	cfStringCreateWithCString     func(alloc uintptr, s string, enc uint32) uintptr
	cfDictionaryCreate            func(alloc uintptr, keys, values unsafe.Pointer, n int, keyCB, valCB uintptr) uintptr
	cfRelease                     func(ref uintptr)

	kCFBooleanTrue uintptr // the CFBooleanRef itself
	keyCallBacks   uintptr // &kCFTypeDictionaryKeyCallBacks
	valueCallBacks uintptr // &kCFTypeDictionaryValueCallBacks
)

// deref reads the pointer stored at a symbol's address (a CF global like kCFBooleanTrue).
func deref(addr uintptr) uintptr {
	return *(*uintptr)(unsafe.Add(unsafe.Pointer(nil), addr))
}

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
		purego.RegisterLibFunc(&axIsProcessTrusted, ax, "AXIsProcessTrusted")
		purego.RegisterLibFunc(&axIsProcessTrustedWithOptions, ax, "AXIsProcessTrustedWithOptions")
		purego.RegisterLibFunc(&cfStringCreateWithCString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cfDictionaryCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		b, err := purego.Dlsym(cf, "kCFBooleanTrue")
		if err != nil {
			loadErr = err
			return
		}
		kCFBooleanTrue = deref(b)
		if keyCallBacks, err = purego.Dlsym(cf, "kCFTypeDictionaryKeyCallBacks"); err != nil {
			loadErr = err
			return
		}
		if valueCallBacks, err = purego.Dlsym(cf, "kCFTypeDictionaryValueCallBacks"); err != nil {
			loadErr = err
		}
	})
	return loadErr
}

// Supported reports whether this platform has the Accessibility API.
func Supported() bool { return load() == nil }

// Trusted reports whether this process holds the Accessibility grant. It never prompts.
func Trusted() bool {
	if load() != nil {
		return false
	}
	return axIsProcessTrusted()
}

// Prompt asks macOS to add this process to the Accessibility list and show its prompt when it
// isn't trusted yet. It returns whether the process is trusted now (normally false: the user
// grants asynchronously, and the process sees it on later checks).
func Prompt() bool {
	if load() != nil {
		return false
	}
	// The value of kAXTrustedCheckOptionPrompt is this literal string.
	key := cfStringCreateWithCString(0, "AXTrustedCheckOptionPrompt", utf8)
	if key == 0 {
		return axIsProcessTrusted()
	}
	defer cfRelease(key)
	keys := [1]uintptr{key}
	vals := [1]uintptr{kCFBooleanTrue}
	dict := cfDictionaryCreate(0, unsafe.Pointer(&keys[0]), unsafe.Pointer(&vals[0]), 1, keyCallBacks, valueCallBacks)
	if dict == 0 {
		return axIsProcessTrusted()
	}
	defer cfRelease(dict)
	return axIsProcessTrustedWithOptions(dict)
}
