package appclick

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestConfirmAllAlsoAsksForSafeClicks(t *testing.T) {
	r := newRig(t)
	r.src.Confirm.Mode = dictate.ConfirmAll
	out, handled := r.src.Direct(context.Background(), "click Bold")
	if !handled || out["status"] != "awaiting_confirmation" || len(r.pressed) != 0 {
		t.Fatalf("confirm-all was bypassed: handled=%v out=%v pressed=%v", handled, out, r.pressed)
	}
}

func TestFocusChangeDuringListeningDoesNotRetarget(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := node.Multi{r.src}
	m.TurnListening(ctx, true)
	defer m.TurnListening(ctx, false)
	r.wins[0].ID = "w3"
	r.wins[0].Title = "Untitled 2 - LibreOffice Writer"
	out, handled := m.Direct(ctx, "click Bold")
	if len(r.pressed) != 0 {
		t.Fatalf("retargeted after listening began in w2: handled=%v out=%v pressed=%v", handled, out, r.pressed)
	}
}

func TestDisablingControlCancelsPendingClick(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	out, handled := r.src.Direct(ctx, "click Delete Comment")
	if !handled || out["status"] != "awaiting_confirmation" {
		t.Fatalf("no pending confirmation: %v", out)
	}
	r.on = false
	out, _ = r.src.AnswerChoice(ctx, "yes")
	if len(r.pressed) != 0 {
		t.Fatalf("clicked after control was disabled: out=%v pressed=%v", out, r.pressed)
	}
}

func TestWhitespaceNameStillAsks(t *testing.T) {
	r := newRig(t)
	r.els[0].Name = " \t "
	snap, err := r.src.Snapshot(context.Background(), 42, snapshotMax)
	if err != nil {
		t.Fatal(err)
	}
	out := r.src.press(context.Background(), r.wins[0], snap, "a1")
	if out["status"] != "awaiting_confirmation" || len(r.pressed) != 0 {
		t.Fatalf("nameless control acted without asking: out=%v pressed=%v", out, r.pressed)
	}
}

// F4's other half: a refused retarget re-binds to the window now in front, so saying it again
// works (a turn that switched windows by voice first isn't stuck).
func TestRetargetRefusalRebinds(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := node.Multi{r.src}
	m.TurnListening(ctx, true)
	defer m.TurnListening(ctx, false)
	r.wins[0].ID = "w3"
	if out, ok := m.Direct(ctx, "click Bold"); !ok || out["success"] != false || len(r.pressed) != 0 {
		t.Fatalf("first click in the new window: %v %v, pressed %v", out, ok, r.pressed)
	}
	if out, ok := m.Direct(ctx, "click Bold"); !ok || out["success"] != true || len(r.pressed) != 1 {
		t.Fatalf("said again: %v %v, pressed %v", out, ok, r.pressed)
	}
}
