package appclick

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/node/nodetest"
)

// Room talk never clicks a control in the app in front (nodetest's corpus). "I couldn't find
// that control" for a "click …" naming nothing on screen is allowed (2026-11-10).
func TestAppClickLeavesOrdinarySpeechToTheModel(t *testing.T) {
	r := newRig(t)
	m := node.Multi{r.src}
	m.TurnListening(context.Background(), true)
	defer m.TurnListening(context.Background(), false)
	nodetest.NeverActs(t, m)
	if len(r.pressed) != 0 {
		t.Fatalf("pressed %v", r.pressed)
	}
}
