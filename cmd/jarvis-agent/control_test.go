package main

import (
	"io"
	"log/slog"
	"testing"
)

// The bug behind "no pill on start": install and start ask for the pill the moment the control
// socket answers, before run has its island. It must show once the island attaches.
func TestRunningPillAskedBeforeTheIsland(t *testing.T) {
	var p runningPill
	p.Show()
	p.Show() // twice before: still one pill
	n := 0
	p.Attach(func() { n++ })
	if n != 1 {
		t.Fatalf("pill shown %d times on attach, want 1", n)
	}
	p.Show()
	if n != 2 {
		t.Fatalf("after attach: shown %d times, want 2", n)
	}
}

func TestRunningPillNotAskedOrNoIsland(t *testing.T) {
	var p runningPill
	n := 0
	p.Attach(func() { n++ })
	if n != 0 {
		t.Fatal("shown on attach without being asked")
	}
	var none runningPill
	none.Show()
	none.Attach(nil) // --no-overlay, or no display: nothing to show, no panic
	none.Show()
}

func TestAppEventsShowThePill(t *testing.T) {
	got := make(chan struct{}, 4)
	var p runningPill
	p.Attach(func() { got <- struct{}{} })
	ev := appEvents(&p, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev.Reopen()
	<-got
	for _, u := range []string{"jarvis-agent://start", "jarvis-agent:start", "jarvis-agent://start/"} {
		ev.OpenURL(u)
		select {
		case <-got:
		default:
			t.Fatalf("%s: no pill", u)
		}
	}
	for _, u := range []string{"jarvis-agent://start?x=1", "jarvis-agent://quit", "https://example.com"} {
		ev.OpenURL(u)
		select {
		case <-got:
			t.Fatalf("%s: showed the pill", u)
		default:
		}
	}
}
