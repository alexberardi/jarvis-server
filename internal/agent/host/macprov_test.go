package host

import (
	"bytes"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/ax"
)

// One app that never answers Accessibility must not hold the list (#359: System Events waited
// out each such app, ~10 s for every window operation). The others' windows come back within
// the list's budget, and the app is logged once, not on every list.
func TestDarwinSkipsAnAppThatNeverAnswers(t *testing.T) {
	d, s, a := macDesktop()
	var logs bytes.Buffer
	d.log = slog.New(slog.NewTextHandler(&logs, nil))
	a.apps = []ax.App{{PID: 101, Name: "TextEdit"}, {PID: 202, Name: "Stuck"}, {PID: 303, Name: "Safari"}}
	a.windows = map[int][]ax.AppWindow{
		101: {{Title: "Untitled"}, {Title: "Notes", Minimized: true}},
		202: {{Title: "never seen"}},
		303: {{Title: "Apple"}},
	}
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })
	a.hang = map[int]chan struct{}{202: never}

	want := []Window{
		{ID: "101/1", App: "TextEdit", Title: "Untitled"},
		{ID: "101/2", App: "TextEdit", Title: "Notes", Minimized: true},
		{ID: "303/1", App: "Safari", Title: "Apple"},
	}
	for range 2 {
		start := time.Now()
		ws, err := d.ListWindows(ctx)
		if took := time.Since(start); took > 400*time.Millisecond {
			t.Fatalf("list took %v with one app not answering", took)
		}
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ws, want) {
			t.Fatalf("windows = %+v, want %+v", ws, want)
		}
	}
	if n := strings.Count(logs.String(), "pid=202"); n != 1 {
		t.Fatalf("the stuck app was logged %d times, want once:\n%s", n, logs.String())
	}
	if strings.Contains(s.allCommands(), "osascript") {
		t.Fatalf("listing windows ran osascript:\n%s", s.allCommands())
	}
}

// An app that fails (it quit between the app list and its windows) is left out; the rest list.
func TestDarwinListLeavesOutAFailingApp(t *testing.T) {
	d, _, a := macDesktop()
	d.log = slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	a.apps = append(a.apps, ax.App{PID: 7, Name: "Gone"})
	a.errs = map[int]error{7: ax.ErrNoWindow}
	ws, err := d.ListWindows(ctx)
	if err != nil || len(ws) != 1 || ws[0].ID != macWinID {
		t.Fatalf("windows = %+v, %v", ws, err)
	}
}

func TestDarwinWindowActions(t *testing.T) {
	for _, tc := range []struct {
		do   func(*Darwin, string) error
		want string
	}{
		{func(d *Darwin, id string) error { return d.MinimizeWindow(ctx, id) }, "ax minimize 4242/1"},
		{func(d *Darwin, id string) error { return d.FocusWindow(ctx, id) }, "ax focus 4242/1"},
		{func(d *Darwin, id string) error { return d.CloseWindow(ctx, id) }, "ax close 4242/1"},
	} {
		d, s, _ := macDesktop()
		if err := tc.do(d, macWinID); err != nil {
			t.Fatalf("%s: %v", tc.want, err)
		}
		if !strings.Contains(s.allCommands(), tc.want) {
			t.Fatalf("did not issue %q:\n%s", tc.want, s.allCommands())
		}
		// Past the end of the app's windows: gone, not a generic failure.
		if err := tc.do(d, "4242/2"); !errors.Is(err, ErrWindowNotFound) {
			t.Fatalf("%s on 4242/2: err = %v, want ErrWindowNotFound", tc.want, err)
		}
	}
}

// A malformed id is refused before any Accessibility call.
func TestDarwinBadIDNeverReachesAX(t *testing.T) {
	for _, id := range []string{"", "4242", "4242/", "/1", "4242/1/2", "x/1", `1"; quit`} {
		d, s, _ := macDesktop()
		if err := d.CloseWindow(ctx, id); !errors.Is(err, ErrWindowNotFound) {
			t.Fatalf("id %q: err = %v", id, err)
		}
		if cmds := s.allCommands(); cmds != "" {
			t.Fatalf("id %q reached AX:\n%s", id, cmds)
		}
	}
}
