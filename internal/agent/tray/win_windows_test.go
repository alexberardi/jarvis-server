//go:build windows

package tray

import (
	"os"
	"testing"
	"unsafe"
)

// The struct layouts Shell_NotifyIconW and RegisterClassExW read (64-bit Windows).
func TestWinStructSizes(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layouts")
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != 976 {
		t.Errorf("NOTIFYICONDATAW is %d bytes, want 976", got)
	}
	if got := unsafe.Sizeof(wndClassEx{}); got != 80 {
		t.Errorf("WNDCLASSEXW is %d bytes, want 80", got)
	}
	if got := unsafe.Sizeof(msg{}); got != 48 {
		t.Errorf("MSG is %d bytes, want 48", got)
	}
}

// The shield as a real HICON (GDI only: no window, no notification area).
func TestWinMakeIcon(t *testing.T) {
	h, err := makeIcon()
	if err != nil || h == 0 {
		t.Fatalf("makeIcon: %v", err)
	}
	if r, _, err := pDestroyIcon.Call(uintptr(h)); r == 0 {
		t.Fatalf("DestroyIcon: %v", err)
	}
}

// TestWinNotifyIconLive adds the icon to the real notification area, updates it and removes it.
// Opt-in (JARVIS_TRAY_WIN_LIVE=1): it shows on the desktop of whoever runs it.
func TestWinNotifyIconLive(t *testing.T) {
	if os.Getenv("JARVIS_TRAY_WIN_LIVE") == "" {
		t.Skip("JARVIS_TRAY_WIN_LIVE=1 to add the icon to this desktop's notification area")
	}
	tr, err := Start(Options{State: State{Status: "Running"}})
	if err != nil {
		t.Fatal(err)
	}
	tr.Update(func(s *State) { s.OpenURL = "http://192.0.2.10:7710/" })
	tr.Close()
}
