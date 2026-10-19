package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
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

// runningPill is the "Jarvis is running" pill, asked for by whoever opens the agent (the control
// socket's show-running, and on macOS the app's reopen and link events). The socket answers
// from the agent's first moment, while the island comes up only after the tools (and voice) are
// ready, and install and start ask the moment the socket answers. So a request before the
// island exists is kept and shown when it attaches, not dropped (what hid the pill on start).
type runningPill struct {
	mu       sync.Mutex
	show     func() // the island's ShowRunning, once attached
	attached bool
	pending  bool
}

// Show flashes the pill now, or once the island is attached.
func (p *runningPill) Show() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.attached {
		p.pending = true
		return
	}
	if p.show != nil {
		p.show()
	}
}

// Attach connects the island (nil: there is none, so nothing will show) and shows a pill asked
// for meanwhile.
func (p *runningPill) Attach(show func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.show, p.attached = show, true
	if p.pending && show != nil {
		show()
	}
	p.pending = false
}

// appEvents is what opening the macOS app, or its jarvis-agent:// link, does while this agent
// runs: LaunchServices sends the running agent an Apple event instead of starting `start`.
func appEvents(pill *runningPill, log *slog.Logger) island.AppEvents {
	return island.AppEvents{
		Reopen: pill.Show,
		OpenURL: func(u string) {
			if err := install.CheckStartURL(u); err != nil {
				log.Warn("ignored a link that isn't jarvis-agent://start", "err", err)
				return
			}
			pill.Show()
		},
	}
}

// serveControl answers the control socket until ctx ends: show-running flashes the island's
// pill (when there is an island), quit stops the agent, voice-on/off is the status icon's
// toggle (voice is nil until the switch exists), computer-stop and computer-status stop and
// report what Jarvis is doing with the computer. It returns a func that waits for it.
func serveControl(ctx context.Context, ln net.Listener, log *slog.Logger, pill *runningPill, quit func(), voice func() *voiceSwitch,
	health func() error, activity *computer.Activity, computerSw *computer.Switch) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := control.ServeAnswers(ctx, ln, func(cmd string) (string, error) {
			switch cmd {
			case control.Health:
				return "", health()
			case control.ComputerStatus:
				return activity.String(), nil
			case control.ComputerStop:
				log.Info("asked to stop what Jarvis is doing with the computer", "activity", activity.String())
				if activity.Stop() {
					return "stopped", nil
				}
				return "idle", nil
			case control.ComputerOn, control.ComputerOff:
				if computerSw == nil {
					return "", errors.New("computer control isn't available on this device")
				}
				on := cmd == control.ComputerOn
				log.Info("asked to turn computer control "+map[bool]string{true: "on", false: "off"}[on], "activity", activity.String())
				if err := computerSw.Set(on); err != nil {
					log.Warn("computer control: couldn't save the choice; it lasts until the agent restarts", "err", err)
				}
				if on {
					return "on", nil
				}
				return "off", nil
			case control.ShowRunning:
				// No island (--no-overlay, no display): nothing to show, but it is running.
				pill.Show()
			case control.Quit:
				log.Info("asked to quit (stop, uninstall, or a reinstall replacing this agent)")
				quit()
			case control.VoiceOn, control.VoiceOff:
				sw := voice()
				if sw == nil {
					return "", errors.New("the agent is still starting")
				}
				if !sw.available {
					return "", errors.New("listening isn't supported on this computer")
				}
				sw.Set(cmd == control.VoiceOn)
			}
			return "", nil
		}, version)
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

// signedOutToSetup turns a run that ended in node.ErrSignedOut into "not set up": it deletes
// only the credentials file (decision 2026-10-14: agent.json and apps.json stay), opens the
// setup page so someone can sign in again, and returns errNotSetUp, whose exit status no
// supervisor restarts. Any other result passes through.
//
// When the file already holds another node, this computer was enrolled again while this run
// was going, and jarvisd retired this run's node in favour of the new one (decision
// 2026-10-15-reenroll-replaces-node): the new sign-in stays, no setup page opens, and the error
// is an ordinary failure, so a supervisor restarts the agent on the new credentials.
func signedOutToSetup(err error, credsFile, nodeID string, log *slog.Logger, openSetup func()) error {
	if !errors.Is(err, node.ErrSignedOut) {
		return err
	}
	if c, lerr := node.LoadCreds(credsFile); lerr == nil && c.NodeID != nodeID {
		log.Info("this node was replaced by a newer sign-in of this computer; keeping the new credentials", "node", nodeID, "new_node", c.NodeID)
		return fmt.Errorf("replaced by node %s: %w", c.NodeID, err)
	}
	if rmErr := os.Remove(credsFile); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		log.Error("signed out, but couldn't delete the credentials", "path", credsFile, "err", rmErr)
		return rmErr
	}
	log.Info("signed out of Jarvis Automation: credentials deleted, settings kept; opening the setup page")
	if openSetup != nil {
		openSetup()
	}
	return errNotSetUp{err}
}
