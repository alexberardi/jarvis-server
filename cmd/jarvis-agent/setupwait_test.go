package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/tray"
)

// fakeWait scripts setupWait: ticks are sent by the test, nothing waits on a clock.
type fakeWait struct {
	setUp   atomic.Bool
	noTray  bool
	shown   []tray.State
	actions tray.Actions
	closed  int
	pages   int
	tick    chan time.Time
	trayUp  chan struct{}
}

func (f *fakeWait) wait(marker string) setupWait {
	f.tick = make(chan time.Time)
	f.trayUp = make(chan struct{}, 1)
	return setupWait{
		log:      slog.New(slog.DiscardHandler),
		enrolled: f.setUp.Load,
		tick:     f.tick,
		showTray: func(st tray.State, a tray.Actions) (func(), error) {
			defer func() { f.trayUp <- struct{}{} }()
			if f.noTray {
				return nil, tray.ErrNoHost
			}
			f.shown, f.actions = append(f.shown, st), a
			return func() { f.closed++ }, nil
		},
		openSetup: func() { f.pages++ },
		marker:    marker,
	}
}

func goWait(ctx context.Context, w setupWait) chan error {
	done := make(chan error, 1)
	go func() { done <- w.wait(ctx) }()
	return done
}

// settle ticks until the wait returns: it may already have seen the credentials after the
// previous tick, before waiting for the next.
func settle(f *fakeWait, done chan error) error {
	for {
		select {
		case f.tick <- time.Time{}:
		case err := <-done:
			return err
		}
	}
}

func TestNotSetUpAtLoginWaitsInTheStatusIcon(t *testing.T) {
	f := &fakeWait{}
	w := f.wait(filepath.Join(t.TempDir(), setupOfferedMarker))
	done := goWait(context.Background(), w)
	<-f.trayUp
	f.tick <- time.Time{} // still not set up: keeps waiting
	if len(f.shown) != 1 || f.shown[0].Status != "Not set up yet" || !f.shown[0].Setup || f.shown[0].SignOut || f.shown[0].VoiceAvailable {
		t.Fatalf("menu %+v", f.shown)
	}
	// "Set up / Settings…" opens the page; nothing opened it before.
	if f.pages != 0 {
		t.Fatal("the page opened by itself while the icon shows")
	}
	f.actions.Setup()
	if f.pages != 1 {
		t.Fatal("Set up didn't open the page")
	}
	// The page saved the credentials: the next tick starts the agent.
	f.setUp.Store(true)
	if err := settle(f, done); err != nil {
		t.Fatal(err)
	}
	if f.closed != 1 {
		t.Fatal("the waiting icon stayed up")
	}
}

func TestNotSetUpAtLoginQuit(t *testing.T) {
	f := &fakeWait{}
	done := goWait(context.Background(), f.wait(""))
	<-f.trayUp
	f.actions.Quit()
	f.actions.Quit() // twice is fine
	if err := <-done; !errors.Is(err, errQuitWhileWaiting) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done = goWait(ctx, f.wait(""))
	<-f.trayUp
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestNoStatusIconOffersThePageOnlyAtTheFirstLogin(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "jarvis-agent", setupOfferedMarker)
	f := &fakeWait{noTray: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := goWait(ctx, f.wait(marker))
	<-f.trayUp
	f.tick <- time.Time{}
	cancel()
	<-done
	if f.pages != 1 {
		t.Fatalf("first login: %d pages", f.pages)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("no marker:", err)
	}
	// The next login: no page again, but it still waits (setup from the menu starts the agent).
	f.setUp.Store(true)
	if err := <-goWait(context.Background(), f.wait(marker)); err != nil || f.pages != 1 {
		t.Fatalf("second login: %v, %d pages", err, f.pages)
	}
}

func TestAlreadySetUpReturnsAtOnce(t *testing.T) {
	f := &fakeWait{}
	f.setUp.Store(true)
	if err := <-goWait(context.Background(), f.wait("")); err != nil {
		t.Fatal(err)
	}
}
