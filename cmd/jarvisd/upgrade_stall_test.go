package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// stopManager is an installed service whose Stop returns stopErr; it counts Starts.
type stopManager struct {
	service.Manager
	stopErr error
	starts  int
}

func (m *stopManager) Stop(context.Context) error  { return m.stopErr }
func (m *stopManager) Start(context.Context) error { m.starts++; return nil }

// stalledUpgrade lays out a swapped upgrade: the new binary in place, the previous one as
// .prev, a database with -wal/-shm and its snapshot named in the marker (withSnapshot).
func stalledUpgrade(t *testing.T, withSnapshot bool) update.Paths {
	t.Helper()
	home := t.TempDir()
	p := update.Paths{Home: home, Exe: filepath.Join(t.TempDir(), "jarvisd")}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(p.Exe, "new")
	write(p.Prev(), "old")
	m := &update.Marker{State: update.StateSwapped, From: "v1.0.0", To: "v1.1.0", Exe: p.Exe, Prev: p.Prev()}
	if withSnapshot {
		db := filepath.Join(home, "jarvis.db")
		snap := filepath.Join(p.BackupsDir(), "jarvis.db.snap")
		write(db, "live")
		write(db+"-wal", "wal")
		write(db+"-shm", "shm")
		write(snap, "snapshot")
		m.Snapshots = map[string]string{db: snap}
	}
	if err := update.WriteMarker(p, m); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// #106: when the new version never reported an outcome and stopping the service failed, the
// CLI still rolled back: restoring the snapshot deletes -wal/-shm and renames over jarvis.db
// while jarvisd may have it open. It now touches nothing and says how to roll back by hand.
func TestRollbackStalledStopFails(t *testing.T) {
	p := stalledUpgrade(t, true)
	stopErr := errors.New("systemctl stop: timed out")
	mgr := &stopManager{stopErr: stopErr}

	var b strings.Builder
	err := rollbackStalled(context.Background(), mgr, service.Status{Installed: true}, p, time.Minute, &b)
	if !errors.Is(err, stopErr) {
		t.Fatalf("want the stop error wrapped, got %v", err)
	}
	for _, want := range []string{"jarvisd upgrade --rollback", "nothing was rolled back"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if mgr.starts != 0 {
		t.Errorf("started the service %d times", mgr.starts)
	}
	m, rerr := update.ReadMarker(p)
	if rerr != nil || m == nil || m.State != update.StateSwapped {
		t.Fatalf("marker changed: %+v %v", m, rerr)
	}
	db := filepath.Join(p.Home, "jarvis.db")
	for path, want := range map[string]string{
		p.Exe: "new", p.Prev(): "old", db: "live", db + "-wal": "wal", db + "-shm": "shm",
		filepath.Join(p.BackupsDir(), "jarvis.db.snap"): "snapshot",
	} {
		if got := readFile(t, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(p.ResultPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a result was recorded: %v", err)
	}
}

// When the stop succeeds the rollback runs as before: the previous binary is back, the
// service started, and the error says it was rolled back.
func TestRollbackStalledStopOK(t *testing.T) {
	p := stalledUpgrade(t, false)
	mgr := &stopManager{}

	var b strings.Builder
	err := rollbackStalled(context.Background(), mgr, service.Status{Installed: true}, p, time.Minute, &b)
	if err == nil || !strings.Contains(err.Error(), "was rolled back to v1.0.0") {
		t.Fatalf("got %v", err)
	}
	if got := readFile(t, p.Exe); got != "old" {
		t.Errorf("exe = %q, want the previous binary", got)
	}
	if mgr.starts != 1 {
		t.Errorf("started %d times, want 1", mgr.starts)
	}
	if m, _ := update.ReadMarker(p); m != nil {
		t.Errorf("marker left: %+v", m)
	}
}
