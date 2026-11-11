//go:build linux

package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/appclick"
	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
)

// newAppClick is voice "click X" in the focused app window, through AT-SPI; nil where there's no
// window list with pids.
func newAppClick(on func() bool, p targets.Picker, c *dictate.Confirmer) node.Toolset {
	ws := computer.NativeWindows()
	if ws == nil {
		return nil
	}
	return &appclick.Source{Windows: ws, Snapshot: atspi.SnapshotShowing, Press: atspi.Press, Allowed: on, Picker: p, Confirm: c}
}

// enableAppElements switches the session's accessibility bus on at start, as a recording does:
// without it Qt and Chromium apps show voice clicks and computer tasks no elements until
// something else turns it on.
func enableAppElements(ctx context.Context, log *slog.Logger) {
	st := atspi.Enable(ctx)
	log.Info("accessibility", "enabled", st.Enabled, "turned_on", st.TurnedOn, "from", st.From, "err", st.Err)
}

// appElementsStatus is status's line on reading app elements (AT-SPI), with the fix when off.
func appElementsStatus(ctx context.Context) string {
	st := atspi.Enable(ctx)
	switch {
	case st.Err != nil:
		return fmt.Sprintf("off (%v)\n  -> Voice clicks in apps and computer tasks can't see app controls. Install at-spi2-core\n     (the accessibility bus) and log in again.", st.Err)
	case !st.Enabled:
		return "off: the accessibility bus refused to switch on"
	case st.TurnedOn:
		return "on (switched on just now; the running agent does this at start)"
	}
	return "on"
}
