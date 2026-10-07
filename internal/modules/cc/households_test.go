package cc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func countRows(t *testing.T, d *db.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Read.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// D20/D49: a deleted household loses everything CC holds for it; a member who leaves loses
// their personal data there only. The other household is untouched.
func TestHouseholdPurges(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	for _, hh := range []string{"hh1", "hh2"} {
		e.createNode("node-"+hh, hh)
		e.do("POST", "/api/v0/households/"+hh+"/rooms", map[string]any{"name": "Kitchen"}, adminH()).want(201)
		e.do("GET", "/api/v0/households/"+hh+"/routines", nil, adminH()).want(200) // seeds defaults
		if err := e.m.settings.Set(ctx, settingHouseholdLocation, "Somewhere", settings.Scope{HouseholdID: hh}); err != nil {
			t.Fatal(err)
		}
		for _, uid := range []int64{7, 8} {
			if _, err := e.d.Write.Exec(`INSERT INTO cc_user_memories (user_id, household_id, content) VALUES (?, ?, 'likes tea')`, uid, hh); err != nil {
				t.Fatal(err)
			}
			u := uid
			id, err := e.m.CreateSchedule(ctx, NewSchedule{HouseholdID: hh, UserID: &u, Intent: "water plants",
				FireAt: e.clock().Add(time.Hour), Timezone: "UTC"})
			if err != nil {
				t.Fatal(err)
			}
			// The test module has no scheduler; stand in its trigger.
			if _, err := e.d.Write.Exec(`INSERT OR IGNORE INTO platform_triggers (name, kind, spec, job_type, created_at, updated_at)
				VALUES (?, 'once', '{}', 'x', 0, 0)`, scheduleTrigger(id)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := e.d.Write.Exec(`INSERT INTO platform_triggers (name, kind, spec, job_type, created_at, updated_at)
		VALUES ('cc.attention_journal:hh1', 'cron', '{}', 'x', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	tx := func(f func(*sql.Tx) error) {
		t.Helper()
		if err := e.d.Tx(ctx, f); err != nil {
			t.Fatal(err)
		}
	}

	// User 7 leaves hh1: their memory and schedule there go; user 8's and hh2's stay.
	tx(func(tx *sql.Tx) error { return e.m.PurgeUserHousehold(ctx, tx, 7, "hh1") })
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM cc_user_memories WHERE user_id = 7`); n != 1 {
		t.Fatalf("user 7 memories left %d (want hh2's only)", n)
	}
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM cc_schedules WHERE household_id = 'hh1'`); n != 1 {
		t.Fatalf("hh1 schedules %d (want user 8's)", n)
	}
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM platform_triggers WHERE name LIKE 'cc.schedule:%'`); n != 3 {
		t.Fatalf("schedule triggers %d", n)
	}

	// hh1 is deleted: nothing of it remains, in any cc table or its triggers.
	tx(func(tx *sql.Tx) error { return e.m.PurgeHousehold(ctx, tx, "hh1") })
	tables, err := householdTables(ctx, mustTx(t, e.d))
	if err != nil || len(tables) < 20 {
		t.Fatalf("tables %v %v", tables, err)
	}
	for _, tb := range tables {
		if n := countRows(t, e.d, `SELECT COUNT(*) FROM "`+tb+`" WHERE household_id = 'hh1'`); n != 0 {
			t.Errorf("%s still has %d hh1 rows", tb, n)
		}
	}
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM cc_nodes WHERE node_id = 'node-hh1'`); n != 0 {
		t.Error("hh1's node survived")
	}
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM platform_triggers WHERE name LIKE '%hh1%'`); n != 0 {
		t.Error("hh1's journal trigger survived")
	}
	if n := countRows(t, e.d, `SELECT COUNT(*) FROM platform_triggers WHERE name LIKE 'cc.schedule:%'`); n != 2 {
		t.Errorf("hh2's schedule triggers: %d", n)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM cc_rooms WHERE household_id = 'hh2'`,
		`SELECT COUNT(*) FROM cc_routines WHERE household_id = 'hh2'`,
		`SELECT COUNT(*) FROM cc_user_memories WHERE household_id = 'hh2'`,
		`SELECT COUNT(*) FROM cc_settings WHERE household_id = 'hh2'`,
		`SELECT COUNT(*) FROM cc_nodes WHERE household_id = 'hh2'`,
	} {
		if countRows(t, e.d, q) == 0 {
			t.Errorf("hh2 lost data: %s", q)
		}
	}
}

// mustTx opens a read transaction for helpers that take one.
func mustTx(t *testing.T, d *db.DB) *sql.Tx {
	t.Helper()
	tx, err := d.Read.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
