package control

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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

// Every answer names the agent's version, and health fails until the handler says it is
// connected (the self-update's check that a new version came up).
func TestHealthNamesTheVersion(t *testing.T) {
	dir, err := shortDir(t)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	var up bool
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeVersion(ctx, ln, func(cmd string) error {
			mu.Lock()
			defer mu.Unlock()
			if cmd == Health && !up {
				return errors.New("not connected to jarvisd yet")
			}
			return nil
		}, "v1.2.3")
	}()
	t.Cleanup(func() { cancel(); <-done })
	dial := func() (net.Conn, error) { return browser.Dial(path) }
	r, err := Ask(dial, Health)
	if err == nil || r.Version != "v1.2.3" {
		t.Fatalf("health before connecting: %+v %v", r, err)
	}
	mu.Lock()
	up = true
	mu.Unlock()
	if r, err := Ask(dial, Health); err != nil || r.Version != "v1.2.3" {
		t.Fatalf("health: %+v %v", r, err)
	}
	if r, err := Ask(dial, Ping); err != nil || r.Version != "v1.2.3" {
		t.Fatalf("ping: %+v %v", r, err)
	}
}

// computer-status and computer-stop: the answer carries what Jarvis is doing with the
// computer, and what stopping did (jarvis-agent computer status|stop prints it).
func TestComputerVerbs(t *testing.T) {
	for _, c := range []string{ComputerStop, ComputerStatus} {
		if !slices.Contains(Commands, c) {
			t.Fatalf("%s isn't a command", c)
		}
	}
	dir, err := shortDir(t)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	activity := "controlling: Ordering the blue mug"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = ServeAnswers(ctx, ln, func(cmd string) (string, error) {
			switch cmd {
			case ComputerStatus:
				return activity, nil
			case ComputerStop:
				activity = "idle"
				return "stopped", nil
			}
			return "", nil
		}, "v9")
	}()
	t.Cleanup(func() { cancel(); <-done })
	dial := func() (net.Conn, error) { return browser.Dial(path) }
	for _, step := range []struct{ cmd, want string }{
		{ComputerStatus, "controlling: Ordering the blue mug"},
		{ComputerStop, "stopped"},
		{ComputerStatus, "idle"},
		{Ping, ""},
	} {
		r, err := Ask(dial, step.cmd)
		if err != nil || r.Answer != step.want || r.Version != "v9" {
			t.Fatalf("%s: %+v %v, want %q", step.cmd, r, err, step.want)
		}
	}
}

// Stopping a recording waits for the browser's last events (up to 10 s): longer than
// IOTimeout, and both ends wait for it.
func TestRecordStopOutlastsIOTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits past IOTimeout")
	}
	if Timeout(ComputerRecordStop) < 10*time.Second || Timeout(ComputerRecordStart) < 10*time.Second || Timeout(Ping) != IOTimeout {
		t.Fatalf("timeouts: stop %v, start %v, ping %v", Timeout(ComputerRecordStop), Timeout(ComputerRecordStart), Timeout(Ping))
	}
	dial, _ := serve(t, func(cmd string) error {
		if cmd == ComputerRecordStop {
			time.Sleep(IOTimeout + 500*time.Millisecond)
		}
		return nil
	})
	if err := Send(dial, ComputerRecordStop); err != nil {
		t.Fatal(err)
	}
}
