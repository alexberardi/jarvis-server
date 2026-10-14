package cc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// setClock moves the env's injected clock to t.
func (e *env) setClock(t time.Time) {
	e.nmu.Lock()
	defer e.nmu.Unlock()
	e.now = t
}

// allContent is every message of an engine request joined, so a test can assert that no
// part of what the model sees carries a stale clock.
func allContent(req map[string]any) string {
	var b strings.Builder
	for _, m := range messagesOf(req) {
		if s, ok := m["content"].(string); ok {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestTimeAnswersAreExact is the regression for "what time is it?" at 21:07 answered "It's
// 9:00 PM" (and 20:11 → 8:00, 19:46 → 7:45, 19:28 → 7:15 on dev): the frozen ambient bundle
// carried a clock floored to the quarter hour and the model read it out instead of calling
// get_current_time. Now no frozen block holds a clock; each turn's <calendar> block carries
// the exact local minute, recomputed per turn (follow-ups and long-lived conversations
// included, across midnight too), and the cached prefix (messages[0]) never changes.
func TestTimeAnswersAreExact(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	hh := settings.Scope{HouseholdID: voiceHH}
	ve.set(settingAmbientContext, true, hh)
	if _, err := ve.d.Write.ExecContext(context.Background(),
		`INSERT INTO cc_user_memories (household_id, category, content) VALUES ('`+voiceHH+`', 'reminder', 'Reminders: refill the meds')`); err != nil {
		t.Fatal(err)
	}

	// Warmup at 21:02 New York (01:02Z), the question at 21:07.
	ve.setClock(time.Date(2026, 10, 14, 1, 2, 0, 0, time.UTC))
	ve.start("tm", weatherTool)
	conv := ve.m.convs.get("tm")
	if conv.ambient != "Reminders: refill the meds" {
		t.Fatalf("the frozen ambient bundle must carry no clock or date: %q", conv.ambient)
	}
	system := conv.messages[0].Content

	ve.setClock(time.Date(2026, 10, 14, 1, 7, 42, 0, time.UTC))
	ve.eng.say("It's 9:07 PM.")
	ve.turn("/api/v0/voice/command", "tm", "what time is it", nil).want(200)
	req := ve.eng.last()
	msgs := messagesOf(req)
	if msgs[0]["content"] != system {
		t.Fatal("the system prompt changed: the prefix cache would miss")
	}
	all := allContent(req)
	if !strings.Contains(all, "The current local time is 9:07 PM.") {
		t.Fatalf("no exact time in the turn:\n%s", all)
	}
	for _, stale := range []string{"9:00", "9:02", "As of"} {
		if strings.Contains(all, stale) {
			t.Fatalf("stale clock %q reached the model:\n%s", stale, all)
		}
	}

	// A follow-up four minutes later in the same (warm) conversation gets the new minute in
	// its own block; only the earlier reply in the history still says 9:07.
	ve.setClock(time.Date(2026, 10, 14, 1, 11, 5, 0, time.UTC))
	ve.eng.say("It's 9:11 PM.")
	ve.turn("/api/v0/voice/command", "tm", "and now", map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}).want(200)
	msgs = messagesOf(ve.eng.last())
	var blocks []string
	for _, m := range msgs {
		if s, _ := m["content"].(string); strings.HasPrefix(s, "<calendar>\n") {
			blocks = append(blocks, s)
		}
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0], "The current local time is 9:11 PM.") {
		t.Fatalf("follow-up calendar blocks %q", blocks)
	}
	if msgs[0]["content"] != system {
		t.Fatal("the system prompt changed on the follow-up")
	}

	// A conversation warmed before local midnight and asked after it rolls the date and the
	// time (the frozen bundle would still have said Tuesday).
	ve.setClock(time.Date(2026, 10, 14, 3, 58, 0, 0, time.UTC)) // 23:58 Tuesday in New York
	ve.start("mn", weatherTool)
	ve.setClock(time.Date(2026, 10, 14, 4, 3, 0, 0, time.UTC)) // 00:03 Wednesday
	ve.eng.say("It's 12:03 AM.")
	ve.turn("/api/v0/voice/command", "mn", "what time is it", nil).want(200)
	all = allContent(ve.eng.last())
	if !strings.Contains(all, "The current local time is 12:03 AM.") ||
		!strings.Contains(all, "Today is Wednesday, October 14, 2026.") {
		t.Fatalf("after midnight:\n%s", all)
	}
}
