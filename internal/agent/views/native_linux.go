//go:build linux

package views

import (
	"github.com/alexberardi/jarvis-server/internal/agent/atspi"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// Native is the view source for this session: AT-SPI over the compositor's (or X11's) window
// list; nil where there's no window list with pids.
func Native(h *host.Host) desktop.ViewSource {
	ws := computer.NativeWindows()
	if ws == nil {
		return nil
	}
	return &Source{Windows: ws, Host: h, Tabs: atspi.Tabs, Select: atspi.SelectTab}
}
