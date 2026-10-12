package island

import (
	"context"
	"log/slog"
)

// Run runs fn with the island on screen when this desktop can show one, or with nil (logged
// once) when it can't. On Linux that is a Wayland compositor with wlr-layer-shell.
func Run(ctx context.Context, log *slog.Logger, fn func(*Island) error) error {
	be, err := newWayland()
	if err != nil {
		logOff(log, err)
		return fn(nil)
	}
	i := newIsland(be, log)
	defer i.Close()
	return fn(i)
}
