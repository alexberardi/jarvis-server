//go:build darwin

package macax

import (
	"errors"
	"runtime"
	"structs"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// The microphone and Automation grants, read without prompting (the setup page's live ticks).

const (
	avPath = "/System/Library/Frameworks/AVFoundation.framework/AVFoundation"
	csPath = "/System/Library/Frameworks/CoreServices.framework/CoreServices"
)

var (
	avOnce    sync.Once
	avErr     error
	avAudio   uintptr // the NSString AVMediaTypeAudio
	avDevice  objc.Class
	selStatus = objc.RegisterName("authorizationStatusForMediaType:")
)

func loadAV() error {
	avOnce.Do(func() {
		h, err := purego.Dlopen(avPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			avErr = err
			return
		}
		sym, err := purego.Dlsym(h, "AVMediaTypeAudio")
		if err != nil {
			avErr = err
			return
		}
		avAudio = deref(sym)
		if avDevice = objc.GetClass("AVCaptureDevice"); avDevice == 0 || avAudio == 0 {
			avErr = errors.New("AVCaptureDevice unavailable")
		}
	})
	return avErr
}

// MicStatus is this process's microphone grant (AVAuthorizationStatus). It never prompts:
// opening the microphone does.
func MicStatus() string {
	if loadAV() != nil {
		return StatusUnknown
	}
	switch objc.Send[int](objc.ID(avDevice), selStatus, avAudio) {
	case 3: // authorized
		return StatusGranted
	case 1, 2: // restricted, denied
		return StatusDenied
	case 0: // not determined
		return StatusNotAsked
	}
	return StatusUnknown
}

// aeDesc is AEDesc: a DescType and a data handle.
type aeDesc struct {
	_    structs.HostLayout
	typ  uint32
	data uintptr
}

var (
	aeOnce sync.Once
	aeErr  error

	aeCreateDesc  func(typ uint32, data unsafe.Pointer, size int, out *aeDesc) int16
	aeDisposeDesc func(d *aeDesc) int16
	aeDetermine   func(target *aeDesc, class, id uint32, ask bool) int32
)

func loadAE() error {
	aeOnce.Do(func() {
		h, err := purego.Dlopen(csPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			aeErr = err
			return
		}
		purego.RegisterLibFunc(&aeCreateDesc, h, "AECreateDesc")
		purego.RegisterLibFunc(&aeDisposeDesc, h, "AEDisposeDesc")
		purego.RegisterLibFunc(&aeDetermine, h, "AEDeterminePermissionToAutomateTarget")
	})
	return aeErr
}

const (
	typeApplicationBundleID = 0x62756e64 // 'bund'
	typeWildCard            = 0x2a2a2a2a // '****'

	errAEEventNotPermitted            = -1743
	errAEEventWouldRequireUserConsent = -1744
	procNotFound                      = -600
)

// AutomationStatus is whether this process may send Apple events to the app with bundleID
// (dictation asks "System Events" for the frontmost app). It never prompts. "unknown" when that app
// isn't running: macOS can only answer for a running target.
func AutomationStatus(bundleID string) string {
	if loadAE() != nil || bundleID == "" {
		return StatusUnknown
	}
	b := []byte(bundleID)
	var target aeDesc
	if aeCreateDesc(typeApplicationBundleID, unsafe.Pointer(&b[0]), len(b), &target) != 0 {
		return StatusUnknown
	}
	defer aeDisposeDesc(&target)
	st := aeDetermine(&target, typeWildCard, typeWildCard, false)
	runtime.KeepAlive(b)
	switch st {
	case 0:
		return StatusGranted
	case errAEEventNotPermitted:
		return StatusDenied
	case errAEEventWouldRequireUserConsent:
		return StatusNotAsked
	case procNotFound:
		return StatusUnknown
	}
	return StatusUnknown
}
