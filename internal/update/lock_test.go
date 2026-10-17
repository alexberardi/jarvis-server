package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLockExcludes(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	release, err := Lock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	release()
	if _, err := os.Stat(p.LockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file left after release: %v", err)
	}
	release2, err := Lock(p)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release() // a stale release must not drop the new holder's lock
	if _, err := Lock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock after a stale release: %v", err)
	}
	release2()
}

func TestLockTakesOverDeadPID(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	writeLock(t, p, strconv.Itoa(cmd.Process.Pid)+"\n")
	release, err := Lock(p)
	if err != nil {
		t.Fatalf("dead holder: %v", err)
	}
	release()
}

func TestLockRefusesLiveHolder(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	writeLock(t, p, strconv.Itoa(os.Getpid())+"\n")
	if _, err := Lock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("live holder: %v", err)
	}
}

func TestLockUnreadable(t *testing.T) {
	p := Paths{Home: t.TempDir()}
	writeLock(t, p, "")
	// Fresh and empty: its holder may not have written its PID yet.
	if _, err := Lock(p); !errors.Is(err, ErrLocked) {
		t.Fatalf("fresh empty lock: %v", err)
	}
	old := time.Now().Add(-2 * staleLockAge)
	if err := os.Chtimes(p.LockPath(), old, old); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(p)
	if err != nil {
		t.Fatalf("old empty lock: %v", err)
	}
	release()
}

func TestStageRefusedWhileLocked(t *testing.T) {
	_, in, o, _ := setup(t)
	release, err := Lock(in.paths)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	sentinel := filepath.Join(in.paths.StagedDir(), "other")
	if err := os.MkdirAll(filepath.Dir(sentinel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), o); !errors.Is(err, ErrLocked) {
		t.Fatalf("stage while locked: %v", err)
	}
	if readString(t, sentinel) != "x" {
		t.Fatal("stage touched the other upgrade's staged dir")
	}
	release()
	if _, err := Stage(context.Background(), o); err != nil {
		t.Fatalf("stage after release: %v", err)
	}
	if _, err := os.Stat(in.paths.LockPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage left its lock: %v", err)
	}
}

func writeLock(t *testing.T, p Paths, body string) {
	t.Helper()
	if err := os.MkdirAll(p.UpdatesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.LockPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
