package desktop

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node/nodetest"
)

// Room talk never switches a window or a tab (nodetest's corpus).
func TestDesktopLeavesOrdinarySpeechToTheModel(t *testing.T) {
	b := &fakeBackend{caps: hypr, windows: []host.Window{
		{ID: "0xf", Title: "YouTube — Mozilla Firefox", App: "firefox", Focused: true},
		{ID: "0xd", Title: "Discord", App: "discord"},
		{ID: "0x7", Title: "~/jarvis", App: "foot"},
	}}
	ts := New(host.New(b), OpenPolicy{}, catalogue)
	fv := &fakeViews{views: firefoxTabs}
	ts.Views = fv
	nodetest.NeverDirect(t, ts)
	if len(b.actions) != 0 || len(fv.switched) != 0 {
		t.Fatalf("acted: %v %v", b.actions, fv.switched)
	}
}
