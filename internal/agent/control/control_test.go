package control

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
)

// serve starts a control socket on a short scratch path and returns its dialer.
func serve(t *testing.T, h Handler) (Dialer, string) {
	t.Helper()
	dir, err := shortDir(t)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = Serve(ctx, ln, h) }()
	t.Cleanup(func() { cancel(); <-done })
	return func() (net.Conn, error) { return browser.Dial(path) }, path
}

func shortDir(t *testing.T) (string, error) {
	d, err := mkShortTemp()
	if err == nil {
		t.Cleanup(func() { removeAll(d) })
	}
	return d, err
}

func TestShowRunningAndQuit(t *testing.T) {
	var mu sync.Mutex
	var got []string
	dial, _ := serve(t, func(cmd string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, cmd)
		return nil
	})
	for _, c := range []string{ShowRunning, Quit} {
		if err := Send(dial, c); err != nil {
			t.Fatal(c, err)
		}
	}
	if err := Send(dial, "rm -rf"); err == nil {
		t.Fatal("unknown command accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != ShowRunning || got[1] != Quit {
		t.Fatalf("handled %q", got)
	}
}

func TestHandlerErrorIsReturned(t *testing.T) {
	dial, _ := serve(t, func(string) error { return errors.New("no island") })
	if err := Send(dial, ShowRunning); err == nil || err.Error() != "no island" {
		t.Fatal(err)
	}
}

// The single-instance lock: a second listener on a live socket is refused.
func TestSecondInstanceIsRefused(t *testing.T) {
	_, path := serve(t, func(string) error { return nil })
	if _, err := browser.Listen(path); !errors.Is(err, browser.ErrInUse) {
		t.Fatalf("second listen: %v", err)
	}
}

func TestNotRunning(t *testing.T) {
	dial := func() (net.Conn, error) { return nil, errors.New("connection refused") }
	if err := Send(dial, ShowRunning); !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
	pauses := 0
	if err := WaitSend(context.Background(), dial, ShowRunning, 5, func() { pauses++ }); !errors.Is(err, ErrNotRunning) || pauses != 4 {
		t.Fatal(err, pauses)
	}
}

func TestWaitSendSucceedsOnceUp(t *testing.T) {
	real, _ := serve(t, func(string) error { return nil })
	tries := 0
	dial := func() (net.Conn, error) {
		tries++
		if tries < 3 {
			return nil, errors.New("not yet")
		}
		return real()
	}
	if err := WaitSend(context.Background(), dial, ShowRunning, 10, func() {}); err != nil || tries != 3 {
		t.Fatal(err, tries)
	}
}
