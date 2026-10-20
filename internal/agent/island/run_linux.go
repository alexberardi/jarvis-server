package island

import (
	"context"
	"log/slog"
)

// Run runs fn with the island on screen when this desktop can show one, or with nil (logged
// once) when it can't. On Linux that is a Wayland compositor with wlr-layer-shell.
func Run(ctx context.Context, log *slog.Logger, fn func(*Island) error) error {
	return RunWith(ctx, log, AppEvents{}, fn)
}

// RunWith is Run with ev's stop hotkey (Ctrl+Alt+Esc) while fn runs; ev's other events are
// macOS's only (a second launch here is a new process).
func RunWith(ctx context.Context, log *slog.Logger, ev AppEvents, fn func(*Island) error) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go linuxStopKeys.start(ctx, log, ev)
	be, err := newWayland()
	if err != nil {
		logOff(log, err)
		return fn(nil)
	}
	i := newIsland(be, log)
	defer i.Close()
	return fn(i)
}

// RunLoop runs fn with ev's stop hotkey; the AppKit loop it stands for is macOS's only.
func RunLoop(ctx context.Context, log *slog.Logger, ev AppEvents, fn func(loop bool) error) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	go linuxStopKeys.start(ctx, log, ev)
	return fn(false)
}
