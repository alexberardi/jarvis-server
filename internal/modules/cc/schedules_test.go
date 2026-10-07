package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

func TestDescribeRecurrence(t *testing.T) {
	// tests/test_schedule_service.py::test_describe_recurrence_phrases, plus the quirks (§8.15).
	cases := map[string]string{
		"":                                      "once",
		`{"type": "cron", "cron": "0 9 * * *"}`: "every day at 9:00 AM",
		`{"type": "cron", "cron": "30 8 * * 1-5"}`:       "every weekday at 8:30 AM",
		`{"type": "cron", "cron": "0 14 * * 1"}`:         "every Monday at 2:00 PM",
		`{"type": "cron", "cron": "0 9 * * 7"}`:          "every Sunday at 9:00 AM",
		`{"type": "cron", "cron": "0 9 5 * *"}`:          "monthly on day 5 at 9:00 AM",
		`{"type": "cron", "cron": "0 0 * * *"}`:          "every day at 12:00 AM",
		`{"type": "cron", "cron": "0 12 * * *"}`:         "every day at 12:00 PM",
		`{"type": "cron", "cron": "*/5 * * * *"}`:        "every day",
		`{"type": "cron", "cron": "0 9 * * 1,3,5"}`:      "every day at 9:00 AM", // multi-day reads as every day (kept)
		`{"type": "cron", "cron": "0 9 * *"}`:            "repeating",
		`{"type": "interval", "interval_seconds": 3600}`: "every hour",
		`{"type": "interval", "interval_seconds": 7200}`: "every 2 hours",
		`{"type": "interval", "interval_seconds": 1800}`: "every 30 minutes",
		`{"type": "interval", "interval_seconds": 60}`:   "every minute",
		`{"type": "interval", "interval_seconds": 90}`:   "on a repeating interval",
		`{"type": "weekly"}`:                             "repeating",
		"{bad json":                                      "repeating",
	}
	for rec, want := range cases {
		var v any
		if rec != "" {
			v = rec
		}
		if got := describeRecurrence(v); got != want {
			t.Errorf("%s: %q, want %q", rec, got, want)
		}
	}
}

func TestLocalWhen(t *testing.T) {
	cases := []struct{ iso, tz, want string }{
		{"2026-08-05T13:00:00", "America/New_York", "Wed Aug 5, 9 AM"},
		{"2026-08-05T14:00:00", "UTC", "Wed Aug 5, 2 PM"},
		{"2026-08-05T14:00:00", "", "Wed Aug 5, 2 PM"},
		{"2026-08-05T14:00:00", "Mars/Base", "Wed Aug 5, 2 PM"}, // unknown zone → UTC
		{"2026-08-05T14:30:00", "UTC", "Wed Aug 5, 2:30 PM"},
		{"2026-12-25T10:05:00", "UTC", "Fri Dec 25, 10:05 AM"},
		{"2026-08-05T10:00:00", "UTC", "Wed Aug 5, 10 AM"}, // ":00" stripped everywhere (§7.10)
		{"", "UTC", ""},
		{"garbage", "UTC", ""},
	}
	for _, c := range cases {
		if got := localWhen(c.iso, c.tz); got != c.want {
			t.Errorf("%s in %q: %q, want %q", c.iso, c.tz, got, c.want)
		}
	}
	s := map[string]any{"intent": " check the weather ", "recurrence": `{"type":"cron","cron":"0 9 * * *"}`,
		"next_fire_at": "2026-08-05T13:00:00", "timezone": "America/New_York"}
	if got := describeSchedule(s); got != "check the weather · every day at 9:00 AM · next Wed Aug 5, 9 AM" {
		t.Errorf("describe %q", got)
	}
	if got := describeSchedule(map[string]any{"intent": nil, "recurrence": nil}); got != "an errand · once" {
		t.Errorf("describe bare %q", got)
	}
}

func TestNextScheduleFire(t *testing.T) {
	after := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	if n, ok := nextScheduleFire(`{"type":"interval","interval_seconds":3600}`, after, "UTC"); !ok || !n.Equal(after.Add(time.Hour)) {
		t.Errorf("interval %v %v", n, ok)
	}
	if n, ok := nextScheduleFire(`{"type":"cron","cron":"0 9 * * *"}`, after, "UTC"); !ok || !n.Equal(time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("cron %v %v", n, ok)
	}
	// Cron in the node's zone, across the fall-back change: 9am New York stays 9am local.
	ny := time.Date(2026, 10, 31, 14, 0, 0, 0, time.UTC) // Sat 10:00 EDT
	n, ok := nextScheduleFire(`{"type":"cron","cron":"0 9 * * *"}`, ny, "America/New_York")
	if !ok || !n.Equal(time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)) { // Sun 09:00 EST
		t.Errorf("DST cron %v %v", n, ok)
	}
	for _, junk := range []string{"", "{bad", `{"type":"interval","interval_seconds":0}`, `{"type":"cron","cron":"nope"}`, `{"type":"weekly"}`} {
		if _, ok := nextScheduleFire(junk, after, "UTC"); ok {
			t.Errorf("%q re-armed", junk)
		}
	}
}

// schedEnv adds the errand hook recorder to a routine env.
type schedEnv struct {
	*routineEnv
	fired []ScheduledErrand
}

func newSchedEnv(t *testing.T) *schedEnv {
	se := &schedEnv{routineEnv: newRoutineEnv(t)}
	se.m.rt.scheduleFire = func(_ context.Context, s ScheduledErrand) error {
		se.fired = append(se.fired, s)
		return nil
	}
	return se
}

func (se *schedEnv) createSchedule(intent, rec string, at time.Time, user *int64) string {
	se.t.Helper()
	id, err := se.m.CreateSchedule(context.Background(), NewSchedule{HouseholdID: rhh, NodeID: "n1", UserID: user,
		Intent: intent, FireAt: at, Timezone: "America/New_York", Recurrence: rec})
	if err != nil {
		se.t.Fatal(err)
	}
	return id
}

func (se *schedEnv) fireSchedule(id string) {
	se.t.Helper()
	inner, _ := json.Marshal(map[string]string{"schedule_id": id})
	p, _ := json.Marshal(scheduler.Fire{Trigger: scheduleTrigger(id), ScheduledAt: se.clock(), Payload: inner})
	if _, err := se.m.runScheduleFire(context.Background(), queue.Job{Type: scheduleFireJob, Payload: p}); err != nil {
		se.t.Fatal(err)
	}
}

func (se *schedEnv) schedRow(id string) *scheduleRow {
	se.t.Helper()
	r, err := scanSchedule(se.d.Read.QueryRow(`SELECT `+scheduleCols+` FROM cc_schedules WHERE id = ?`, id))
	if err != nil {
		se.t.Fatal(err)
	}
	return r
}

func TestScheduleOneShotFire(t *testing.T) {
	e := newSchedEnv(t)
	uid := int64(1)
	id := e.createSchedule("check my refill", "", e.clock().Add(time.Hour), &uid)
	if !strings.HasPrefix(id, "sch_") || len(id) != 36 {
		t.Fatalf("id %q", id)
	}
	st, err := e.sched.Status(context.Background(), scheduleTrigger(id))
	if err != nil || !st.NextFireAt.Equal(e.clock().Add(time.Hour)) {
		t.Fatalf("trigger %v %v", st, err)
	}
	e.advance(time.Hour)
	e.fireSchedule(id)
	if len(e.fired) != 1 || e.fired[0].Intent != "check my refill" || e.fired[0].HouseholdID != rhh || *e.fired[0].UserID != 1 ||
		e.fired[0].NodeID != "n1" || e.fired[0].Timezone != "America/New_York" {
		t.Fatalf("hook %+v", e.fired)
	}
	if r := e.schedRow(id); r.state != "done" || r.lastFiredAt.String != "2026-10-06T13:00:00.000Z" {
		t.Fatalf("row %+v", r)
	}
	e.fireSchedule(id) // a second delivery is a no-op (the claim)
	if len(e.fired) != 1 {
		t.Fatal("fired twice")
	}
	// No errands hook yet: the fire is claimed and dropped, never looped.
	e.m.rt.scheduleFire = nil
	id2 := e.createSchedule("later", "", e.clock(), nil)
	e.fireSchedule(id2)
	if e.schedRow(id2).state != "done" {
		t.Fatal("not claimed without a hook")
	}
}

func TestScheduleRecurringFire(t *testing.T) {
	e := newSchedEnv(t)
	// 2026-10-06 12:00Z is 08:00 EDT; a daily 9am New York errand.
	first := time.Date(2026, 10, 6, 13, 0, 0, 0, time.UTC)
	id := e.createSchedule("check traffic", `{"type":"cron","cron":"0 9 * * *"}`, first, nil)
	e.advance(time.Hour)
	e.fireSchedule(id)
	r := e.schedRow(id)
	if r.state != "active" || r.nextFireAt != "2026-10-07T13:00:00.000Z" || r.lastFiredAt.String != "2026-10-06T13:00:00.000Z" {
		t.Fatalf("re-arm %+v", r)
	}
	st, _ := e.sched.Status(context.Background(), scheduleTrigger(id))
	if !st.NextFireAt.Equal(time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("trigger %v", st.NextFireAt)
	}
	e.fireSchedule(id) // not due yet: the claim refuses it
	if len(e.fired) != 1 {
		t.Fatalf("fired %d", len(e.fired))
	}
	// Missed for three days: one late fire, then the next occurrence after now (D26).
	e.advance(3*24*time.Hour + 5*time.Hour)
	e.fireSchedule(id)
	if r := e.schedRow(id); len(e.fired) != 2 || r.nextFireAt != "2026-10-10T13:00:00.000Z" {
		t.Fatalf("late fire %d %+v", len(e.fired), r)
	}
	// A recurrence that stops parsing degrades to a one-shot (§8.14).
	if _, err := e.d.Write.Exec(`UPDATE cc_schedules SET recurrence = '{bad', next_fire_at = ? WHERE id = ?`, dbTime(e.clock()), id); err != nil {
		t.Fatal(err)
	}
	e.fireSchedule(id)
	if e.schedRow(id).state != "done" {
		t.Fatal("junk recurrence did not finish")
	}
}

func TestScheduleReconcileAndPurge(t *testing.T) {
	e := newSchedEnv(t)
	at := e.clock().Add(-time.Hour) // missed while down
	id := e.createSchedule("x", "", at, nil)
	if err := e.sched.Delete(context.Background(), scheduleTrigger(id)); err != nil {
		t.Fatal(err)
	}
	if err := e.m.startRoutines(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := e.sched.Status(context.Background(), scheduleTrigger(id))
	if err != nil || !st.NextFireAt.Equal(at) {
		t.Fatalf("reconcile %v %v", st, err)
	}
	if _, err := e.sched.Status(context.Background(), schedulePurgeJob); err != nil {
		t.Fatalf("purge trigger: %v", err)
	}

	old := e.createSchedule("old", "", e.clock(), nil)
	if _, err := e.m.cancelSchedule(context.Background(), old, rhh); err != nil {
		t.Fatal(err)
	}
	e.advance(31 * 24 * time.Hour)
	recent := e.createSchedule("recent", "", e.clock(), nil)
	e.fireSchedule(recent) // done just now
	if _, err := e.m.runSchedulePurge(context.Background(), queue.Job{}); err != nil {
		t.Fatal(err)
	}
	var n int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_schedules WHERE id = ?`, old).Scan(&n)
	if n != 0 {
		t.Fatal("terminal schedule older than 30 days kept")
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_schedules WHERE id IN (?, ?)`, recent, id).Scan(&n)
	if n != 2 {
		t.Fatalf("purged too much: %d left", n)
	}
}

func TestMobileSchedules(t *testing.T) {
	e := newSchedEnv(t)
	outsider := e.auth.addUser(2, "hh-other", authn.RoleOwner)
	member := e.auth.addUser(3, rhh, authn.RoleMember)
	weather := e.createSchedule("check the weather", `{"type":"cron","cron":"0 9 * * *"}`, time.Date(2026, 10, 7, 13, 0, 0, 0, time.UTC), nil)
	e.createSchedule("dentist", "", time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC), nil)
	done := e.createSchedule("finished", "", e.clock(), nil)
	e.fireSchedule(done)
	foreign, _ := e.m.CreateSchedule(context.Background(), NewSchedule{HouseholdID: "hh-other", Intent: "theirs", FireAt: e.clock().Add(time.Hour)})

	base := "/api/v0/mobile/household/" + rhh + "/schedules"
	got := e.do("GET", base, nil, bearer(member)).want(200).json()
	list := got["schedules"].([]any)
	if got["household_id"] != rhh || len(list) != 2 {
		t.Fatalf("list %v", got)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["intent"] != "dentist" || first["is_recurring"] != false || first["cadence"] != "once" ||
		first["next_local"] != "Tue Oct 6, 4 PM" || first["next_fire_at"] != "2026-10-06T20:00:00" || first["recurrence"] != nil ||
		first["state"] != "active" || first["last_fired_at"] != nil || first["timezone"] != "America/New_York" {
		t.Fatalf("one-shot view %v", first)
	}
	if second["id"] != weather || second["is_recurring"] != true || second["cadence"] != "every day at 9:00 AM" ||
		second["description"] != "check the weather · every day at 9:00 AM · next Wed Oct 7, 9 AM" ||
		second["recurrence"] != `{"type":"cron","cron":"0 9 * * *"}` {
		t.Fatalf("recurring view %v", second)
	}

	r := e.do("POST", base+"/"+weather+"/cancel", nil, bearer(member)).want(200).json()
	if r["cancelled"] != true || len(r["schedules"].([]any)) != 1 {
		t.Fatalf("cancel %v", r)
	}
	if _, err := e.sched.Status(context.Background(), scheduleTrigger(weather)); err != sql.ErrNoRows {
		t.Fatalf("cancelled schedule kept its trigger: %v", err)
	}
	if r := e.do("POST", base+"/"+weather+"/cancel", nil, bearer(member)).want(200).json(); r["cancelled"] != false {
		t.Fatalf("idempotent cancel %v", r)
	}
	// Another household's schedule can't be cancelled through this one.
	if r := e.do("POST", base+"/"+foreign+"/cancel", nil, bearer(member)).want(200).json(); r["cancelled"] != false {
		t.Fatalf("cross-household cancel %v", r)
	}
	e.do("GET", base, nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("POST", base+"/"+weather+"/cancel", nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("GET", base, nil, nil).detail(401, "Missing or invalid Authorization header")
}

func TestScheduleListCard(t *testing.T) {
	e := newSchedEnv(t)
	uid := int64(1)
	if n, item, err := e.m.postScheduleListCard(context.Background(), rhh, &uid); n != 0 || item != "" || err != nil {
		t.Fatalf("empty: %d %q %v", n, item, err)
	}
	for i := range 10 {
		e.createSchedule(fmt.Sprintf("errand number %d with a very long intent text", i), "", e.clock().Add(time.Duration(i+1)*time.Hour), nil)
	}
	n, item, err := e.m.postScheduleListCard(context.Background(), rhh, &uid)
	if err != nil || n != 10 || item == "" {
		t.Fatalf("card %d %q %v", n, item, err)
	}
	card := e.notify.items[0]
	if card.Title != "🗓️ 10 scheduled errands" || card.Summary != "10 errands scheduled" || card.Category != "schedule" || *card.UserID != 1 {
		t.Fatalf("card %+v", card)
	}
	if !strings.Contains(card.Body, "_Showing the first 8 of 10._") || !strings.HasSuffix(card.Body, "Tap **Cancel** on any errand to stop it.") {
		t.Fatalf("body %q", card.Body)
	}
	elems := card.Metadata["interactive_elements"].([]any)
	btn := elems[0].(map[string]any)
	if len(elems) != 8 || len(card.Metadata["schedules"].([]any)) != 8 || btn["target"] != "server" ||
		btn["command"] != "schedule" || btn["callback"] != "cancel_schedule" || btn["label"] != "Cancel: errand number 0 with a very long" {
		t.Fatalf("metadata %v", card.Metadata)
	}
	if p := e.notify.pushes[0]; p.TargetType != "user" || p.TargetID != "1" {
		t.Fatalf("push %+v", p)
	}

	// The Cancel tap: cancels, re-posts the list, and reports.
	sid := btn["data"].(map[string]any)["schedule_id"].(string)
	out, errMsg, err := e.m.CancelScheduleTap(context.Background(), rhh, &uid, map[string]any{"schedule_id": sid})
	if err != nil || errMsg != "" {
		t.Fatal(err, errMsg)
	}
	inbox := out["inbox"].(map[string]any)
	if inbox["title"] != "🗓️ Errand cancelled" || inbox["summary"] != "Done — you have 9 errands still scheduled." || len(e.notify.items) != 2 {
		t.Fatalf("tap %v", out)
	}
	out, _, _ = e.m.CancelScheduleTap(context.Background(), rhh, &uid, map[string]any{"schedule_id": sid})
	if out["inbox"].(map[string]any)["title"] != "Errand already stopped" {
		t.Fatalf("second tap %v", out)
	}
	if _, msg, _ := e.m.CancelScheduleTap(context.Background(), rhh, &uid, map[string]any{}); msg != "No schedule to cancel." {
		t.Fatalf("no id: %q", msg)
	}
}

func TestPurgeUserSchedules(t *testing.T) {
	e := newSchedEnv(t)
	uid := int64(7)
	mine := e.createSchedule("mine", "", e.clock().Add(time.Hour), &uid)
	theirs := e.createSchedule("theirs", "", e.clock().Add(time.Hour), nil)
	err := e.d.Tx(context.Background(), func(tx *sql.Tx) error { return e.m.PurgeUser(context.Background(), tx, uid) })
	if err != nil {
		t.Fatal(err)
	}
	var n int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_schedules WHERE id IN (?, ?)`, mine, theirs).Scan(&n)
	if n != 1 {
		t.Fatalf("%d left", n)
	}
	e.fireSchedule(mine) // the lapsed trigger finds no row
	if len(e.fired) != 0 {
		t.Fatal("a purged user's schedule fired")
	}
}
