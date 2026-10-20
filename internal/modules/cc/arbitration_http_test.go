package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// expiredWindow makes every arbitration wait end at once (the window ran out).
func expiredWindow(m *Module) {
	m.wakeAfter = func(time.Duration) <-chan time.Time {
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
}

// Loop #287: one "Hey Jarvis" heard by two nodes of a household is answered once; the other
// node gets a silent not_for_me marked stood_down.
func TestTwoNodesOneWakeOneAnswer(t *testing.T) {
	ve := newVoiceEnv(t, prompts.ChatGPT, expiredWindow)
	other := ve.createNode("node-v2", voiceHH)
	ve.start("wake-a", `[]`)
	ve.do("POST", "/api/v0/conversation/start",
		`{"conversation_id":"wake-b","node_context":{"timezone":"America/New_York","speaker_user_id":7},"client_tools":[]}`, other.h()).want(200)
	ve.eng.say("It's noon.")
	ve.eng.say("It's noon.")
	before := len(ve.eng.requests()) // the two warmups
	a := ve.turn("/api/v0/voice/command/stream", "wake-a", "what time is it", map[string]any{"turn_source": "wake", "wake_confidence": 0.8})
	b := ve.do("POST", "/api/v0/voice/command/stream",
		map[string]any{"voice_command": "What time is it?", "conversation_id": "wake-b", "turn_source": "wake", "wake_confidence": 0.9}, other.h())
	if a.status != 200 {
		t.Fatalf("the first node must answer: %d %s", a.status, a.body)
	}
	body := b.want(202).json()
	if body["stop_reason"] != "not_for_me" || body["stood_down"] != true || body["assistant_message"] != "" {
		t.Fatalf("the second node must stand down silently: %v", body)
	}
	if said := ve.tts.said(); len(said) != 1 {
		t.Fatalf("want 1 spoken reply, got %q", said)
	}
	if n := len(ve.eng.requests()) - before; n != 1 {
		t.Fatalf("the model ran %d times for one wake, want 1", n)
	}
}

// Two nodes of different households hearing the same words both answer.
func TestTwoHouseholdsSameWordsBothAnswer(t *testing.T) {
	ve := newVoiceEnv(t, prompts.ChatGPT, expiredWindow)
	ve.auth.addUser(9, "hh-other", "member")
	other := ve.createNode("node-x", "hh-other")
	ve.start("wake-a", `[]`)
	ve.do("POST", "/api/v0/conversation/start",
		`{"conversation_id":"wake-x","node_context":{"timezone":"America/New_York"},"client_tools":[]}`, other.h()).want(200)
	ve.eng.say("It's noon.")
	ve.eng.say("It's noon.")
	ve.turn("/api/v0/voice/command/stream", "wake-a", "what time is it", map[string]any{"turn_source": "wake"}).want(200)
	ve.do("POST", "/api/v0/voice/command/stream",
		map[string]any{"voice_command": "what time is it", "conversation_id": "wake-x", "turn_source": "wake"}, other.h()).want(200)
}
