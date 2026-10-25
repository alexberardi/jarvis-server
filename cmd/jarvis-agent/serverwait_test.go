package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/tray"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// The menu says what the agent is waiting for while its server hasn't answered — the user
// started the agent before the server — and offers "Retry now" instead of claiming it runs.
func TestTrayWaitsForTheServer(t *testing.T) {
	reached := false
	retries := 0
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), sw: newFakeVoice().sw(true), activity: &computer.Activity{},
		configURL: "http://127.0.0.1:7700", reached: func() bool { return reached }, retryNow: func() { retries++ }}

	s := ic.state()
	if !s.Waiting || s.Status != "Waiting for the Jarvis server at http://127.0.0.1:7700" {
		t.Fatalf("while waiting: %+v", s)
	}
	menu := labels(tray.Items(s))
	if !strings.Contains(menu, tray.RetryLabel) {
		t.Fatalf("no Retry now in %q", menu)
	}
	if voice.Supported && !strings.Contains(menu, tray.VoiceWaitText) {
		t.Fatalf("listening is on and waiting, but the menu doesn't say so: %q", menu)
	}
	ic.retryNowFromMenu()

	reached = true
	if s := ic.state(); s.Waiting || s.Status != "Running" {
		t.Fatalf("once reached: %+v", s)
	}
	if retries != 1 {
		t.Fatalf("Retry now ran %d times", retries)
	}
}

// labels renders a menu as text (tray's own test helper, kept in step with it).
func labels(items []tray.Item) string {
	var out []string
	for _, it := range items {
		switch {
		case it.Separator:
			out = append(out, "--")
		case it.Toggle && it.Checked:
			out = append(out, "[x] "+it.Label)
		case it.Toggle:
			out = append(out, "[ ] "+it.Label)
		case !it.Enabled:
			out = append(out, "("+it.Label+")")
		default:
			out = append(out, it.Label)
		}
	}
	return strings.Join(out, " | ")
}

// serverWait draws the menu again when the waiting starts and when the server answers, says
// it once on the island (the menu is what says it for as long as it lasts), and says
// "connected" only if the user was told it was waiting.
func TestServerWaitTellsTheUser(t *testing.T) {
	var mu sync.Mutex
	var notes []string
	refreshes := 0
	w := &serverWait{log: slog.New(slog.DiscardHandler),
		refresh: func() { mu.Lock(); refreshes++; mu.Unlock() },
		note:    func(text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }}

	w.waiting("http://127.0.0.1:7700", errors.New("connection refused"), 0)
	w.waiting("http://127.0.0.1:7700", errors.New("connection refused"), 0) // the next try
	mu.Lock()
	if refreshes != 1 || len(notes) != 1 || notes[0] != "Waiting for the Jarvis server at http://127.0.0.1:7700" {
		mu.Unlock()
		t.Fatalf("waiting: %d refreshes, notes %q", refreshes, notes)
	}
	mu.Unlock()

	w.reached("http://127.0.0.1:7700")
	mu.Lock()
	if refreshes != 2 || len(notes) != 2 || notes[1] != connectedNote {
		mu.Unlock()
		t.Fatalf("reached: %d refreshes, notes %q", refreshes, notes)
	}
	mu.Unlock()

	// A server that answered straight away: the menu is drawn again, but there was nothing to
	// say on the island.
	w2 := &serverWait{log: slog.New(slog.DiscardHandler),
		refresh: func() { mu.Lock(); refreshes++; mu.Unlock() },
		note:    func(text string) { mu.Lock(); notes = append(notes, text); mu.Unlock() }}
	w2.reached("http://127.0.0.1:7700")
	mu.Lock()
	defer mu.Unlock()
	if refreshes != 3 || len(notes) != 2 {
		t.Fatalf("straight away: %d refreshes, notes %q", refreshes, notes)
	}
}

// Turning listening on before the server answers says so (the user turned it on twice and
// nothing happened, #585); it still starts by itself once the agent connects.
func TestVoiceTurnedOnBeforeTheServerAnswers(t *testing.T) {
	f := newFakeVoice()
	sw := f.sw(false)
	sw.addIndicator(f)
	told := 0
	sw.waiting = func() { told++ }

	sw.Set(true) // not connected yet: nothing to listen with
	if _, _, runs, _, _, _ := f.state(); runs != 0 || told != 1 {
		t.Fatalf("%d runs, told %d times", runs, told)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { sw.Serve(ctx, &node.CC{}); close(served) }()
	recv(t, f.started, "the pipeline to start on connect")

	sw.Set(true) // on and connected: nothing more to say
	if told != 1 {
		t.Fatalf("told %d times once connected", told)
	}
	cancel()
	<-served
}

// Once the server answers, the web UI is looked for again at once: "Open Jarvis Automation"
// doesn't wait out findWebUI's own backoff (two minutes at worst).
func TestFindWebUILooksAgainWhenTheServerAnswers(t *testing.T) {
	var up atomic.Bool
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case hits <- struct{}{}:
		default:
		}
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"services": []map[string]string{
			{"name": svcWebUI, "url": "http://jarvis.example:7710/"}}})
	}))
	defer srv.Close()
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), sw: newFakeVoice().sw(false), activity: &computer.Activity{},
		configURL: srv.URL, webUI: make(chan struct{}, 1), webUIEvery: time.Hour} // only the wake-up can move it on
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); ic.findWebUI(ctx) }()

	select { // the first try, with the server still down
	case <-hits:
	case <-time.After(20 * time.Second):
		t.Fatal("findWebUI never asked the server")
	}
	up.Store(true)
	ic.lookForWebUI()
	select { // findWebUI returns once it knows the web UI's address
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("findWebUI didn't look again when the server answered")
	}
}

// lookForWebUI never blocks: with no loop listening, with one wake already due, or with no
// channel at all (the tests that build a trayIcon without one).
func TestLookForWebUIDoesntBlock(t *testing.T) {
	ic := &trayIcon{log: slog.New(slog.DiscardHandler), webUI: make(chan struct{}, 1)}
	ic.lookForWebUI()
	ic.lookForWebUI()
	if len(ic.webUI) != 1 {
		t.Fatalf("%d wakes queued", len(ic.webUI))
	}
	(&trayIcon{log: slog.New(slog.DiscardHandler)}).lookForWebUI()
}
