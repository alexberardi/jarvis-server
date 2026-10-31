package appclick

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestBrowserAtTurnStartDoesNotRetargetToApp(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	writer := r.wins[0]
	r.wins[0] = computer.DesktopWindow{ID: "browser", PID: 9, App: "firefox", Focused: true}
	m := node.Multi{r.src}
	m.TurnListening(ctx, true)
	defer m.TurnListening(ctx, false)
	r.wins[0] = writer
	out, handled := m.Direct(ctx, "click Bold")
	if len(r.pressed) != 0 {
		t.Fatalf("a turn begun in Firefox clicked Writer: handled=%v out=%v pressed=%v", handled, out, r.pressed)
	}
}

func TestUnmatchedSpeechAfterWindowChangePassesOn(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	m := node.Multi{r.src}
	m.TurnListening(ctx, true)
	defer m.TurnListening(ctx, false)
	r.wins[0].ID = "other-writer"
	out, handled := m.Direct(ctx, "Good morning.")
	if handled || len(r.pressed) != 0 {
		t.Fatalf("ordinary speech was taken as a click after focus change: handled=%v out=%v", handled, out)
	}
}
