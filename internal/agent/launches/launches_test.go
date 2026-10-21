package launches

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordAndRecent(t *testing.T) {
	now := time.Unix(1000, 0)
	tr := &Tracker{Now: func() time.Time { return now }, Windows: func(context.Context) ([]string, error) { return []string{"a", "b"}, nil }}
	if _, ok := tr.Recent(time.Minute); ok {
		t.Fatal("nothing launched yet")
	}
	tr.Record(context.Background(), "Neovim", "nvim")
	l, ok := tr.Recent(time.Minute)
	if !ok || l.Name != "Neovim" || l.ID != "nvim" || !l.Before["a"] || !l.Before["b"] || len(l.Before) != 2 {
		t.Fatalf("%+v %v", l, ok)
	}
	now = now.Add(61 * time.Second)
	if _, ok := tr.Recent(time.Minute); ok {
		t.Fatal("a launch a minute ago is not recent")
	}
}

func TestUnlistableWindowsLeaveBeforeNil(t *testing.T) {
	tr := &Tracker{Windows: func(context.Context) ([]string, error) { return nil, errors.New("no session") }}
	tr.Record(context.Background(), "Steam", "")
	if l, ok := tr.Recent(time.Minute); !ok || l.Before != nil {
		t.Fatalf("%+v", l)
	}
	var nilT *Tracker
	nilT.Record(context.Background(), "x", "")
	if _, ok := nilT.Recent(time.Minute); ok {
		t.Fatal("nil tracker")
	}
}

// A slow window lister (AppleScript over every window, 5-10 s on a busy Mac) never holds the
// launch up: Record waits at most Wait for the snapshot and records no Before without it.
func TestSlowSnapshotDoesNotDelayRecord(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	tr := &Tracker{Wait: 50 * time.Millisecond, Windows: func(ctx context.Context) ([]string, error) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		return []string{"a"}, nil
	}}
	start := time.Now()
	tr.Record(context.Background(), "TextEdit", "")
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("Record took %v", d)
	}
	if l, ok := tr.Recent(time.Minute); !ok || l.Name != "TextEdit" || l.Before != nil {
		t.Fatalf("%+v %v", l, ok)
	}
}

// The lister is given a deadline, so one that honours its context (osascript) is stopped.
func TestSnapshotListerGetsTheDeadline(t *testing.T) {
	tr := &Tracker{Wait: 20 * time.Millisecond, Windows: func(ctx context.Context) ([]string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("no deadline")
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	tr.Record(context.Background(), "x", "")
}

// macOS: the snapshot comes from the window server (CoreGraphics window numbers and their
// pids), not the window list's System Events ids. NewOwners names the apps with a window that
// wasn't there at the launch, front first.
func TestNewOwnersSinceTheLaunch(t *testing.T) {
	cur := map[string]string{"1": "100", "2": "200"}
	order := []string{"1", "2"}
	tr := &Tracker{Owners: func(context.Context) ([]Owned, error) {
		var out []Owned
		for _, id := range order {
			out = append(out, Owned{ID: id, Owner: cur[id]})
		}
		return out, nil
	}}
	tr.Record(context.Background(), "Terminal", "")
	l, _ := tr.Recent(time.Minute)
	if !l.ByOwner || len(l.Before) != 2 {
		t.Fatalf("%+v", l)
	}
	if got, err := tr.NewOwners(context.Background(), l); err != nil || len(got) != 0 {
		t.Fatalf("nothing new yet: %v %v", got, err)
	}
	cur["9"], cur["8"], cur["7"] = "200", "300", "200"
	order = []string{"9", "1", "8", "7", "2"}
	got, err := tr.NewOwners(context.Background(), l)
	if err != nil || strings.Join(got, ",") != "200,300" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := tr.NewOwners(context.Background(), Launch{}); err == nil {
		t.Fatal("a launch without an owner snapshot has no new owners")
	}
}
