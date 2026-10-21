package main

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// #407: on macOS the waiting login item never execs itself into the agent (its menu-bar icon
// didn't show on the first run); launchd starts a fresh one, as at a later login.
func TestHandOff(t *testing.T) {
	exe := "/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent"
	cases := []struct {
		name, goos, job string
		want            []string
	}{
		{"macOS login item", "darwin", "net.jarvisautomation.agent",
			[]string{"launchctl", "kickstart", "-k", "gui/501/net.jarvisautomation.agent"}},
		{"macOS outside launchd", "darwin", "", []string{exe, "run", "--no-tray"}},
		{"macOS another job", "darwin", "application.com.apple.Terminal", []string{exe, "run", "--no-tray"}},
		{"Linux", "linux", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := handOff(c.goos, c.job, 501, exe, []string{"run", "--no-tray"})
			if !slices.Equal(got, c.want) {
				t.Fatalf("handOff = %q, want %q", got, c.want)
			}
		})
	}
}

// fakeListener blocks in Accept until it is closed.
type fakeListener struct {
	once   sync.Once
	closed chan struct{}
}

func (l *fakeListener) Accept() (net.Conn, error) { <-l.closed; return nil, net.ErrClosed }
func (l *fakeListener) Close() error              { l.once.Do(func() { close(l.closed) }); return nil }
func (l *fakeListener) Addr() net.Addr            { return &net.UnixAddr{Name: "fake", Net: "unix"} }

// The first run's hand-off: once the page saved the credentials, the waiting login item lets
// go of the control socket before the agent starts in its place, so the new one can take it.
func TestFirstRunHandsOffAfterClosingTheSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")
	defer func(d time.Duration) { setupPollEvery = d }(setupPollEvery)
	setupPollEvery = 5 * time.Millisecond
	ctl := &fakeListener{closed: make(chan struct{})}
	socketOpen := make(chan bool, 1)
	defer func(f func(*slog.Logger) error) { reexec = f }(reexec)
	reexec = func(*slog.Logger) error {
		select {
		case <-ctl.closed:
			socketOpen <- false
		default:
			socketOpen <- true
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- waitForSetup(context.Background(), slog.New(slog.DiscardHandler), path, true, ctl, true)
	}()
	if err := node.SaveCreds(path, node.Creds{ConfigURL: "http://cfg", NodeID: "n1", NodeKey: "k"}); err != nil {
		t.Fatal(err)
	}
	select {
	case open := <-socketOpen:
		if open {
			t.Fatal("handed off while still holding the control socket")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("didn't hand off once set up")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
