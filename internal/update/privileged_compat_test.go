package update

import (
	"context"
	"os"
	"testing"
)

// Where the ID11 helper meets main's rollback hardening (A10c U4) and the releases that
// predate binary_restored (v0.1.0-rc1 .. rc5).

// The helper's rollback keeps the binary it replaces as jarvisd.rolledback and its swap drops
// a stale one, as Rollback and Swap do in process.
func TestPrivilegedRollbackKeepsRolledBack(t *testing.T) {
	in, o, bin, _ := staged(t)
	ctx := context.Background()
	if err := os.WriteFile(in.paths.RolledBack(), []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.paths.RolledBack()); !os.IsNotExist(err) {
		t.Fatal("the swap kept a stale jarvisd.rolledback")
	}
	if err := RequestRollback(in.paths, "health gate"); err != nil {
		t.Fatal(err)
	}
	if action, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o)); err != nil || action != ActionBinaryRestored {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" || readString(t, in.paths.RolledBack()) != string(bin) {
		t.Fatal("jarvisd.rolledback is not the replaced binary")
	}
}

// A marker staged by a release that predates binary_restored (no from_finishes_restore): the
// helper restores the binary but leaves rollback_requested, which that release's own
// `sudo jarvisd upgrade --rollback` finishes (it refuses binary_restored as unknown).
func TestPrivilegedRollbackToPreID11Release(t *testing.T) {
	in, o, _, _ := staged(t)
	ctx := context.Background()
	if _, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o)); err != nil {
		t.Fatal(err)
	}
	in.exec(t, `INSERT INTO goose_auth (version_id, is_applied) VALUES (3, 1)`, `INSERT INTO users VALUES ('after')`)
	if err := RequestRollback(in.paths, "health gate"); err != nil {
		t.Fatal(err)
	}
	m, _ := ReadMarker(in.paths)
	m.FromFinishesRestore = false
	if err := WriteMarker(in.paths, m); err != nil {
		t.Fatal(err)
	}
	action, _, err := PrivilegedStep(ctx, in.paths, helperOpts(o))
	if err != nil || action != ActionBinaryRestored {
		t.Fatalf("%q %v", action, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("binary not restored")
	}
	if m, _ := ReadMarker(in.paths); m == nil || m.State != StateRollbackRequested {
		t.Fatalf("marker %+v", m)
	}
	if got := in.users(t); len(got) != 2 {
		t.Fatalf("the helper touched the database: %v", got)
	}
	// The operator's rollback (as root) finishes it.
	res, err := Rollback(ctx, in.paths, "")
	if err != nil || res.Outcome != ResultRolledBack || !res.DBRestored {
		t.Fatalf("%+v %v", res, err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("binary")
	}
	if got := in.users(t); len(got) != 1 || got[0] != "before" {
		t.Fatalf("users %v", got)
	}
}

// Stage vouches for the restored binary only when it is that binary.
func TestStageFromFinishesRestore(t *testing.T) {
	for _, other := range []bool{false, true} {
		_, _, o, _ := setup(t)
		o.ForOtherBinary = other
		m, err := Stage(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if m.FromFinishesRestore == other {
			t.Fatalf("ForOtherBinary %v: FromFinishesRestore %v", other, m.FromFinishesRestore)
		}
	}
}
