//go:build linux

package computer

import (
	"context"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// NewDesktopSource is the recorder's desktop mode on Linux: X11 (RECORD and AT-SPI), with
// keyframes from keyframe (nil: none) kept in dir. A Wayland session refuses it at start.
func NewDesktopSource(dir string, keyframe func(context.Context) ([]byte, error), log *slog.Logger) Source {
	return newX11Source(dir, &x11.Display{}, x11Access{ElementAt: atspi.ElementAt, Focused: atspi.Focused}, keyframe, log)
}
