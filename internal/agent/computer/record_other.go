//go:build !darwin && !linux

package computer

import (
	"context"
	"log/slog"
)

// NewDesktopSource is nil off macOS and Linux: the desktop can't be recorded there.
func NewDesktopSource(string, func(context.Context) ([]byte, error), *slog.Logger) Source { return nil }
