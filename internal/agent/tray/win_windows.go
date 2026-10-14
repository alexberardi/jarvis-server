//go:build windows

package tray

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The icon on Windows is a notification-area icon (Shell_NotifyIconW) owned by a hidden window
// on a thread of its own, which runs that window's message loop. A click opens the menu,
// built from the current items each time (TrackPopupMenu, returning the command), and the
// command goes to a goroutine. When Explorer restarts it broadcasts "TaskbarCreated" and the
// icon is added again. Syscalls only: the build stays CGO_ENABLED=0.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pDestroyWindow          = user32.NewProc("DestroyWindow")
	pGetMessageW            = user32.NewProc("GetMessageW")
	pTranslateMessage       = user32.NewProc("TranslateMessage")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pPostQuitMessage        = user32.NewProc("PostQuitMessage")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	pDestroyIcon            = user32.NewProc("DestroyIcon")
	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	pCreateBitmap           = gdi32.NewProc("CreateBitmap")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
)

const (
	wmNull        = 0x0000
	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205
	wmApp         = 0x8000
	wmTray        = wmApp + 1 // the icon's callback message
	wmUpdate      = wmApp + 2 // the items or tooltip changed
	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nifMessage    = 0x1
	nifIcon       = 0x2
	nifTip        = 0x4
	tpmRightBtn   = 0x0002
	tpmNoNotify   = 0x0080
	tpmReturnCmd  = 0x0100
	smCxSmIcon    = 49
	smCySmIcon    = 50
	dibRGBColors  = 0
	iconID        = 1
	trayClassName = "JarvisAutomationAgentTray"
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type notifyIconData struct {
	CbSize          uint32
	Wnd             windows.HWND
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  windows.Handle
	HbmColor windows.Handle
}

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

var (
	classOnce sync.Once
	classErr  error
	wndProcCB uintptr
	windowsMu sync.Mutex
	byHwnd    = map[windows.HWND]*winIcon{}
)

// winIcon is the backend on Windows.
type winIcon struct {
	tray           *Tray
	hwnd           windows.HWND
	icon           windows.Handle
	taskbarCreated uint32
	done           chan struct{}

	mu      sync.Mutex
	items   []Item
	tooltip string
}

// Start adds the icon to the notification area.
func Start(o Options) (*Tray, error) {
	t := newTray(o.Log, o.Actions, o.State)
	w := &winIcon{tray: t, done: make(chan struct{})}
	ready := make(chan error, 1)
	go w.loop(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	if err := t.attach(w); err != nil {
		w.close()
		return nil, err
	}
	return t, nil
}

func registerClass() error {
	classOnce.Do(func() {
		wndProcCB = windows.NewCallback(wndProc)
		inst, _, _ := pGetModuleHandleW.Call(0)
		name, _ := windows.UTF16PtrFromString(trayClassName)
		wc := wndClassEx{WndProc: wndProcCB, Instance: windows.Handle(inst), ClassName: name}
		wc.Size = uint32(unsafe.Sizeof(wc))
		if r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
			classErr = fmt.Errorf("RegisterClassExW: %v", err)
		}
	})
	return classErr
}

// loop owns the window: it is created, used and destroyed on this one locked thread.
func (w *winIcon) loop(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.done)
	if err := registerClass(); err != nil {
		ready <- err
		return
	}
	name, _ := windows.UTF16PtrFromString(trayClassName)
	title, _ := windows.UTF16PtrFromString("Jarvis Automation Agent")
	inst, _, _ := pGetModuleHandleW.Call(0)
	// A hidden top-level window, not a message-only one: TaskbarCreated is broadcast to
	// top-level windows only.
	h, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0, 0, 0, inst, 0)
	if h == 0 {
		ready <- fmt.Errorf("CreateWindowExW: %v", err)
		return
	}
	w.hwnd = windows.HWND(h)
	tc, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(tc)))
	w.taskbarCreated = uint32(r)
	windowsMu.Lock()
	byHwnd[w.hwnd] = w
	windowsMu.Unlock()
	defer func() {
		windowsMu.Lock()
		delete(byHwnd, w.hwnd)
		windowsMu.Unlock()
	}()
	if w.icon, err = makeIcon(); err != nil {
		w.tray.log.Info("tray: no icon image", "err", err) // the icon still shows, blank
	}
	if err := w.notify(nimAdd); err != nil {
		_, _, _ = pDestroyWindow.Call(uintptr(w.hwnd))
		ready <- err
		return
	}
	ready <- nil
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT, or an error
			break
		}
		_, _, _ = pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	if w.icon != 0 {
		_, _, _ = pDestroyIcon.Call(uintptr(w.icon))
	}
}

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	windowsMu.Lock()
	w := byHwnd[windows.HWND(hwnd)]
	windowsMu.Unlock()
	if w != nil {
		switch uint32(message) {
		case wmTray:
			switch uint32(lParam) & 0xFFFF {
			case wmLButtonUp, wmRButtonUp:
				w.showMenu()
			}
			return 0
		case wmUpdate:
			_ = w.notify(nimModify)
			return 0
		case wmClose:
			_ = w.notify(nimDelete)
			_, _, _ = pDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			_, _, _ = pPostQuitMessage.Call(0)
			return 0
		}
		if w.taskbarCreated != 0 && uint32(message) == w.taskbarCreated {
			_ = w.notify(nimAdd) // Explorer restarted: the icon is gone, add it again
			return 0
		}
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

// notify adds, updates or removes the icon. Window thread only.
func (w *winIcon) notify(op uintptr) error {
	w.mu.Lock()
	tip := w.tooltip
	w.mu.Unlock()
	if tip == "" {
		tip = "Jarvis Automation Agent"
	}
	nid := notifyIconData{Wnd: w.hwnd, ID: iconID, Flags: nifMessage | nifTip, CallbackMessage: wmTray, Tip: winTip(tip)}
	if w.icon != 0 {
		nid.Flags |= nifIcon
		nid.Icon = w.icon
	}
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	if r, _, err := pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&nid))); r == 0 && op != nimDelete {
		return fmt.Errorf("Shell_NotifyIconW(%d): %v", op, err)
	}
	return nil
}

// showMenu pops the menu up at the cursor and hands the chosen command on. Window thread only.
func (w *winIcon) showMenu() {
	w.mu.Lock()
	items := w.items
	w.mu.Unlock()
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	for _, it := range items {
		var label uintptr
		if !it.Separator {
			p, _ := windows.UTF16PtrFromString(it.Label)
			label = uintptr(unsafe.Pointer(p))
		}
		_, _, _ = pAppendMenuW.Call(menu, uintptr(winMenuFlags(it)), uintptr(it.ID), label)
	}
	var pt point
	_, _, _ = pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// The documented dance: foreground first, so the menu closes when the user clicks away,
	// and a WM_NULL after.
	_, _, _ = pSetForegroundWindow.Call(uintptr(w.hwnd))
	cmd, _, _ := pTrackPopupMenu.Call(menu, tpmRightBtn|tpmReturnCmd|tpmNoNotify,
		uintptr(pt.X), uintptr(pt.Y), 0, uintptr(w.hwnd), 0)
	_, _, _ = pPostMessageW.Call(uintptr(w.hwnd), wmNull, 0, 0)
	if cmd != 0 {
		w.tray.clicked(int32(cmd))
	}
}

func (w *winIcon) update(items []Item, tooltip string) error {
	w.mu.Lock()
	w.items, w.tooltip = items, tooltip
	w.mu.Unlock()
	if r, _, err := pPostMessageW.Call(uintptr(w.hwnd), wmUpdate, 0, 0); r == 0 {
		return fmt.Errorf("PostMessageW: %v", err)
	}
	return nil
}

func (w *winIcon) close() {
	if r, _, _ := pPostMessageW.Call(uintptr(w.hwnd), wmClose, 0, 0); r == 0 {
		return // the window is already gone
	}
	<-w.done
}

// makeIcon is the shield at the small-icon size as an HICON.
func makeIcon() (windows.Handle, error) {
	cx, _, _ := pGetSystemMetrics.Call(smCxSmIcon)
	cy, _, _ := pGetSystemMetrics.Call(smCySmIcon)
	size := int(max(cx, cy))
	if size <= 0 {
		size = 16
	}
	bits := winIconBits(Color(size))
	bi := bitmapInfoHeader{Width: int32(size), Height: -int32(size), Planes: 1, BitCount: 32} // top-down
	bi.Size = uint32(unsafe.Sizeof(bi))
	var p unsafe.Pointer
	color, _, err := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), dibRGBColors, uintptr(unsafe.Pointer(&p)), 0, 0)
	if color == 0 || p == nil {
		return 0, fmt.Errorf("CreateDIBSection: %v", err)
	}
	defer pDeleteObject.Call(color)
	copy(unsafe.Slice((*byte)(p), len(bits)), bits)
	mask, _, err := pCreateBitmap.Call(uintptr(size), uintptr(size), 1, 1, 0) // all 0: alpha decides
	if mask == 0 {
		return 0, fmt.Errorf("CreateBitmap: %v", err)
	}
	defer pDeleteObject.Call(mask)
	ii := iconInfo{FIcon: 1, HbmMask: windows.Handle(mask), HbmColor: windows.Handle(color)}
	h, _, err := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if h == 0 {
		return 0, errors.Join(errors.New("CreateIconIndirect"), err)
	}
	return windows.Handle(h), nil
}

// Probe says, for `status`, where the icon shows.
func Probe() string { return "on (the notification area)" }
