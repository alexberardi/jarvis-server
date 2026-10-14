package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// listenControl opens this agent's control socket; browser.ErrInUse when another agent of this
// user holds it.
func listenControl() (net.Listener, error) {
	p, err := browser.ControlSocketPath()
	if err != nil {
		return nil, err
	}
	return browser.Listen(p)
}

// controlDial reaches the running agent's control socket.
func controlDial() (net.Conn, error) {
	p, err := browser.ControlSocketPath()
	if err != nil {
		return nil, err
	}
	return browser.Dial(p)
}

// serveControl answers the control socket until ctx ends: show-running flashes the island's
// pill (when there is an island), quit stops the agent. It returns a func that waits for it.
func serveControl(ctx context.Context, ln net.Listener, log *slog.Logger, shown *atomic.Pointer[island.Island], quit func()) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := control.Serve(ctx, ln, func(cmd string) error {
			switch cmd {
			case control.ShowRunning:
				// No island (--no-overlay, no display): nothing to show, but it is running.
				shown.Load().ShowRunning()
			case control.Quit:
				log.Info("asked to quit (uninstall, or a reinstall replacing this agent)")
				quit()
			}
			return nil
		})
		if err != nil {
			log.Warn("control socket stopped", "err", err)
		}
	}()
	return func() { <-done }
}

// exitNotSetUp is run's exit status when this computer isn't enrolled yet: the systemd unit
// names it in RestartPreventExitStatus=, so the login item doesn't restart an agent that has
// nothing to do until someone sets it up (EX_CONFIG).
const exitNotSetUp = 78

// errNotSetUp carries exitNotSetUp out of run.
type errNotSetUp struct{ err error }

func (e errNotSetUp) Error() string { return e.err.Error() }
func (e errNotSetUp) Unwrap() error { return e.err }

// notSetUp wraps a missing enrollment so main exits with exitNotSetUp; other errors pass.
func notSetUp(err error) error {
	if errors.Is(err, node.ErrNotEnrolled) {
		return errNotSetUp{err}
	}
	return err
}

// exitCode is main's exit status for run's error. launchd restarts a KeepAlive job on any
// failure, so under the agent's own LaunchAgent "not set up" is a clean exit there.
func exitCode(err error) int {
	var ns errNotSetUp
	if errors.As(err, &ns) {
		if os.Getenv("XPC_SERVICE_NAME") == appid.ID {
			return 0
		}
		return exitNotSetUp
	}
	return 1
}

// firstRun is where a start with nothing set up goes. TODO(installers 1.5): open the agent's
// first-run page (server, pairing, browser, permissions, voice) once it exists. Until then it
// says how to set up from a terminal.
var firstRun = func() error {
	return fmt.Errorf("this computer isn't set up yet. The setup page is coming; for now run: " +
		"jarvis-agent enroll --server http://<jarvisd-host>:7700 --email you@example.com")
}
