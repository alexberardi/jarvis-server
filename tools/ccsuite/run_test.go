package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// A run's model calls are the log's lines after the offset, kept in the run's own llm.jsonl.
func TestCopyLines(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "llm.jsonl")
	os.WriteFile(log, []byte("{\"a\":1}\n"), 0o644)
	offset := fileSize(log)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{\"b\":2}\n\n{\"c\":3}\n")
	f.Close()
	dst := filepath.Join(dir, "run-1", "llm.jsonl")
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if n := copyLines(log, offset, dst); n != 2 {
		t.Fatalf("rounds = %d", n)
	}
	if b, _ := os.ReadFile(dst); string(b) != "{\"b\":2}\n{\"c\":3}\n" {
		t.Fatalf("copy = %q", b)
	}
	if n := copyLines(filepath.Join(dir, "missing"), 0, dst); n != 0 {
		t.Fatalf("missing log: %d", n)
	}
}

func TestSkipReason(t *testing.T) {
	browser := Case{Name: "web_form", Phase: "browser", Status: "required", Target: "browser"}
	for _, c := range []struct {
		c       Case
		llm     string
		pending bool
		want    string
	}{
		{browser, "shared", false, ""},
		{browser, "scripted", false, "the scripted model does no computer tasks"},
		{Case{Phase: "browser", Status: "pending"}, "shared", false, "pending"},
		{Case{Phase: "browser", Status: "pending"}, "shared", true, ""},
		{Case{Phase: "tests", Status: "required"}, "scripted", false, ""},
	} {
		if got := skipReason(c.c, c.llm, c.pending, "linux"); got != c.want {
			t.Errorf("%+v %s: %q, want %q", c.c, c.llm, got, c.want)
		}
	}
}

// fakeClock is a clock whose waits only move its time on.
type fakeClock struct{ t time.Time }

func (f *fakeClock) clock() clock {
	return clock{now: func() time.Time { return f.t }, wait: func(ctx context.Context, d time.Duration) bool {
		f.t = f.t.Add(d)
		return ctx.Err() == nil
	}}
}

// fakeSite is an inbox (and the sites) for the card loop: items is the inbox at a time since
// the start; onTap sees each tap.
type fakeSite struct {
	start time.Time
	clk   *fakeClock
	items func(since time.Duration) []inboxItem
	onTap func(it inboxItem, el element)
	taps  []string
}

func (f *fakeSite) inbox(context.Context) ([]inboxItem, error) {
	return f.items(f.clk.t.Sub(f.start)), nil
}
func (f *fakeSite) sitesRaw(context.Context) ([]byte, error) { return []byte("{}"), nil }
func (f *fakeSite) tap(_ context.Context, it inboxItem, el element) error {
	f.taps = append(f.taps, it.ID+":"+el.Callback)
	if f.onTap != nil {
		f.onTap(it, el)
	}
	return nil
}

func progressCard(id, title, body, state string) inboxItem {
	it := inboxItem{ID: id, Title: title, Body: body, Category: "errand_plan"}
	it.Metadata.CardType, it.Metadata.State, it.Metadata.WorkflowID = "errand_progress", state, "wf-"+id
	if state == "running" || state == "waiting" {
		it.Metadata.Elements = []element{{ID: "stop-errand", Label: "Stop", Command: "errand", Callback: "stop_errand"}}
	}
	return it
}

// An errand's progress card that keeps changing is the errand still working: the run goes on
// past --settle until the completion card, and each card is kept at its last text.
func TestWatchCardsProgressEditsKeepTheRunGoing(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	site := &fakeSite{start: clk.t, clk: clk}
	site.items = func(since time.Duration) []inboxItem {
		old := inboxItem{ID: "old", Title: "an earlier run's card", Category: "errand"}
		if since >= 5*time.Minute {
			return []inboxItem{old, progressCard("p", "Budget", "done", "done"),
				{ID: "c", Title: "✅ Errand done", Body: "spreadsheet saved", Category: "errand"}}
		}
		return []inboxItem{old, progressCard("p", "Budget", fmt.Sprintf("step %d", since/(40*time.Second)), "running")}
	}
	chatDone := make(chan struct{})
	close(chatDone)
	cards, ended := watchCards(context.Background(), site, clk.clock(), nil, map[string]bool{"old": true}, chatDone, time.Minute)
	if !ended {
		t.Fatal("not ended")
	}
	if got := clk.t.Sub(site.start); got < 5*time.Minute {
		t.Fatalf("the run ended on settle after %v, while the progress card still changed", got)
	}
	if len(cards) != 2 || cards[0].ID != "p" || cards[0].Body != "done" || !cards[1].Done() {
		t.Fatalf("cards = %+v", cards)
	}
	if len(site.taps) != 0 {
		t.Fatalf("taps = %v", site.taps)
	}
}

// Quiet for --settle after the chat's answer ends the run, as before.
func TestWatchCardsSettles(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	site := &fakeSite{start: clk.t, clk: clk, items: func(time.Duration) []inboxItem {
		return []inboxItem{{ID: "a", Title: "I couldn't do that", Category: "errand_plan"}}
	}}
	chatDone := make(chan struct{})
	close(chatDone)
	cards, ended := watchCards(context.Background(), site, clk.clock(), nil, nil, chatDone, time.Minute)
	if got := clk.t.Sub(site.start); !ended || len(cards) != 1 || got > 70*time.Second {
		t.Fatalf("ended %v after %v, cards %+v", ended, got, cards)
	}
}

// An errand still running when its run ends gets its Stop tapped, and the run waits until it
// reports stopped; finished errands and other cards with a Stop are left alone.
func TestStopErrandsTapsStopOnRunningErrands(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	state := "running"
	site := &fakeSite{start: clk.t, clk: clk}
	site.items = func(time.Duration) []inboxItem {
		replan := inboxItem{ID: "r", Title: "More steps?", Category: "errand_plan"}
		replan.Metadata.Elements = []element{{Label: "Stop", Callback: "stop_errand"}}
		replan.Metadata.State = "waiting" // only a progress card says how its errand is
		return []inboxItem{progressCard("p", "Budget", "step 2", state), progressCard("q", "Old", "done", "done"), replan}
	}
	site.onTap = func(it inboxItem, el element) {
		if it.ID == "p" && el.Callback == "stop_errand" {
			state = "stopped"
		}
	}
	stopped, err := stopErrands(context.Background(), site, clk.clock(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(site.taps, []string{"p:stop_errand"}) || !reflect.DeepEqual(stopped, []string{"Budget"}) {
		t.Fatalf("taps %v, stopped %v", site.taps, stopped)
	}
}

// An errand that never stops is an error once the limit is up, after a few Stop taps.
func TestStopErrandsGivesUp(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	site := &fakeSite{start: clk.t, clk: clk, items: func(time.Duration) []inboxItem {
		return []inboxItem{progressCard("p", "Budget", "step 1", "running")}
	}}
	stopped, err := stopErrands(context.Background(), site, clk.clock(), time.Minute)
	if err == nil || len(stopped) != 1 {
		t.Fatalf("stopped %v, err %v", stopped, err)
	}
	if n := len(site.taps); n < 2 || n > 6 {
		t.Fatalf("%d taps in a minute", n)
	}
}
