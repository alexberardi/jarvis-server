package desktop

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
)

// timedBackend notes when LaunchApp was called.
type timedBackend struct {
	fakeBackend
	at time.Time
}

func (f *timedBackend) LaunchApp(ctx context.Context, p string) error {
	f.at = time.Now()
	return f.fakeBackend.LaunchApp(ctx, p)
}

// "open TextEdit" took 9-10 s on a Mac: the launch record's AppleScript window snapshot ran
// before the launch. A slow window lister no longer delays it, for desktop_open and for a
// computer task's launch.
func TestSlowWindowSnapshotDoesNotDelayTheLaunch(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	slow := func(context.Context) ([]string, error) {
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		return nil, nil
	}
	b := &timedBackend{fakeBackend: fakeBackend{caps: all}}
	ts := New(host.New(b), OpenPolicy{}, misheardCatalogue)
	ts.Launched = &launches.Tracker{Windows: slow}
	start := time.Now()
	out := call(t, ts, ToolOpen, map[string]any{"target": "Steam"})
	if out["success"] != true || !slices.Equal(b.launched, []string{"/apps/steam.desktop"}) {
		t.Fatalf("%v launched %v", out, b.launched)
	}
	if d := b.at.Sub(start); d > launches.SnapshotWait+200*time.Millisecond {
		t.Fatalf("desktop_open launched after %v", d)
	}
	app, err := ts.FindApp("Neovim")
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := ts.LaunchApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if d := b.at.Sub(start); d > launches.SnapshotWait+200*time.Millisecond {
		t.Fatalf("LaunchApp launched after %v", d)
	}
}

// Time desktop_open spends outside the launch itself is logged when it is long.
func TestSlowOpenIsLogged(t *testing.T) {
	defer func(d time.Duration) { slowOutsideLaunch = d }(slowOutsideLaunch)
	slowOutsideLaunch = 20 * time.Millisecond
	var buf bytes.Buffer
	b := &fakeBackend{caps: all}
	ts := New(host.New(b), OpenPolicy{}, misheardCatalogue)
	ts.Log = slog.New(slog.NewTextHandler(&buf, nil))
	ts.Launched = &launches.Tracker{Windows: func(context.Context) ([]string, error) {
		time.Sleep(50 * time.Millisecond)
		return nil, nil
	}}
	call(t, ts, ToolOpen, map[string]any{"target": "Steam"})
	if !strings.Contains(buf.String(), "slow outside the launch") || !strings.Contains(buf.String(), "app=Steam") {
		t.Fatalf("log %q", buf.String())
	}
	buf.Reset()
	ts.Launched = nil
	call(t, ts, ToolOpen, map[string]any{"target": "Steam"})
	if buf.Len() != 0 {
		t.Fatalf("fast open logged %q", buf.String())
	}
}
