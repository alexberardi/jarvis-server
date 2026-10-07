package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// TestAmbientBundleVoice: opted in (ambient_context.enabled + memory.enabled), a voice
// conversation freezes the bundle at warmup and every turn carries it as the trailing
// <ambient_context> message after the speaker block; messages[0] never changes.
func TestAmbientBundleVoice(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO cc_user_memories (household_id, category, content) VALUES ('` + voiceHH + `', 'reminder', 'Reminders: refill the meds')`,
		`INSERT INTO cc_user_memories (household_id, category, content, expires_at) VALUES ('` + voiceHH + `', 'calendar', 'old', '2020-01-01T00:00:00.000Z')`,
		`INSERT INTO cc_user_memories (user_id, household_id, category, content) VALUES (7, '` + voiceHH + `', 'weather', 'personal, not household')`,
	} {
		if _, err := ve.d.Write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	ve.start("a0", weatherTool)
	if ve.m.convs.get("a0").ambient != "" {
		t.Fatal("ambient bundle without the opt-in")
	}
	base := ve.m.convs.get("a0").messages[0].Content

	ve.set(settingAmbientContext, true, settings.Scope{HouseholdID: voiceHH})
	ve.start("a1", weatherTool)
	conv := ve.m.convs.get("a1")
	// 12:00 UTC is 8:00 in New York (the node's zone); the clock is quantised to 15 min.
	want := "As of 8:00 AM, Tuesday, Oct 6.\nReminders: refill the meds"
	if conv.ambient != want || conv.messages[0].Content != base {
		t.Fatalf("ambient %q (system prompt changed: %v)", conv.ambient, conv.messages[0].Content != base)
	}
	ve.eng.say("Okay.")
	ve.turn("/api/v0/voice/command", "a1", "anything on today", nil).want(200)
	msgs := messagesOf(ve.eng.last())
	n := len(msgs)
	if msgs[n-3]["content"] != prompts.SpeakerBlock("default", "") || msgs[n-2]["content"] != prompts.AmbientBlock(want) ||
		!strings.HasPrefix(msgs[n-1]["content"].(string), "anything on today") {
		t.Fatalf("per-turn tail %v", msgs[n-3:])
	}
	// Memory off turns the bundle off too.
	ve.set(settingMemoryEnabled, false, settings.Scope{HouseholdID: voiceHH})
	ve.start("a2", weatherTool)
	if ve.m.convs.get("a2").ambient != "" {
		t.Fatal("ambient bundle with memory off")
	}
}
