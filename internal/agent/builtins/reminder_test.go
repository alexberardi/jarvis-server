package builtins

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// newYorkReminder is a Reminder in New York on Tuesday 2026-10-27 at noon, five days before
// DST ends (Sunday 2026-11-01).
func newYorkReminder(t *testing.T) (*Reminder, *time.Time) {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, loc)
	return &Reminder{Store: st, Loc: loc, Now: func() time.Time { return now }}, &now
}

// The deliberate difference from Python: a date past a DST change keeps its local time.
func TestReminderDatesKeepTheirLocalTimeAcrossDST(t *testing.T) {
	r, _ := newYorkReminder(t)
	for _, c := range []struct {
		keys  []string
		clock string
		want  string
	}{
		{[]string{"next_monday"}, "09:00", "2026-11-02T14:00:00+00:00"}, // 9 EST; Python: 13:00Z, 8 EST
		{[]string{"tomorrow"}, "09:00", "2026-10-28T13:00:00+00:00"},    // 9 EDT
		{[]string{"this_weekend"}, "", "2026-10-31T13:00:00+00:00"},     // Saturday, 9 by default
		{nil, "11:30", "2026-10-28T15:30:00+00:00"},                     // past today: tomorrow
	} {
		due, ok, err := r.resolveDue(c.keys, c.clock, 0)
		if got := isoformat(due); !ok || err != nil || got != c.want {
			t.Errorf("%v %q = %s, %v, %v; want %s", c.keys, c.clock, got, ok, err, c.want)
		}
	}
	// Recurring reminders advance on the local clock: 8 AM stays 8 AM after the change.
	at := func(s string) time.Time { v, _, _ := pyFromISO(s); return v }
	for _, c := range []struct{ due, rec, want string }{
		{"2026-10-31T12:00:00+00:00", "daily", "2026-11-01T13:00:00+00:00"},
		{"2026-10-30T12:00:00+00:00", "weekdays", "2026-11-02T13:00:00+00:00"}, // Friday to Monday
		{"2026-10-26T12:00:00+00:00", "weekly", "2026-11-02T13:00:00+00:00"},
		{"2026-01-31T13:00:00+00:00", "monthly", "2026-02-28T13:00:00+00:00"},
	} {
		if got := isoformat(r.next(at(c.due), c.rec)); got != c.want {
			t.Errorf("%s %s = %s; want %s", c.rec, c.due, got, c.want)
		}
	}
}

// Due fires each due reminder once, pushes it to its owner when the setting is on, and moves a
// recurring one on.
func TestReminderDuePushesAndAdvances(t *testing.T) {
	r, now := newYorkReminder(t)
	cc := &fakeCC{}
	client := cc.start(t)
	r.CC = func() *node.CC { return client }
	seven := int64(7)
	daily := "daily"
	r.save(reminderData{ReminderID: "rem_1", Text: "meds", DueAt: "2026-10-27T15:00:00+00:00", CreatedAt: "x", Recurrence: &daily, UserID: &seven})
	r.save(reminderData{ReminderID: "rem_2", Text: "later", DueAt: "2026-10-28T15:00:00+00:00", CreatedAt: "x"})
	if err := r.Store.SetSecret(reminderPushKey, "true", "integration", "bool", nil); err != nil {
		t.Fatal(err)
	}
	due, pending, err := r.Due(context.Background())
	if err != nil || pending != 2 || len(due) != 1 || due[0].Text != "meds" || *due[0].UserID != 7 {
		t.Fatalf("Due = %+v, %d, %v", due, pending, err)
	}
	if p := cc.last("/api/v0/node/push-notification"); p["body"] != "meds" || p["target_type"] != "user" || p["user_id"] != float64(7) {
		t.Errorf("push = %v", p)
	}
	o, _ := r.Store.Data(reminderStorage, "rem_1", *now)
	if d, _ := reminderOf(o); d.DueAt != "2026-10-28T15:00:00+00:00" || d.Announced || d.AnnounceCount != 1 {
		t.Errorf("after firing: %+v", d)
	}
	if again, _, _ := r.Due(context.Background()); len(again) != 0 {
		t.Errorf("fired again: %+v", again)
	}
}

// The app's edits go through update_reminder's rules, by way of the command-data handler.
func TestReminderEditsFromTheApp(t *testing.T) {
	r, now := newYorkReminder(t)
	seven := int64(7)
	last := "2026-10-27T15:00:00+00:00"
	r.save(reminderData{ReminderID: "rem_1", Text: "meds", DueAt: "2026-10-27T15:00:00+00:00", CreatedAt: "x",
		Announced: true, AnnounceCount: 3, LastAnnouncedAt: &last, UserID: &seven})
	cd := &nodesettings.CommandData{Store: r.Store, Commands: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return *now }}
	update := func(patch map[string]any, user any) map[string]any {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"correlation_id": "c", "command_name": "reminder", "key": "rem_1", "patch": patch,
			"requesting_user_id": user})
		_, body := cd.Handle(context.Background(), "jarvis/nodes/n1/command-data/update", b)
		var out map[string]any
		_ = json.Unmarshal(body, &out)
		return out
	}
	errOf := func(out map[string]any) any { e, _ := out["error"].(map[string]any); return e["message"] }
	for _, c := range []struct {
		patch map[string]any
		user  any
		want  string
	}{
		{map[string]any{"text": "  "}, 7, "text must be a non-empty string"},
		{map[string]any{"due_at": "tomorrow"}, 7, "due_at is not a valid ISO 8601 timestamp: tomorrow"},
		{map[string]any{"due_at": 5}, 7, "due_at must be an ISO 8601 string"},
		{map[string]any{"recurrence": "hourly"}, 7, "unknown recurrence: hourly"},
		{map[string]any{"text": "x"}, 9, "Reminder rem_1 not found"},
	} {
		if got := errOf(update(c.patch, c.user)); got != c.want {
			t.Errorf("%v as %v: %v; want %q", c.patch, c.user, got, c.want)
		}
	}
	out := update(map[string]any{"text": " pills ", "due_at": "2026-11-03T08:00:00", "recurrence": "daily", "announced": false}, 7)
	rec, _ := out["record"].(map[string]any)
	if out["ok"] != true || rec["text"] != "pills" || rec["due_at"] != "2026-11-03T08:00:00-05:00" || rec["recurrence"] != "daily" ||
		rec["announced"] != false || rec["announce_count"] != float64(0) || rec["last_announced_at"] != nil {
		t.Errorf("update = %v", out)
	}
	// The same recurrence again keeps the firing history.
	o, _ := r.Store.Data(reminderStorage, "rem_1", *now)
	d, _ := reminderOf(o)
	d.AnnounceCount = 2
	r.save(d)
	if rec, _ := update(map[string]any{"recurrence": "daily"}, 7)["record"].(map[string]any); rec["announce_count"] != float64(2) {
		t.Errorf("unchanged recurrence reset the history: %v", rec)
	}
}

func TestReminderHuman(t *testing.T) {
	r, _ := newYorkReminder(t)
	for in, want := range map[string]string{
		"2026-10-27T20:05:00+00:00": "today at 4:05 PM",
		"2026-10-28T13:00:00+00:00": "tomorrow at 9:00 AM",
		"2026-11-02T14:00:00+00:00": "Monday at 9:00 AM",
		"2026-11-04T14:00:00+00:00": "Nov 4 at 9:00 AM",
		"2026-10-27T20:05:00":       "today at 4:05 PM", // naive is UTC
		"soon":                      "soon",
	} {
		if got := r.human(in); got != want {
			t.Errorf("human(%s) = %q; want %q", in, got, want)
		}
	}
}
