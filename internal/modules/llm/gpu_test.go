package llm

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// A label refused for a lost GPU (prod outage 2026-10-10) fails at once, even for callers that
// wait for loading models, with the sentence the user should hear; /health says so.
func TestGPUUnavailableFailsFast(t *testing.T) {
	e := setup(t)
	good := e.res.eps[LabelLive]
	// What the engine stack's resolver returns, through the same mapping.
	err := notReady(&engine.NotReadyError{Label: LabelLive, State: engine.StateGPUUnavailable, Reason: engine.UserMsgDriverUpdated})
	var nr *NotReadyError
	if !errors.As(err, &nr) || nr.State != StateGPUUnavailable || nr.Reason != engine.UserMsgDriverUpdated {
		t.Fatalf("mapped to %v", err)
	}
	e.res.set(LabelLive, good, err)

	req := ChatRequest{Label: LabelLive, Messages: []Message{{Role: "user", Content: TextContent("hi")}}}
	start := time.Now()
	_, cerr := e.m.Service().Chat(WithReadyWait(e.ctx, time.Hour), req)
	if time.Since(start) > time.Second {
		t.Fatalf("took %v: must not wait", time.Since(start))
	}
	var ae *APIError
	if !errors.As(cerr, &ae) || ae.Status != 503 || ae.Message != engine.UserMsgDriverUpdated {
		t.Fatalf("%v", cerr)
	}
	if msg, ok := UserMessage(fmt.Errorf("wrapped: %w", cerr)); !ok || msg != engine.UserMsgDriverUpdated {
		t.Fatalf("UserMessage %q %v", msg, ok)
	}
	if _, ok := UserMessage(&APIError{Status: 503, Message: "live model is failed: x"}); ok {
		t.Fatal("other errors have no user message")
	}

	r := e.do("GET", "/health", nil, nil)
	v := r.json(t)
	if r.status != 503 || v["status"] != "degraded" || !strings.Contains(fmt.Sprint(v["reason"]), "reboot the server") {
		t.Fatalf("%d %s", r.status, r.body)
	}
}
