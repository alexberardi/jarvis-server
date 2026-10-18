package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// recordSyncDir replaces syncDir for the test, calling see with each directory synced.
func recordSyncDir(t *testing.T, see func(dir string) error) {
	t.Helper()
	old := syncDir
	syncDir = see
	t.Cleanup(func() { syncDir = old })
}

// A rename isn't durable until its directory is synced: a power cut could otherwise bring the
// old executable (or none) back with a marker that says swapped, or lose the marker.
func TestSwapSyncsDirsAfterRenames(t *testing.T) {
	_, in, o, bin := setup(t)
	ctx := context.Background()
	if _, err := Stage(ctx, o); err != nil {
		t.Fatal(err)
	}
	exeDir := filepath.Dir(in.paths.Exe)
	var exeSynced, swappedSynced bool
	recordSyncDir(t, func(dir string) error {
		switch filepath.Clean(dir) {
		case filepath.Clean(exeDir):
			if readString(t, in.paths.Exe) == string(bin) {
				exeSynced = true
			}
		case filepath.Clean(in.paths.UpdatesDir()):
			if m, _ := ReadMarker(in.paths); m != nil && m.State == StateSwapped {
				swappedSynced = true
			}
		}
		return nil
	})
	if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err != nil {
		t.Fatal(err)
	}
	if !exeSynced {
		t.Error("the executable's directory was not synced after the new binary was renamed in")
	}
	if !swappedSynced {
		t.Error("the updates directory was not synced after the swapped marker was renamed in")
	}

	var restoredSynced bool
	recordSyncDir(t, func(dir string) error {
		if filepath.Clean(dir) == filepath.Clean(exeDir) && readString(t, in.paths.Exe) == "old binary" {
			restoredSynced = true
		}
		return nil
	})
	if err := RequestRollback(in.paths, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(ctx, in.paths, ""); err != nil {
		t.Fatal(err)
	}
	if !restoredSynced {
		t.Error("the executable's directory was not synced after the rollback put the old binary back")
	}
}

// The marker is the upgrade's state: a write whose directory sync fails is reported.
func TestWriteMarkerReportsDirSyncFailure(t *testing.T) {
	in := newInstall(t)
	boom := errors.New("sync failed")
	recordSyncDir(t, func(string) error { return boom })
	if err := WriteMarker(in.paths, &Marker{State: StateStaged}); !errors.Is(err, boom) {
		t.Fatalf("WriteMarker: %v, want the sync error", err)
	}
}
