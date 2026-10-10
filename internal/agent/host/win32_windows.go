//go:build windows

package host

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The real Win32: user32 for windows, shell32 for open, WASAPI (COM, by vtable) for volume.
// Pure syscalls, no cgo.

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	ole32   = windows.NewLazySystemDLL("ole32.dll")

	procEnumWindows         = user32.NewProc("EnumWindows")
	procIsWindowVisible     = user32.NewProc("IsWindowVisible")
	procIsWindow            = user32.NewProc("IsWindow")
	procIsIconic            = user32.NewProc("IsIconic")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	procGetClassNameW       = user32.NewProc("GetClassNameW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procOpenInputDesktop    = user32.NewProc("OpenInputDesktop")
	procCloseDesktop        = user32.NewProc("CloseDesktop")
	procShellExecuteW       = shell32.NewProc("ShellExecuteW")
	procCoInitializeEx      = ole32.NewProc("CoInitializeEx")
	procCoUninitialize      = ole32.NewProc("CoUninitialize")
	procCoCreateInstance    = ole32.NewProc("CoCreateInstance")
)

const (
	swMinimize         = 6
	swRestore          = 9
	swShowNormal       = 1
	wmClose            = 0x0010
	desktopReadObjects = 0x0001
	coinitMulti        = 0x0
	clsctxAll          = 0x17
	rpcEChangedMode    = 0x80010106
)

var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioEndpointVolume = windows.GUID{Data1: 0x5CDF2C82, Data2: 0x841E, Data3: 0x4546, Data4: [8]byte{0x97, 0x22, 0x0C, 0xF7, 0x40, 0x78, 0x22, 0x9A}}
)

type realWin32 struct{}

// NewWin32 returns the real Win32.
func NewWin32() Win32 { return realWin32{} }

// --- COM / WASAPI ---

// comCall invokes vtable slot idx of a COM object.
func comCall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func comRelease(obj unsafe.Pointer) {
	if obj != nil {
		comCall(obj, 2)
	}
}

// withEndpoint runs f with the default render endpoint's IAudioEndpointVolume, on a locked OS
// thread with COM initialised for it.
func withEndpoint(f func(vol unsafe.Pointer) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coinitMulti)
	if hr == 0 || hr == 1 { // S_OK, S_FALSE: we own a matching uninit
		defer procCoUninitialize.Call()
	} else if uint32(hr) != rpcEChangedMode {
		return fmt.Errorf("CoInitializeEx: 0x%08x", uint32(hr))
	}
	var enum unsafe.Pointer
	hr, _, _ = procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if hr != 0 || enum == nil {
		return fmt.Errorf("CoCreateInstance(MMDeviceEnumerator): 0x%08x", uint32(hr))
	}
	defer comRelease(enum)
	var dev unsafe.Pointer
	// IMMDeviceEnumerator::GetDefaultAudioEndpoint(eRender=0, eConsole=0, &dev): slot 4.
	if hr := comCall(enum, 4, 0, 0, uintptr(unsafe.Pointer(&dev))); hr != 0 || dev == nil {
		return fmt.Errorf("no default audio endpoint: 0x%08x", uint32(hr))
	}
	defer comRelease(dev)
	var vol unsafe.Pointer
	// IMMDevice::Activate(iid, CLSCTX_ALL, NULL, &vol): slot 3.
	if hr := comCall(dev, 3, uintptr(unsafe.Pointer(&iidIAudioEndpointVolume)), clsctxAll, 0, uintptr(unsafe.Pointer(&vol))); hr != 0 || vol == nil {
		return fmt.Errorf("activate IAudioEndpointVolume: 0x%08x", uint32(hr))
	}
	defer comRelease(vol)
	return f(vol)
}

func (realWin32) AudioAvailable() bool {
	return withEndpoint(func(unsafe.Pointer) error { return nil }) == nil
}

func (realWin32) VolumeScalar() (float32, error) {
	var level float32
	err := withEndpoint(func(vol unsafe.Pointer) error {
		// GetMasterVolumeLevelScalar(&level): slot 9.
		if hr := comCall(vol, 9, uintptr(unsafe.Pointer(&level))); hr != 0 {
			return fmt.Errorf("GetMasterVolumeLevelScalar: 0x%08x", uint32(hr))
		}
		return nil
	})
	return level, err
}

func (realWin32) SetVolumeScalar(v float32) error {
	return withEndpoint(func(vol unsafe.Pointer) error {
		// SetMasterVolumeLevelScalar(level, NULL): slot 7. On amd64 the runtime mirrors the
		// first four integer argument registers into XMM0-3, so the float's bits land where
		// the callee reads them.
		if hr := comCall(vol, 7, uintptr(math.Float32bits(v)), 0); hr != 0 {
			return fmt.Errorf("SetMasterVolumeLevelScalar: 0x%08x", uint32(hr))
		}
		return nil
	})
}

// --- windows ---

func (realWin32) DesktopAvailable() bool {
	h, _, _ := procOpenInputDesktop.Call(0, 0, desktopReadObjects)
	if h == 0 {
		return false // Session 0, a locked workstation, or no window station
	}
	procCloseDesktop.Call(h)
	return true
}

var (
	enumMu     sync.Mutex
	enumFound  []Window
	enumCbOnce sync.Once
	enumCb     uintptr
)

func windowText(proc *windows.LazyProc, hwnd uintptr, n int) string {
	buf := make([]uint16, n)
	r, _, _ := proc.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(n))
	return windows.UTF16ToString(buf[:r])
}

func (realWin32) EnumWindows() ([]Window, error) {
	// Callbacks are a scarce runtime resource: create exactly one.
	enumCbOnce.Do(func() {
		enumCb = windows.NewCallback(func(hwnd, _ uintptr) uintptr {
			if v, _, _ := procIsWindowVisible.Call(hwnd); v == 0 {
				return 1
			}
			n, _, _ := procGetWindowTextLength.Call(hwnd)
			if n == 0 {
				return 1
			}
			title := windowText(procGetWindowTextW, hwnd, int(n)+1)
			class := windowText(procGetClassNameW, hwnd, 256)
			iconic, _, _ := procIsIconic.Call(hwnd)
			enumFound = append(enumFound, Window{ID: strconv.FormatUint(uint64(hwnd), 10), Title: title, App: class, Minimized: iconic != 0})
			return 1
		})
	})
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	if r, _, err := procEnumWindows.Call(enumCb, 0); r == 0 {
		return nil, fmt.Errorf("EnumWindows: %v", err)
	}
	out := enumFound
	enumFound = nil
	return out, nil
}

func (realWin32) IsWindow(hwnd uintptr) bool {
	r, _, _ := procIsWindow.Call(hwnd)
	return r != 0
}

func (realWin32) Minimize(hwnd uintptr) error {
	procShowWindow.Call(hwnd, swMinimize)
	return nil
}

func (realWin32) Focus(hwnd uintptr) error {
	procShowWindow.Call(hwnd, swRestore)
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r == 0 {
		// Windows' foreground lock can refuse a background process; report it rather than lie.
		return errors.New("SetForegroundWindow was refused (foreground lock)")
	}
	return nil
}

func (realWin32) PostClose(hwnd uintptr) error {
	if r, _, err := procPostMessageW.Call(hwnd, wmClose, 0, 0); r == 0 {
		return fmt.Errorf("PostMessage(WM_CLOSE): %v", err)
	}
	return nil
}

func (realWin32) ShellOpen(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, swShowNormal)
	if r <= 32 {
		return fmt.Errorf("ShellExecute returned %d", r)
	}
	return nil
}
