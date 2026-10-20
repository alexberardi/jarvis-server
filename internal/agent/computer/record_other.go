//go:build !darwin && !linux

package computer

import (
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/screen"
)

// NewDesktopSource is nil off macOS and Linux: the desktop can't be recorded there.
func NewDesktopSource(string, screen.Capturer, *slog.Logger) Source { return nil }
