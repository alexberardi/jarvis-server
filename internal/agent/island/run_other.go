//go:build !linux && !darwin

package island

import (
	"context"
	"log/slog"
)

// Run runs fn with no island: this platform has no backend yet (Windows is next).
func Run(ctx context.Context, log *slog.Logger, fn func(*Island) error) error {
	return RunWith(ctx, log, AppEvents{}, fn)
}

// RunWith is Run; ev is for macOS only.
func RunWith(ctx context.Context, log *slog.Logger, _ AppEvents, fn func(*Island) error) error {
	logOff(log, "not supported on this platform yet")
	return fn(nil)
}
