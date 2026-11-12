//go:build linux

package main

import (
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
