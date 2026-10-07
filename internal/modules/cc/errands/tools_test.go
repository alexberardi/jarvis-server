package errands

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

func TestResolveFireAt(t *testing.T) {
	// Wednesday 2026-10-07 10:00 in New York (14:00 UTC).
	now := mustTime(t, "2026-10-07T14:00:00Z")
	ny := "America/New_York"
	cases := []struct{ in, want string }{
		{"tomorrow at 9am", "2026-10-08T13:00:00Z"},
		{"tonight at 7:51pm", "2026-10-07T23:51:00Z"},
		{"friday at noon", "2026-10-09T16:00:00Z"},
		{"wednesday", "2026-10-14T13:00:00Z"},      // the NEXT wednesday, default 9am
		{"this afternoon", "2026-10-07T16:00:00Z"}, // legacy: "noon" matches first
		{"at 8 PM", "2026-10-08T00:00:00Z"},
		{"2026-10-08T09:30:00Z", "2026-10-08T09:30:00Z"},
		{"2026-10-08T09:30", "2026-10-08T13:30:00Z"},
		{"2026-10-08 tomorrow at 7am", "2026-10-08T11:00:00Z"},
	}
	for _, c := range cases {
		got, ok := ResolveFireAt(c.in, ny, now)
		if !ok || got.Format(time.RFC3339) != c.want {
			t.Errorf("%q: got %v %v, want %s", c.in, got.Format(time.RFC3339), ok, c.want)
		}
	}
	if _, ok := ResolveFireAt("whenever", ny, now); ok {
		t.Error("no day and no time resolved")
	}
	// A bare ISO date at midnight falls through to phrase parsing (and has no day word).
	if _, ok := ResolveFireAt("2026-10-08", ny, now); ok {
		t.Error("bare date resolved")
	}
}

func TestBuildRecurrence(t *testing.T) {
	fire := mustTime(t, "2026-10-08T13:00:00Z") // Thursday 9:00 New York
	ny := "America/New_York"
	cases := map[string]string{
		"daily":            `{"type": "cron", "cron": "0 9 * * *"}`,
		"every day":        `{"type": "cron", "cron": "0 9 * * *"}`,
		"weekdays":         `{"type": "cron", "cron": "0 9 * * 1-5"}`,
		"weekly":           `{"type": "cron", "cron": "0 9 * * 4"}`,
		"monthly":          `{"type": "cron", "cron": "0 9 8 * *"}`,
		"hourly":           `{"type": "interval", "interval_seconds": 3600}`,
		"every 30 minutes": `{"type": "interval", "interval_seconds": 1800}`,
		"every 2 hours":    `{"type": "interval", "interval_seconds": 7200}`,
		"once":             "",
		"":                 "",
		"fortnightly-ish":  "",
	}
	for in, want := range cases {
		if got := BuildRecurrence(in, fire, ny); got != want {
			t.Errorf("%q: got %s want %s", in, got, want)
		}
	}
	next, ok := NextFire(`{"type": "cron", "cron": "0 9 * * *"}`, mustTime(t, "2026-10-08T14:00:00Z"), ny)
	if !ok || next.Format(time.RFC3339) != "2026-10-09T13:00:00Z" {
		t.Errorf("next cron = %v", next)
	}
	next, _ = NextFire(`{"type": "interval", "interval_seconds": 60}`, fire, ny)
	if !next.Equal(fire.Add(time.Minute)) {
		t.Errorf("next interval = %v", next)
	}
	if w := friendlyWhen(fire, ny); w != "Thursday at 9 AM" {
		t.Errorf("friendly = %q", w)
	}
	if w := friendlyWhen(mustTime(t, "2026-10-08T23:51:00Z"), ny); w != "Thursday at 7:51 PM" {
		t.Errorf("friendly = %q", w)
	}
}

type fakeSchedules struct {
	created []NewSchedule
	live    int
	posted  int
}

func (f *fakeSchedules) CreateSchedule(_ context.Context, s NewSchedule) (string, error) {
	f.created = append(f.created, s)
	return "sch_1", nil
}

func (f *fakeSchedules) PostListCard(context.Context, string, *int64) (int, error) {
	if f.live > 0 {
		f.posted++
	}
	return f.live, nil
}

func res(v any, err error) string {
	if err != nil {
		panic(err)
	}
	return pyjson.Dumps(v, false)
}

func turnFor(speaker int64) servertools.Turn {
	return servertools.Turn{ConversationID: "c1", HouseholdID: hh, NodeID: "node-1", Timezone: "America/New_York",
		Speaker: servertools.Speaker{UserID: speaker}}
}

func TestRunErrandTool(t *testing.T) {
	e := newTenv(t, false)
	svc := func() *Service { return e.s }
	tool := &RunErrandTool{Svc: svc, Commands: func(id string) []*pyjson.Object {
		return []*pyjson.Object{mustObj(t, `{"command_name": "get_weather", "description": "W"}`)}
	}}
	ctx := context.Background()
	got := res(tool.Execute(ctx, servertools.Call{Args: mustObj(t, `{"goal": "  "}`)}, turnFor(7)))
	if got != `{"error": "missing_goal", "message": "I need to know what the errand should do."}` {
		t.Fatal(got)
	}
	got = res(tool.Execute(ctx, servertools.Call{Args: mustObj(t, `{"goal": "check weather"}`)}, turnFor(7)))
	if got != `{"status": "accepted", "message": "On it — I'm putting together a plan for that errand and I'll send it to your phone to review. Tap Run when you're ready."}` {
		t.Fatal(got)
	}
	var payload string
	_ = e.d.Read.QueryRow(`SELECT payload FROM platform_jobs WHERE type = ?`, JobPlan).Scan(&payload)
	if payload != `{"household_id":"hh-1","node_id":"node-1","user_id":7,"goal":"check weather","menu":[{"command":"get_weather","description":"W","args":{},"is_risky":false}]}` {
		t.Fatalf("payload = %s", payload)
	}
	e.set[SettingEnabled] = false
	got = res(tool.Execute(ctx, servertools.Call{Args: mustObj(t, `{"goal": "x"}`)}, turnFor(0)))
	if got != `{"error": "errands_disabled", "message": "Errands are turned off for this household."}` {
		t.Fatal(got)
	}
}

func TestScheduleErrandTool(t *testing.T) {
	e := newTenv(t, false)
	sch := &fakeSchedules{}
	e.s.Schedules = sch
	e.s.Now = func() time.Time { return mustTime(t, "2026-10-07T14:00:00Z") }
	tool := &ScheduleErrandTool{Svc: func() *Service { return e.s }}
	ctx := context.Background()
	call := func(args string) string {
		return res(tool.Execute(ctx, servertools.Call{Args: mustObj(t, args)}, turnFor(7)))
	}
	if got := call(`{"goal": "call the dentist", "fire_at": "tomorrow at 9am"}`); got != `{"status": "accepted", "message": "Okay — I'll get to that Thursday at 9 AM. I'll send a plan to your phone to approve when the time comes."}` {
		t.Fatal(got)
	}
	c := sch.created[0]
	if c.Intent != "call the dentist" || c.FireAt.Format(time.RFC3339) != "2026-10-08T13:00:00Z" || c.Timezone != "America/New_York" ||
		c.Recurrence != "" || *c.UserID != 7 || c.NodeID != "node-1" {
		t.Fatalf("created = %+v", c)
	}
	// A past daily time rolls forward.
	if got := call(`{"goal": "check traffic", "fire_at": "today at 8am", "recurrence": "daily"}`); got != `{"status": "accepted", "message": "Okay — I'll do that every day, starting Thursday at 8 AM. Each time, I'll send a plan to your phone to approve first."}` {
		t.Fatal(got)
	}
	if got := call(`{"goal": "x", "fire_at": "today at 8am"}`); got != `{"error": "past_time", "message": "That time has already passed — give me a future time."}` {
		t.Fatal(got)
	}
	if got := call(`{"goal": "x", "fire_at": "sometime"}`); got != `{"error": "bad_time", "message": "I couldn't work out when to run that — tell me a day and a time, like 'tomorrow at 9am'."}` {
		t.Fatal(got)
	}
	if got := call(`{"goal": "x", "fire_at": "sometime", "recurrence": "every 2 hours"}`); got != `{"status": "accepted", "message": "Okay — I'll do that every 2 hours, starting Wednesday at 12 PM. Each time, I'll send a plan to your phone to approve first."}` {
		t.Fatal(got)
	}
}

func TestListScheduledErrandsTool(t *testing.T) {
	e := newTenv(t, false)
	sch := &fakeSchedules{}
	e.s.Schedules = sch
	tool := &ListScheduledErrandsTool{Svc: func() *Service { return e.s }}
	ctx := context.Background()
	for live, want := range map[int]string{
		0: `{"status": "ok", "message": "You don't have any errands scheduled right now."}`,
		1: `{"status": "ok", "message": "You have one errand scheduled. I've put it on your phone, where you can cancel it."}`,
		3: `{"status": "ok", "message": "You have 3 errands scheduled. I've sent the full list to your phone, where you can cancel any of them."}`,
	} {
		sch.live = live
		if got := res(tool.Execute(ctx, servertools.Call{}, turnFor(0))); got != want {
			t.Errorf("%d: %s", live, got)
		}
	}
	e.s.Schedules = nil
	if got := res(tool.Execute(ctx, servertools.Call{}, turnFor(0))); got != `{"error": "unavailable", "message": "I can't check your scheduled errands right now."}` {
		t.Fatal(got)
	}
}
