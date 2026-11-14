package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// ChatText answers like the mobile chat does, as plain text.
func TestChatText(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.eng.say("Café opens at noon today.")
	u := authn.User{ID: 7, HouseholdID: voiceHH}

	res, err := ce.m.ChatText(context.Background(), u, ChatTextRequest{
		Message: "when does the café open", NodeID: ce.node.id, HouseholdID: voiceHH})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Café opens at noon today." {
		t.Fatalf("text %q", res.Text)
	}
	if !strings.HasPrefix(res.ConversationID, "mobile-") {
		t.Fatalf("conversation id %q", res.ConversationID)
	}

	// The conversation carries on under the id handed back.
	ce.eng.say("Yes.")
	res2, err := ce.m.ChatText(context.Background(), u, ChatTextRequest{
		Message: "ok?", NodeID: ce.node.id, HouseholdID: voiceHH, ConversationID: res.ConversationID})
	if err != nil || res2.Text != "Yes." || res2.ConversationID != res.ConversationID {
		t.Fatalf("second turn %+v %v", res2, err)
	}
}

// With no SSE client watching, the fake word-by-word replay (a pause per word) is skipped.
func TestCollectedTurnSkipsTheWordReplay(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	var events int
	ce.m.replayWords(context.Background(), &sseWriter{collect: func(*pyjson.Object) { events++ }}, "one two three four five")
	if events != 0 {
		t.Fatalf("replayed %d words", events)
	}
}

// The same gate as the endpoint: a node outside the household is refused with the app's message.
func TestChatTextRefusesUnknownNode(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	_, err := ce.m.ChatText(context.Background(), authn.User{ID: 7}, ChatTextRequest{
		Message: "hi", NodeID: "no-such-node", HouseholdID: voiceHH})
	if err == nil || !strings.Contains(err.Error(), "not found in household") {
		t.Fatalf("err %v", err)
	}
}
