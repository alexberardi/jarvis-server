//go:build !darwin

package computer

import (
	"context"
	"log/slog"
)

// NewDesktopSource is nil off macOS: the desktop can't be recorded there yet (X11 is piece 37).
func NewDesktopSource(string, func(context.Context) ([]byte, error), *slog.Logger) Source { return nil }
