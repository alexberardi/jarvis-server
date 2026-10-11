package cc

import (
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The follow-up gate (followup_gate.go): a rejected follow-up is silent, leaves no trace in the
// conversation and keeps the node listening; only a different voice from far away is dropped
// before the model.

// voiceAt is a usable voice whose similarity to voiceAt(1, 0) is a (unit vectors in 2-D).
func voiceAt(a, b float32) stt.Voice {
	return stt.Voice{Vec: []float32{a, b}, SpeechSecs: 2, ModelID: "m"}
}

// gateConv opens c1 with an answered wake turn, its transcription giving the reference voice.
func gateConv(t *testing.T, ve *voiceEnv) {
	t.Helper()
	ve.start("c1", "[]")
	ve.stt.voices = []stt.Voice{voiceAt(1, 0)}
	ve.transcribe("c1", true)
	ve.eng.say("It's 66 and overcast.")
	ve.turn("/api/v0/voice/command", "c1", "what's the weather", map[string]any{"turn_source": "wake", "wake_confidence": 0.9}).want(200)
}

func followUp(ve *voiceEnv, text string, extra map[string]any) map[string]any {
	b := map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}
	for k, v := range extra {
		b[k] = v
	}
	return ve.turn("/api/v0/voice/command", "c1", text, b).want(200).json()
}

func historyHas(ve *voiceEnv, s string) bool {
	conv := ve.m.convs.get("c1")
	conv.mu.Lock()
	defer conv.mu.Unlock()
	for _, m := range conv.messages {
		if strings.Contains(m.Content, s) {
			return true
		}
	}
	return false
}

func TestFollowUpGateModelRejectionIsSilentAndUncommitted(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	gateConv(t, ve)
	rounds := ve.m.convs.get("c1").answeredRounds

	ve.stt.voices = []stt.Voice{voiceAt(1, 0)} // the same voice: the model decides
	ve.transcribe("c1", true)
	ve.eng.say("<not_for_me/>")
	ve.eng.say("<not_for_me/>") // the double-check pass (same voice keeps it)
	b := followUp(ve, "pass me the remote", nil)
	if b["stop_reason"] != "not_for_me" || b["keep_listening"] != true || b["assistant_message"] != nil {
		t.Fatalf("body %v", b)
	}
	if historyHas(ve, "pass me the remote") || len(ve.m.convs.get("c1").messages) != 4 {
		t.Fatal("a rejected follow-up must not enter the history")
	}
	if got := ve.m.convs.get("c1").answeredRounds; got != rounds {
		t.Fatalf("answered rounds %d -> %d", rounds, got)
	}

	// The next follow-up's prompt carries the wake exchange only.
	ve.transcribe("c1", true)
	ve.eng.say("Yes, bring an umbrella.")
	b = followUp(ve, "do I need an umbrella", nil)
	if b["assistant_message"] != "Yes, bring an umbrella." {
		t.Fatalf("body %v", b)
	}
	if _, has := b["keep_listening"]; has {
		t.Fatalf("keep_listening only on rejections: %v", b)
	}
	for _, m := range messagesOf(ve.eng.last()) {
		if s, _ := m["content"].(string); strings.Contains(s, "remote") || strings.Contains(s, "not_for_me/>") && m["role"] == "assistant" {
			t.Fatalf("rejected follow-up leaked into the prompt: %v", m)
		}
	}
	if !historyHas(ve, "do I need an umbrella") {
		t.Fatal("an answered follow-up is committed")
	}
}

func TestFollowUpGateDropsADifferentFarVoiceBeforeTheModel(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	gateConv(t, ve)
	calls := len(ve.eng.requests())

	ve.stt.voices = []stt.Voice{voiceAt(0, 1)} // similarity 0: a different voice
	ve.transcribe("c1", true)
	b := followUp(ve, "police say the suspect was last seen heading north", map[string]any{"level_vs_wake_db": -14.5})
	if b["stop_reason"] != "not_for_me" || b["keep_listening"] != true {
		t.Fatalf("body %v", b)
	}
	if n := len(ve.eng.requests()); n != calls {
		t.Fatalf("the model was called %d times for an acoustic rejection", n-calls)
	}
	if historyHas(ve, "suspect") {
		t.Fatal("rejected follow-up committed")
	}
}

func TestFollowUpGateHintsTheModelAndSkipsTheDoubleCheck(t *testing.T) {
	cases := []struct {
		name      string
		voice     stt.Voice
		level     any
		hint      string // "" = none
		modelRuns int    // LLM calls for a <not_for_me/> answer
	}{
		{"different voice, near", voiceAt(0, 1), -2.0, "does not match the person who woke you", 1},
		{"unknown voice, far", stt.Voice{}, -12.0, "much quieter than the wake request", 1},
		{"same voice, far", voiceAt(1, 0), -12.0, "", 2},
		{"unsure voice, near", voiceAt(0.3, 0.954), nil, "", 2},
		{"nothing known", stt.Voice{}, nil, "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := newVoiceEnv(t, prompts.Qwen3_8B)
			gateConv(t, ve)
			before := len(ve.eng.requests())
			ve.stt.voices = []stt.Voice{c.voice}
			ve.transcribe("c1", true)
			ve.eng.say("<not_for_me/>")
			ve.eng.say("<not_for_me/>")
			extra := map[string]any{}
			if c.level != nil {
				extra["level_vs_wake_db"] = c.level
			}
			b := followUp(ve, "so anyway she said the meeting moved", extra)
			if b["stop_reason"] != "not_for_me" || b["keep_listening"] != true {
				t.Fatalf("body %v", b)
			}
			reqs := ve.eng.requests()[before:]
			if len(reqs) != c.modelRuns {
				t.Fatalf("%d model calls, want %d", len(reqs), c.modelRuns)
			}
			msgs := messagesOf(reqs[0])
			user, _ := msgs[len(msgs)-1]["content"].(string)
			has := strings.Contains(user, "[voice check:")
			if c.hint == "" && has || c.hint != "" && !strings.Contains(user, c.hint) {
				t.Fatalf("user message %q, want hint %q", user, c.hint)
			}
		})
	}
}

func TestFollowUpGateClosesTheWindowAfterMaxIgnored(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	gateConv(t, ve)
	ve.set(settingFollowUpMaxIgnored, int64(2), settings.Scope{})
	far := map[string]any{"level_vs_wake_db": -20.0}
	for i, want := range []any{true, true, nil} {
		ve.stt.voices = []stt.Voice{voiceAt(0, 1)}
		ve.transcribe("c1", true)
		b := followUp(ve, "and the score is now three to one", far)
		if b["stop_reason"] != "not_for_me" || b["keep_listening"] != want {
			t.Fatalf("rejection %d: %v", i+1, b)
		}
	}
	// An answered follow-up resets the count.
	ve.transcribe("c1", true)
	ve.eng.say("Tomorrow looks sunny.")
	followUp(ve, "what about tomorrow", nil)
	if n := ve.m.convs.get("c1").ignoredFollowUps; n != 0 {
		t.Fatalf("ignored count %d after an answer", n)
	}
}

func TestFollowUpGateOffKeepsTheOldBehaviour(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.set(settingFollowUpGate, false, settings.Scope{})
	gateConv(t, ve)
	if len(ve.stt.voiceClips) != 0 {
		t.Fatal("no voice check with the gate off")
	}
	ve.eng.say("<not_for_me/>")
	ve.eng.say("<not_for_me/>")
	b := followUp(ve, "pass me the remote", map[string]any{"level_vs_wake_db": -20.0})
	if b["stop_reason"] != "not_for_me" {
		t.Fatalf("body %v", b)
	}
	if _, has := b["keep_listening"]; has {
		t.Fatalf("no keep_listening with the gate off: %v", b)
	}
	if !historyHas(ve, "pass me the remote") {
		t.Fatal("gate off: the not_for_me turn is committed as before")
	}
}

func TestFollowUpGateVoiceReferenceAndComparison(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c1", "[]")
	ve.stt.voices = []stt.Voice{voiceAt(1, 0), voiceAt(0.6, 0.8), {SpeechSecs: 0.3, ModelID: "m"}}
	ve.transcribe("c1", true) // the wake: speaker_audio is the reference
	ve.transcribe("c1", true) // a follow-up: its own audio is compared
	if len(ve.stt.voiceClips) != 2 || len(ve.stt.voiceClips[0]) <= len(ve.stt.voiceClips[1]) {
		t.Fatalf("the reference embeds speaker_audio, a follow-up the transcribed file: %d clips", len(ve.stt.voiceClips))
	}
	sim := ve.m.signals.takeVoiceMatch("c1")
	if sim == nil || *sim < 0.59 || *sim > 0.61 {
		t.Fatalf("similarity %v", sim)
	}
	if ve.m.signals.takeVoiceMatch("c1") != nil {
		t.Fatal("a comparison is consumed once")
	}
	ve.transcribe("c1", true) // too little speech: no comparison
	if ve.m.signals.takeVoiceMatch("c1") != nil {
		t.Fatal("an unusable clip gives no comparison")
	}
}
