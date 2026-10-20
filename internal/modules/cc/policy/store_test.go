package policy

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
)

// storeEnv opens a cc-migrated database with a Store whose clock advances by a second per call,
// so "newest first" is deterministic.
func storeEnv(t *testing.T) (*db.DB, *Store, func() time.Time) {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "cc", os.DirFS(filepath.Join("..", "migrations"))); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	now := base
	s := &Store{DB: d, Now: func() time.Time { now = now.Add(time.Second); return now }}
	return d, s, func() time.Time { return now }
}

func TestRecordAndEvents(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	uid, rid := int64(7), int64(3)
	events := []Event{
		{HouseholdID: "hh-1", UserID: &uid, Source: SourceErrandPlan, SourceID: "p1", Tool: "quick_search",
			Effect: Allow, Outcome: OutcomeRan, RuleID: &rid, Reason: "household rule", Detail: map[string]any{"k": "v"}},
		{HouseholdID: "hh-1", Source: SourceErrandStep, SourceID: "p1", Tool: "make_phone_call",
			Effect: Ask, Outcome: OutcomeAsked},
		{HouseholdID: "hh-1", Source: SourceAutomation, Tool: "quick_search", Effect: Block, Outcome: OutcomeBlocked,
			Reason: "watch rule"},
		{HouseholdID: "hh-2", Source: SourceWatch, SourceID: "wt_1", Tool: "notify", Effect: Allow, Outcome: OutcomeFired},
	}
	for _, e := range events {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Events(ctx, "hh-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events for hh-1, want 3: %+v", len(got), got)
	}
	// Newest first.
	if got[0].Source != SourceAutomation || got[1].Source != SourceErrandStep || got[2].Source != SourceErrandPlan {
		t.Fatalf("order: %+v", got)
	}
	if got[2].UserID == nil || *got[2].UserID != 7 || got[2].RuleID == nil || *got[2].RuleID != 3 {
		t.Fatalf("user/rule not round-tripped: %+v", got[2])
	}
	if got[2].Detail == nil || got[2].Detail["k"] != "v" {
		t.Fatalf("detail not round-tripped: %+v", got[2].Detail)
	}
	if got[2].CreatedAt.IsZero() || got[0].CreatedAt.Before(got[2].CreatedAt) {
		t.Fatalf("created_at: %+v", got)
	}
	if got[1].UserID != nil || got[1].RuleID != nil || got[1].Detail != nil {
		t.Fatalf("nils not round-tripped: %+v", got[1])
	}
}

func TestEventsLimit(t *testing.T) {
	_, s, _ := storeEnv(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := s.Record(ctx, Event{HouseholdID: "hh-1", Source: SourceWatch, Effect: Allow, Outcome: OutcomeFired}); err != nil {
			t.Fatal(err)
		}
	}
	two, err := s.Events(ctx, "hh-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(two) != 2 {
		t.Fatalf("limit 2: got %d", len(two))
	}
	all, err := s.Events(ctx, "hh-1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 5 {
		t.Fatalf("limit 1000: got %d, want 5", len(all))
	}
}

// RecordTx commits with the caller's transaction and is gone when it rolls back.
func TestRecordTx(t *testing.T) {
	d, s, _ := storeEnv(t)
	ctx := context.Background()
	ev := Event{HouseholdID: "hh-1", Source: SourceErrandPlan, Effect: Allow, Outcome: OutcomeLaunched}
	rollback := errors.New("rollback")
	if err := d.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.RecordTx(ctx, tx, ev); err != nil {
			t.Fatal(err)
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("tx = %v", err)
	}
	if evs, _ := s.Events(ctx, "hh-1", 0); len(evs) != 0 {
		t.Fatalf("rolled back: %+v", evs)
	}
	if err := d.Tx(ctx, func(tx *sql.Tx) error { return s.RecordTx(ctx, tx, ev) }); err != nil {
		t.Fatal(err)
	}
	if evs, _ := s.Events(ctx, "hh-1", 0); len(evs) != 1 || evs[0].Outcome != OutcomeLaunched {
		t.Fatalf("committed: %+v", evs)
	}
}

func TestEventsPurgeUser(t *testing.T) {
	d, s, _ := storeEnv(t)
	ctx := context.Background()
	uid := int64(7)
	if err := s.Record(ctx, Event{HouseholdID: "hh-1", UserID: &uid, Source: SourceErrandStep, Effect: Allow, Outcome: OutcomeRan}); err != nil {
		t.Fatal(err)
	}
	err := d.Tx(ctx, func(tx *sql.Tx) error { return s.PurgeUser(ctx, tx, uid) })
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Events(ctx, "hh-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("row lost: %d", len(got))
	}
	if got[0].UserID != nil {
		t.Fatalf("user id not purged: %+v", got[0].UserID)
	}
}

func TestRecordRejectsBadEffect(t *testing.T) {
	_, s, _ := storeEnv(t)
	err := s.Record(context.Background(), Event{HouseholdID: "hh-1", Source: SourceWatch, Effect: "maybe", Outcome: OutcomeFired})
	if err == nil {
		t.Fatal("accepted effect 'maybe'; the CHECK constraint must reject it")
	}
}
