package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
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
	ve := newVoiceEnv(t, prompts.Cloud, expiredWindow)
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
	ve := newVoiceEnv(t, prompts.Cloud, expiredWindow)
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

// A follow-up that its own gate drops never silences the household: node A's follow-up window
// is open when someone else says "Hey Jarvis, what time is it" at node B; A's mic hears it as
// a different, far voice and drops it, and B answers.
func TestGateDroppedFollowUpDoesNotSilenceTheWake(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B, expiredWindow)
	gateConv(t, ve)
	other := ve.createNode("node-v2", voiceHH)
	ve.do("POST", "/api/v0/conversation/start",
		`{"conversation_id":"wake-b","node_context":{"timezone":"America/New_York","speaker_user_id":7},"client_tools":[]}`, other.h()).want(200)
	ve.eng.say("It's noon.")
	said := len(ve.tts.said())

	ve.stt.voices = []stt.Voice{voiceAt(0, 1)} // a different voice from the one that woke A
	ve.transcribe("c1", true)
	a := followUp(ve, "hey jarvis what time is it", map[string]any{"level_vs_wake_db": -14.5})
	if a["stop_reason"] != "not_for_me" || a["keep_listening"] != true {
		t.Fatalf("A's gate must drop the far voice: %v", a)
	}
	b := ve.do("POST", "/api/v0/voice/command/stream",
		map[string]any{"voice_command": "Hey Jarvis, what time is it?", "conversation_id": "wake-b", "turn_source": "wake"}, other.h())
	if b.status != 200 {
		t.Fatalf("B's wake must answer: %d %s", b.status, b.body)
	}
	if got := ve.tts.said()[said:]; len(got) != 1 {
		t.Fatalf("want 1 spoken reply, got %q", got)
	}
}

// With wake verification enforced, a node whose wake clip is unverified drops its own turn and
// the node whose wake is verified answers.
func TestUnverifiedWakeDoesNotSilenceTheVerifiedOne(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B, expiredWindow)
	ve.set(settingWakeMode, "enforce", settings.Scope{HouseholdID: voiceHH})
	other := ve.createNode("node-v2", voiceHH)
	ve.start("wake-a", "[]")
	ve.do("POST", "/api/v0/conversation/start",
		`{"conversation_id":"wake-b","node_context":{"timezone":"America/New_York","speaker_user_id":7},"client_tools":[]}`, other.h()).want(200)
	ve.stt.wakeText = "the weather"
	ve.transcribe("wake-a", true)
	ve.awaitWake("wake-a")
	ve.stt.wakeText = "hey jervis"
	ve.transcribeOn(other, "wake-b", true)
	ve.awaitWake("wake-b")
	ve.eng.say("It's noon.")
	said := len(ve.tts.said())

	a := ve.turn("/api/v0/voice/command/stream", "wake-a", "what time is it", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(202).json()
	if a["stop_reason"] != "not_for_me" || a["stood_down"] == true {
		t.Fatalf("A's unverified wake must be dropped by its own gate: %v", a)
	}
	b := ve.do("POST", "/api/v0/voice/command/stream",
		map[string]any{"voice_command": "What time is it?", "conversation_id": "wake-b", "turn_source": "wake", "wake_confidence": 0.8}, other.h())
	if b.status != 200 {
		t.Fatalf("B's verified wake must answer: %d %s", b.status, b.body)
	}
	if got := ve.tts.said()[said:]; len(got) != 1 {
		t.Fatalf("want 1 spoken reply, got %q", got)
	}
}
