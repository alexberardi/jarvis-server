//go:build linux

package input

import (
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/vkbd"
	"github.com/alexberardi/jarvis-server/internal/agent/vptr"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// Native is the wlroots backend in a Wayland session (Available says when the compositor
// lacks the protocols, as GNOME and KDE do), else the X11 XTEST backend (unavailable outside
// an X11 session).
func Native() Input {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return &Wlroots{Pointer: &vptr.Pointer{}, Keyboard: &vkbd.Keyboard{}}
	}
	return &X11{D: &x11.Display{}}
}
