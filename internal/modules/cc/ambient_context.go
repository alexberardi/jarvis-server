package cc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The ambient bundle (docs/cc/01 §3.2, 03 §3.3 item 2, 10 §3): the household's situational
// snapshot — a 15-minute-quantised clock, the latest household weather / calendar / reminder
// memories and the live Signals — assembled once at warmup and frozen on the conversation.
// Every turn (voice and mobile chat alike) re-wraps it as the trailing <ambient_context>
// system message after the speaker block, never in messages[0], so the cached prefix stays
// byte-stable. Opt-in: ambient_context.enabled AND memory.enabled for the household
// (legacy conversation_handler._assemble_ambient_bundle, :471 and :3192-3268).

// ambientCategories are the household memory categories the bundle reads, with their labels.
var ambientCategories = [...]struct{ category, label string }{
	{"weather", "Weather"}, {"calendar", "Today"}, {"reminder", "Reminders"},
}

// ambientBundle returns the frozen bundle, or "" when the feature is off or nothing renders.
// Failures fail open to "" (never break warmup).
func (m *Module) ambientBundle(ctx context.Context, hh, tz string) string {
	if hh == "" {
		return ""
	}
	sc := settings.Scope{HouseholdID: hh}
	if !m.settings.Bool(ctx, settingMemoryEnabled, sc) || !m.settings.Bool(ctx, settingAmbientContext, sc) {
		return ""
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || tz == "" {
		loc = time.UTC
	}
	now := m.now().In(loc)
	clock := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), now.Minute()/15*15, 0, 0, loc)
	lines := []string{fmt.Sprintf("As of %s, %s.", clock.Format("3:04 PM"), clock.Format("Monday, Jan 2"))}

	for _, c := range ambientCategories {
		content, err := m.latestHouseholdMemory(ctx, hh, c.category)
		if err != nil {
			m.deps.Log.Warn("cc: ambient bundle unavailable", "household", hh, "err", err)
			return ""
		}
		content = parse.PyStrip(content)
		if content == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(content), strings.ToLower(c.label)) {
			lines = append(lines, content)
		} else {
			lines = append(lines, c.label+": "+content)
		}
	}
	if sig := m.SignalContext(ctx, hh); sig != "" {
		lines = append(lines, sig)
	}
	return strings.Join(lines, "\n")
}

// latestHouseholdMemory is the newest live household (user_id NULL) memory of a category.
// Weather prefers the "Current weather …" row: the agent also writes a newer "Tomorrow's
// forecast …" row that would otherwise win and describe the wrong day.
func (m *Module) latestHouseholdMemory(ctx context.Context, hh, category string) (string, error) {
	where, args := memScope{HouseholdID: hh, Category: category}.where(m.now())
	pick := func(extra string) (string, error) {
		var s string
		err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT content FROM cc_user_memories WHERE `+where+extra+
			` ORDER BY updated_at DESC, id DESC LIMIT 1`, args...).Scan(&s)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return s, err
	}
	if category == "weather" {
		s, err := pick(` AND content LIKE 'current weather%'`) // SQLite LIKE: ASCII case-insensitive (ilike)
		if err != nil || s != "" {
			return s, err
		}
	}
	return pick("")
}
