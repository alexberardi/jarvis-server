package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
)

type fixedClock string

func (f fixedClock) HouseholdTimezone(context.Context, string) string { return string(f) }

// The household timezone is the zone its most recently seen active node reported.
func TestHouseholdTimezoneFromNodes(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	e.createNode("n1", "hh1")
	e.createNode("n2", "hh1")
	e.createNode("n3", "hh2")
	seen := func(id string, at time.Time) {
		if _, err := e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ? WHERE node_id = ?`, at.Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
	}
	if tz := e.m.householdTimezone(ctx, "hh1"); tz != "" {
		t.Fatalf("no reports yet: %q", tz)
	}
	e.m.recordNodeTimezone(ctx, "n1", "America/New_York")
	e.m.recordNodeTimezone(ctx, "n2", "Mars/Olympus") // invalid: ignored
	e.m.recordNodeTimezone(ctx, "n3", "Europe/London")
	seen("n1", e.clock())
	seen("n2", e.clock().Add(time.Hour))
	if tz := e.m.householdTimezone(ctx, "hh1"); tz != "America/New_York" {
		t.Fatalf("hh1 %q", tz)
	}
	e.m.recordNodeTimezone(ctx, "n2", "America/Chicago")
	if tz := e.m.householdTimezone(ctx, "hh1"); tz != "America/Chicago" {
		t.Fatalf("most recent node: %q", tz)
	}
	if tz := e.m.householdTimezone(ctx, "hh2"); tz != "Europe/London" {
		t.Fatalf("hh2 %q", tz)
	}
	if loc := e.m.householdLocation(ctx, "hh1"); loc.String() != "America/Chicago" {
		t.Fatalf("attention location %v", loc)
	}
	e.m.HouseholdClock = fixedClock("Asia/Tokyo")
	if tz := e.m.householdTimezone(ctx, "hh1"); tz != "Asia/Tokyo" {
		t.Fatalf("override %q", tz)
	}
}

// Errands reach schedules (doc 08) and phone (doc 11) through the module by default.
func TestErrandWiring(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	if e.m.rt.scheduleFire == nil {
		t.Fatal("schedule fire not wired to errands")
	}
	if _, ok := e.m.phone.Errands.(phoneErrandHook); !ok {
		t.Fatalf("phone errand hook %T", e.m.phone.Errands)
	}
	id, err := errandSchedules{e.m}.CreateSchedule(ctx, errands.NewSchedule{HouseholdID: "hh1", Intent: "water plants",
		FireAt: e.clock().Add(time.Hour), Timezone: "UTC"})
	if err != nil || id == "" {
		t.Fatalf("create schedule %q %v", id, err)
	}
	var intent string
	if err := e.d.Read.QueryRow(`SELECT intent FROM cc_schedules WHERE id = ?`, id).Scan(&intent); err != nil || intent != "water plants" {
		t.Fatalf("schedule row %q %v", intent, err)
	}
	p := errandPhone{e.m}
	if p.Enabled(ctx, "hh1") {
		t.Fatal("phone is off by default")
	}
	if _, ok, err := p.CallStatus(ctx, "nope"); ok || err != nil {
		t.Fatalf("status %v %v", ok, err)
	}
	if n, err := p.DeclineErrandCalls(ctx, "run-x"); n || err != nil {
		t.Fatalf("decline %v %v", n, err)
	}
}
