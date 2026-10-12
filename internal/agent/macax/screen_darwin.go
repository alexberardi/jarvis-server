//go:build darwin

package macax

import (
	"sync"

	"github.com/ebitengine/purego"
)

const cgPath = "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"

var (
	cgOnce    sync.Once
	cgLoadErr error

	cgPreflightScreenCaptureAccess func() bool
	cgRequestScreenCaptureAccess   func() bool
)

func loadCG() error {
	cgOnce.Do(func() {
		cg, err := purego.Dlopen(cgPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			cgLoadErr = err
			return
		}
		purego.RegisterLibFunc(&cgPreflightScreenCaptureAccess, cg, "CGPreflightScreenCaptureAccess")
		purego.RegisterLibFunc(&cgRequestScreenCaptureAccess, cg, "CGRequestScreenCaptureAccess")
	})
	return cgLoadErr
}

// ScreenTrusted reports whether this process holds the Screen Recording grant. It never prompts.
func ScreenTrusted() bool {
	if loadCG() != nil {
		return false
	}
	return cgPreflightScreenCaptureAccess()
}

// ScreenPrompt asks macOS to add this process to Screen Recording and show its prompt when it
// isn't granted yet (once per process; later calls only report). macOS applies a new grant
// after the process restarts.
func ScreenPrompt() bool {
	if loadCG() != nil {
		return false
	}
	return cgRequestScreenCaptureAccess()
}
