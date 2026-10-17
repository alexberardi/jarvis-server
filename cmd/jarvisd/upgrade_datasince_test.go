package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// #109: a rollback that restored a database snapshot says from when the writes were discarded.
func TestDBNote(t *testing.T) {
	since := time.Date(2026, 10, 17, 14, 3, 5, 0, time.UTC)
	got := dbNote(&update.Result{DBRestored: true, DataSince: since})
	want := "database restored: data written since " + since.Local().Format(dataSinceLayout) + " was rolled back"
	if got != want {
		t.Errorf("restored: %q, want %q", got, want)
	}
	if got := dbNote(&update.Result{}); got != "database kept" {
		t.Errorf("kept: %q", got)
	}
	// A result written by an older version: restored, no time.
	if got := dbNote(&update.Result{DBRestored: true}); got != "database restored from the pre-upgrade snapshot" {
		t.Errorf("no time: %q", got)
	}
}

func TestWaitOutcomeSaysDataSince(t *testing.T) {
	p := update.Paths{Home: t.TempDir()}
	since := time.Date(2026, 10, 17, 14, 3, 5, 0, time.UTC)
	res := update.Result{Outcome: update.ResultRolledBack, From: "v1.0.0", To: "v1.1.0", Reason: "health gate",
		DBRestored: true, DataSince: since, At: since.Add(time.Minute)}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.ResultPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ResultPath(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	err = waitOutcome(context.Background(), p, time.Second, "", &out)
	if err == nil || !strings.Contains(err.Error(), "data written since "+since.Local().Format(dataSinceLayout)+" was rolled back") {
		t.Fatalf("got %v", err)
	}
}
