//go:build windows

package host

import (
	"context"
	"runtime"
	"strconv"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
)

// TestNativeWindowsArranging drives the arranging actions through the real Win32 against a
// window it creates itself (CI's runner has a desktop), and destroys it after. It touches no
// other window.
func TestNativeWindowsArranging(t *testing.T) {
	h := native(t)
	ctx := context.Background()
	if !h.Has(ctx, CapWindowsMaximize) {
		t.Skip("no desktop here")
	}
	runtime.LockOSThread() // the window's messages are handled on the thread that made it
	defer runtime.UnlockOSThread()
	class, _ := windows.UTF16PtrFromString("STATIC")
	title, _ := windows.UTF16PtrFromString("jarvis arranging test")
	const wsOverlappedWindow, wsVisible = 0x00CF0000, 0x10000000
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow|wsVisible, 100, 100, 640, 480, 0, 0, 0, 0)
	if hwnd == 0 {
		t.Fatalf("CreateWindowEx: %v", err)
	}
	defer procDestroyWindow.Call(hwnd)
	id := strconv.FormatUint(uint64(hwnd), 10)
	api := NewWin32()
	do := func(a WindowAction) {
		t.Helper()
		if err := h.WindowDo(ctx, a, id); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	do(ActionMaximize)
	if !api.IsZoomed(hwnd) {
		t.Fatal("not maximized")
	}
	do(ActionUnmaximize)
	if api.IsZoomed(hwnd) {
		t.Fatal("still maximized")
	}
	areas, err := api.WorkAreas()
	if err != nil || len(areas) == 0 {
		t.Fatalf("work areas %v %v", areas, err)
	}
	t.Logf("work areas: %v", areas)
	for _, a := range []WindowAction{ActionSnapLeft, ActionSnapRight} {
		do(a)
		r, err := api.WindowRect(hwnd)
		if err != nil {
			t.Fatal(err)
		}
		area := areas[0]
		wantL := area.L
		if a == ActionSnapRight {
			wantL += area.w() / 2
		}
		// The window rect includes an invisible resize border on Windows 10/11 (DWM), so allow a
		// few pixels.
		if abs(r.L-wantL) > 16 || abs(r.w()-area.w()/2) > 32 || abs(r.T-area.T) > 16 {
			t.Fatalf("%s: rect %+v in %+v", a, r, area)
		}
	}
	do(ActionMinimize)
	if !api.IsIconic(hwnd) {
		t.Fatal("not minimized")
	}
	do(ActionUnminimize)
	if api.IsIconic(hwnd) {
		t.Fatal("still minimized")
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
