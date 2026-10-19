package screen

import (
	"runtime"

	"github.com/alexberardi/jarvis-server/internal/agent/macax"
)

// Native is this OS's capturer, or nil where capture isn't supported yet (Windows, X11 come
// later). It probes nothing at construction.
func Native() Capturer {
	switch runtime.GOOS {
	case "linux":
		return &Wayland{X: OSExec{}}
	case "darwin":
		return &Mac{X: OSExec{}, Trusted: macax.ScreenTrusted, Windows: windowList, Main: mainDisplay}
	}
	return nil
}
