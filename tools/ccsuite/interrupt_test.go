package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent is the watcher's world on a fake clock: a script of (status, actions) per poll.
type fakeAgent struct {
	t0    time.Time
	tick  int
	polls []poll
	fired int
}

type poll struct {
	status string
	acts   int // actions logged since the last poll
}

func (f *fakeAgent) now() time.Time { return f.t0.Add(time.Duration(f.tick) * time.Second) }

func (f *fakeAgent) cur() poll {
	if f.tick < len(f.polls) {
		return f.polls[f.tick]
	}
	return f.polls[len(f.polls)-1]
}

func (f *fakeAgent) watcher() watcher {
	return watcher{
		status: func() (string, error) { return f.cur().status, nil },
		acts: func() []time.Time {
			var out []time.Time
			for i := 0; i < f.cur().acts; i++ {
				out = append(out, f.now().Add(-time.Millisecond))
			}
			p := f.polls[min(f.tick, len(f.polls)-1)]
			p.acts = 0 // read once
			if f.tick < len(f.polls) {
				f.polls[f.tick] = p
			}
			return out
		},
		fire: func(context.Context) error { f.fired++; f.tick++; return nil },
		clk: clock{now: f.now, wait: func(ctx context.Context, _ time.Duration) bool {
			f.tick++
			return f.tick < len(f.polls)+1
		}},
	}
}

// The watcher keeps each change of activity, fires once the task's first action has begun (a new
// intent after the task's first status), and samples again straight away; checks read what it saw.
func TestWatcherStops(t *testing.T) {
	f := &fakeAgent{t0: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), polls: []poll{
		{"idle", 0}, {"controlling: Working on your computer", 0}, {"controlling: Working on your computer", 0},
		{"controlling: Focus the title field", 1}, {"idle", 0}, {"idle", 0},
	}}
	w := f.watcher().run(context.Background(), &Interrupt{Do: "stop"})
	if f.fired != 1 || w.Interrupt != "stop" || w.Interrupted.IsZero() {
		t.Fatalf("fired %d, watch %+v", f.fired, w)
	}
	var kinds []string
	for _, s := range w.Activity {
		kinds = append(kinds, s.kind())
	}
	if strings.Join(kinds, " ") != "idle controlling controlling idle" {
		t.Fatalf("activity %v", kinds)
	}
	for _, kind := range []string{"stopped", "indicator"} {
		if why := (Check{Kind: kind}).Eval(&State{Watch: w}); why != "" {
			t.Errorf("%s: %s", kind, why)
		}
	}
	// An action after the stop, or an indicator left on, fails.
	late := *w
	late.Actions = append(append([]time.Time(nil), w.Actions...), w.Interrupted.Add(time.Second))
	if why := (Check{Kind: "stopped"}).Eval(&State{Watch: &late}); !strings.Contains(why, "1 actions landed after the stop") {
		t.Errorf("late action: %q", why)
	}
	on := *w
	on.Activity = append(append([]Sample(nil), w.Activity...), Sample{Time: w.Interrupted.Add(time.Second), Status: "controlling: Save"})
	if why := (Check{Kind: "stopped"}).Eval(&State{Watch: &on}); !strings.Contains(why, "still shows") {
		t.Errorf("indicator on: %q", why)
	}
}

// paused holds when the agent shows paused after the input with at most one action first.
func TestPausedCheck(t *testing.T) {
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	w := &Watch{Interrupt: "user_input", Interrupted: at(5), Actions: []time.Time{at(4), at(6)}, Activity: []Sample{
		{at(0), "idle"}, {at(1), "controlling: New"}, {at(7), "paused: New"}, {at(20), "controlling: Save"}, {at(30), "idle"}}}
	if why := (Check{Kind: "paused"}).Eval(&State{Watch: w}); why != "" {
		t.Fatalf("paused: %s", why)
	}
	w.Actions = append(w.Actions, at(6))
	if why := (Check{Kind: "paused"}).Eval(&State{Watch: w}); !strings.Contains(why, "2 actions landed before it paused") {
		t.Fatalf("two actions: %q", why)
	}
	w.Activity = []Sample{{at(0), "idle"}, {at(1), "controlling: New"}, {at(30), "idle"}}
	if why := (Check{Kind: "paused"}).Eval(&State{Watch: w}); !strings.Contains(why, "never showed paused") {
		t.Fatalf("no pause: %q", why)
	}
	if why := (Check{Kind: "paused"}).Eval(&State{}); !strings.Contains(why, "not watched") {
		t.Fatalf("no watch: %q", why)
	}
	if why := (Check{Kind: "stopped"}).Eval(&State{Watch: &Watch{Activity: []Sample{{at(0), "idle"}}}}); !strings.Contains(why, "never interrupted") {
		t.Fatalf("not fired: %q", why)
	}
}

// The agent's log is read in pieces as it grows: successful computer_act lines only, a line
// split across reads counted once.
func TestLogTailActs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.log")
	os.WriteFile(p, []byte("time=2026-11-12T04:34:59.449Z level=INFO msg=tool_call command=computer_observe args=x success=true\n"), 0o644)
	tail := &logTail{path: p, offset: fileSize(p)}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("time=2026-11-12T04:35:01.907Z level=INFO msg=tool_call command=computer_act args=\"{a}\" success=true error_code=<nil>\n" +
		"time=2026-11-12T04:35:02.000Z level=INFO msg=tool_call command=computer_act args=x success=false error_code=stopped\n" +
		"time=2026-11-12T04:35:03.000Z level=INFO msg=tool_call command=comp")
	if got := tail.acts(); len(got) != 1 || got[0].Format(time.RFC3339Nano) != "2026-11-12T04:35:01.907Z" {
		t.Fatalf("first read: %v", got)
	}
	f.WriteString("uter_act args=y success=true\n")
	f.Close()
	if got := tail.acts(); len(got) != 1 || got[0].Second() != 3 {
		t.Fatalf("second read: %v", got)
	}
	if got := tail.acts(); len(got) != 0 {
		t.Fatalf("third read: %v", got)
	}
}
