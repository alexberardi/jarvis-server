package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// openNoShareDelete opens path the way a scanner does: readers and writers welcome, but no
// rename or delete while the handle is open.
func openNoShareDelete(t *testing.T, path string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// TestTransientLockRealSharingViolation: a real handle without FILE_SHARE_DELETE makes the
// rename fail with an error transientLock recognizes, and renameRetry gets through once the
// handle closes (closed on its first wait: no wall clock involved).
func TestTransientLockRealSharingViolation(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "jarvisd.exe"), filepath.Join(dir, "jarvisd.exe.old")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := openNoShareDelete(t, src)
	err := os.Rename(src, dst)
	if err == nil {
		windows.CloseHandle(h)
		t.Fatal("renamed a file held without FILE_SHARE_DELETE")
	}
	if !transientLock(err) {
		windows.CloseHandle(h)
		t.Fatalf("not seen as transient: %v", err)
	}

	oldSleep := sleepFor
	t.Cleanup(func() { sleepFor = oldSleep })
	waits := 0
	sleepFor = func(time.Duration) {
		if waits++; waits == 1 {
			windows.CloseHandle(h)
		}
	}
	if err := renameRetry(src, dst); err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Fatalf("waited %d times", waits)
	}
}
