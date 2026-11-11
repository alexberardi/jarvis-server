package dictate

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node/nodetest"
)

// Room talk never types or presses a key (nodetest's corpus).
func TestDictateLeavesOrdinarySpeechToTheModel(t *testing.T) {
	kb := &fakeKB{ok: true}
	ts := &Tools{KB: kb, Win: newWin(), Sleep: noSleep, Confirm: &Confirmer{Mode: ConfirmSubmit}}
	nodetest.NeverDirect(t, ts)
	if len(kb.typed) != 0 || len(kb.keys) != 0 {
		t.Fatalf("typed %q keys %v", kb.typed, kb.keys)
	}
}
