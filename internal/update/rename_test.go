package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var errLocked = errors.New("locked by another process")

// fakeLocks replaces the rename seams: rename fails with errLocked while locked(old) says so,
// errLocked is the only transient error, and sleeps are recorded, not slept.
func fakeLocks(t *testing.T, locked func(oldpath string) bool) *[]time.Duration {
	t.Helper()
	oldRename, oldSleep, oldTransient := renameFile, sleepFor, isTransientLock
	t.Cleanup(func() { renameFile, sleepFor, isTransientLock = oldRename, oldSleep, oldTransient })
	var slept []time.Duration
	renameFile = func(oldpath, newpath string) error {
		if locked(oldpath) {
			return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: errLocked}
		}
		return os.Rename(oldpath, newpath)
	}
	sleepFor = func(d time.Duration) { slept = append(slept, d) }
	isTransientLock = func(err error) bool { return errors.Is(err, errLocked) }
	return &slept
}

// lockedFor fails the first n renames of path.
func lockedFor(path string, n int) func(string) bool {
	return func(oldpath string) bool {
		if oldpath != path || n == 0 {
			return false
		}
		n--
		return true
	}
}

func TestRenameRetryClearsTransientLock(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	slept := fakeLocks(t, lockedFor(src, 3))
	if err := renameRetry(src, dst); err != nil {
		t.Fatal(err)
	}
	if readString(t, dst) != "x" {
		t.Fatal("not renamed")
	}
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	if len(*slept) != len(want) {
		t.Fatalf("slept %v, want %v", *slept, want)
	}
	for i := range want {
		if (*slept)[i] != want[i] {
			t.Fatalf("slept %v, want %v", *slept, want)
		}
	}
}

func TestRenameRetryOtherErrorsReturnAtOnce(t *testing.T) {
	dir := t.TempDir()
	slept := fakeLocks(t, func(string) bool { return false })
	err := renameRetry(filepath.Join(dir, "missing"), filepath.Join(dir, "b"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err %v", err)
	}
	if len(*slept) != 0 {
		t.Fatalf("retried a permanent error: slept %v", *slept)
	}
}

func TestRenameRetryGivesUp(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a")
	slept := fakeLocks(t, func(string) bool { return true })
	err := renameRetry(src, filepath.Join(dir, "b"))
	if !errors.Is(err, errLocked) {
		t.Fatalf("err %v", err)
	}
	var total time.Duration
	for _, d := range *slept {
		if d > time.Second {
			t.Fatalf("a wait above the 1s cap: %v", *slept)
		}
		total += d
	}
	if total < renameRetryBudget || total >= renameRetryBudget+time.Second {
		t.Fatalf("waited %s in all, budget %s", total, renameRetryBudget)
	}
}

// TestSwapAfterRollbackWaitsOutAVLock is the CI flake (PR #127, windows upgrade e2e): right
// after a rollback restored jarvisd.exe, something (antivirus) held it open without
// FILE_SHARE_DELETE, and the next swap failed to move it aside. The Windows swap now retries
// both renames on any OS.
func TestSwapAfterRollbackWaitsOutAVLock(t *testing.T) {
	old := renameAside
	renameAside = true
	t.Cleanup(func() { renameAside = old })
	in, o, bin := swapped(t)
	if err := RequestRollback(in.paths, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(t.Context(), in.paths, ""); err != nil {
		t.Fatal(err)
	}
	// The restored executable and the next swap's extracted binary are both being scanned.
	newBin := filepath.Join(filepath.Dir(in.paths.Exe), ".jarvisd.new")
	exeLocks, newLocks := lockedFor(in.paths.Exe, 4), lockedFor(newBin, 2)
	slept := fakeLocks(t, func(p string) bool { return exeLocks(p) || newLocks(p) })
	if _, err := Stage(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	if _, err := Swap(t.Context(), in.paths, SwapOptions{Keys: o.Keys}); err != nil {
		t.Fatal(err)
	}
	if readString(t, in.paths.Exe) != string(bin) || readString(t, in.paths.Prev()) != "old binary" {
		t.Fatal("swap after rollback")
	}
	if len(*slept) != 6 {
		t.Fatalf("slept %v, want 6 waits (4 on the exe, 2 on .jarvisd.new)", *slept)
	}
}

// TestSwapLockedForGoodKeepsTheBinary: when the extracted binary never frees up, the running
// executable is moved back, so the service is never left without one.
func TestSwapLockedForGoodKeepsTheBinary(t *testing.T) {
	old := renameAside
	renameAside = true
	t.Cleanup(func() { renameAside = old })
	_, in, o, _ := setup(t)
	if _, err := Stage(t.Context(), o); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(filepath.Dir(in.paths.Exe), ".jarvisd.new")
	fakeLocks(t, func(p string) bool { return p == newBin })
	if _, err := Swap(t.Context(), in.paths, SwapOptions{Keys: o.Keys}); !errors.Is(err, errLocked) {
		t.Fatalf("err %v", err)
	}
	if readString(t, in.paths.Exe) != "old binary" {
		t.Fatal("the running binary was not put back")
	}
}
