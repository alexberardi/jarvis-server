package cc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// gaveUpWithin runs f and fails the test if it hasn't returned in time (the waiter is stuck on the
// turn lock).
func gaveUpWithin(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("still waiting on the turn lock after its caller gave up")
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestTurnLockWaitGivesUpWithCaller(t *testing.T) {
	var l turnLock
	if err := l.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var err error
	gaveUpWithin(t, 2*time.Second, func() { err = l.lock(ctx) })
	if !errors.Is(err, errConversationBusy) {
		t.Fatalf("got %v, want errConversationBusy", err)
	}
	l.Unlock()
	if err := l.lock(context.Background()); err != nil {
		t.Fatalf("free lock: %v", err)
	}
	l.Unlock()
	// A free lock is taken even when the caller is already gone: the wait is what gives up.
	if err := l.lock(cancelledCtx()); err != nil {
		t.Fatalf("free lock, cancelled ctx: %v", err)
	}
	l.Unlock()
}

func TestProcessTurnBusyWhenCallerGone(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Cloud)
	ve.start("tl1", weatherTool)
	conv := ve.m.convs.get("tl1")
	conv.mu.Lock()
	defer conv.mu.Unlock()
	gaveUpWithin(t, 2*time.Second, func() {
		if _, err := ve.m.processTurn(cancelledCtx(), nil, turnInput{VoiceCommand: "hi", ConversationID: "tl1"}); !errors.Is(err, errConversationBusy) {
			t.Errorf("processTurn: %v, want errConversationBusy", err)
		}
		if _, _, err := ve.m.continueBlocking(cancelledCtx(), "tl1", nil); !errors.Is(err, errConversationBusy) {
			t.Errorf("continueBlocking: %v, want errConversationBusy", err)
		}
	})
}

func TestVoiceRoutesBusyIs409(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Cloud)
	ve.start("tl2", weatherTool)
	conv := ve.m.convs.get("tl2")
	conv.mu.Lock()
	defer conv.mu.Unlock()
	n := &nodeCtx{ID: ve.node.id, HouseholdID: voiceHH}
	for _, c := range []struct {
		body string
		h    func(http.ResponseWriter, *http.Request, *nodeCtx)
	}{
		{`{"voice_command":"hi","conversation_id":"tl2"}`, ve.m.handleVoiceCommand},
		{`{"voice_command":"hi","conversation_id":"tl2"}`, ve.m.handleVoiceStream},
		{`{"conversation_id":"tl2","tool_results":[{"tool_call_id":"c1","output":{}}]}`, ve.m.handleContinue},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/", strings.NewReader(c.body)).WithContext(cancelledCtx())
		gaveUpWithin(t, 2*time.Second, func() { c.h(rec, req, n) })
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status %d, want 409: %s", c.body, rec.Code, rec.Body)
		}
	}
	// The streamed continue gives up too and answers with the use_blocking_continue fallback.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"conversation_id":"tl2","tool_results":[{"tool_call_id":"c1","output":{}}]}`)).
		WithContext(cancelledCtx())
	gaveUpWithin(t, 2*time.Second, func() { ve.m.handleContinueStream(rec, req, n) })
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "use_blocking_continue") {
		t.Errorf("continue/stream: status %d: %s", rec.Code, rec.Body)
	}
}

func TestMobileChatWaitGivesUpWithClient(t *testing.T) {
	ce := newChatEnv(t, prompts.Cloud)
	cid := ce.warm("tok-7")
	conv := ce.m.convs.get(cid)
	before := len(history(conv))
	u := authn.User{ID: 7, HouseholdID: voiceHH}
	req := chatRequest{Message: "hello", NodeID: ce.node.id, HouseholdID: voiceHH, ConversationID: cid}
	row, err := ce.m.chatNode(context.Background(), u, req)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	func() {
		// Deferred, so a regression fails in 2 s instead of hanging on the held lock.
		conv.mu.Lock()
		defer conv.mu.Unlock()
		gaveUpWithin(t, 2*time.Second, func() {
			ce.m.chatStream(cancelledCtx(), &sseWriter{w: rec, rc: http.NewResponseController(rec)}, u, row, req)
		})
	}()
	if !strings.Contains(rec.Body.String(), `"type": "error"`) {
		t.Fatalf("no error frame: %s", rec.Body)
	}
	if got := len(history(conv)); got != before {
		t.Fatalf("history grew from %d to %d: a turn the client gave up on still ran", before, got)
	}
}
