package phone

import (
	"context"
	"testing"
)

func TestErrandSnapshotAndDecline(t *testing.T) {
	e := newEnv(t)
	e.enable()
	ctx := context.Background()
	id := e.draft() // linked to errand run-1, step 2

	snap, ok, err := e.s.Snapshot(ctx, id)
	if err != nil || !ok || snap.ErrandID != "run-1" || *snap.ErrandStep != 2 || snap.State != StateDraft || !snap.ConfirmedAt.IsZero() {
		t.Fatalf("snapshot %+v %v %v", snap, ok, err)
	}
	if _, ok, err := e.s.Snapshot(ctx, "nope"); ok || err != nil {
		t.Fatalf("missing session: %v %v", ok, err)
	}
	// Another errand's sessions are untouched; this errand's draft is declined, once.
	if n, err := e.s.DeclineErrand(ctx, "run-other"); n || err != nil {
		t.Fatalf("other errand: %v %v", n, err)
	}
	if n, err := e.s.DeclineErrand(ctx, "run-1"); !n || err != nil {
		t.Fatalf("decline: %v %v", n, err)
	}
	if s := e.session(id); s.State != StateDeclined || s.ErrorMessage != "The errand was stopped" {
		t.Fatalf("after decline: %s %q", s.State, s.ErrorMessage)
	}
	if n, _ := e.s.DeclineErrand(ctx, "run-1"); n {
		t.Fatal("a declined session is final")
	}
}
