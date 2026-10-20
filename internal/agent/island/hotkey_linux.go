package island

import (
	"context"
	"log/slog"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/portal"
	"github.com/alexberardi/jarvis-server/internal/agent/x11"
)

// HyprlandStopBind is the hyprland.conf line that makes Ctrl+Alt+Esc stop Jarvis where the
// desktop gives the agent no key itself (Hyprland's portal binds none; docs/agent/DESIGN.md).
const HyprlandStopBind = "bind = CTRL ALT, Escape, exec, jarvis-agent computer stop"

// stopKeys are the ways to the stop hotkey on Linux: an X11 key grab in an X11 session, the
// GlobalShortcuts portal in a Wayland one. Fields for tests.
type stopKeys struct {
	getenv func(string) string
	x11    func(ctx context.Context, on func()) error
	portal func(ctx context.Context, on func()) (label string, err error)
}

var linuxStopKeys = stopKeys{
	getenv: os.Getenv,
	x11:    (&x11.Display{}).GrabStopKey,
	portal: func(ctx context.Context, on func()) (string, error) { return portal.BindStopShortcut(ctx, nil, on) },
}

// start asks for the stop hotkey (for ev.StopHotkey, until ctx ends) and, once the desktop names
// its key, sets StopHotkey's label. It can wait for the desktop's dialog: run it on a goroutine.
func (k stopKeys) start(ctx context.Context, log *slog.Logger, ev AppEvents) {
	if ev.StopHotkey == nil {
		return
	}
	if k.getenv("WAYLAND_DISPLAY") != "" {
		label, err := k.portal(ctx, ev.hotkey)
		switch {
		case err != nil:
			log.Info("no stop hotkey from the desktop; on Hyprland, add to hyprland.conf: "+HyprlandStopBind, "why", err)
		case label == "":
			log.Info("the desktop took the stop shortcut but named no key for it; on Hyprland, add to hyprland.conf: " + HyprlandStopBind)
		default:
			setStopHotkey(label)
		}
		return
	}
	if k.getenv("DISPLAY") == "" {
		return // no desktop (a Pi)
	}
	if err := k.x11(ctx, ev.hotkey); err != nil {
		log.Warn("the stop hotkey "+x11.StopKeyLabel+" won't work", "err", err)
		return
	}
	setStopHotkey(x11.StopKeyLabel)
}
