package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/agent/control"

	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/tray"
)

// The login item on a computer that isn't set up yet (DESIGN §13, "Not set up at login"): `run`
// started by systemd, launchd or the Run key, or spawned by the autostart entry's
// `start --login`, doesn't exit at once with nothing on screen. It shows the status icon,
// "Jarvis Automation Agent — Not set up yet" with "Set up / Settings…" and Quit, and opens no
// browser window. Where the desktop shows no status icon it opens the setup page instead, but
// only at the first such login (a marker next to the credentials). Once the computer is set up
// (the page saved node.json) it becomes the agent in place: the same process image again
// (exec), so the supervisor that started it keeps supervising it.

// setupPollEvery is how often a waiting login item looks for the credentials the setup page saves.
const setupPollEvery = time.Second

// setupOfferedMarker is the file that records the setup page was opened at a login.
const setupOfferedMarker = "setup-offered-at-login"

// errQuitWhileWaiting: Quit was chosen in the waiting icon's menu.
var errQuitWhileWaiting = errors.New("quit before setup")

// setupWait is the wait, with what it touches swappable for tests.
type setupWait struct {
	log      *slog.Logger
	enrolled func() bool
	tick     <-chan time.Time
	// showTray puts the waiting icon up; an error means this desktop shows none.
	showTray func(tray.State, tray.Actions) (closeTray func(), err error)
	// openSetup opens the setup page (`jarvis-agent setup`, a process of its own).
	openSetup func()
	// marker is setupOfferedMarker's path ("" never opens the page by itself).
	marker string
	// quit closes when the control socket asks this agent to quit (nil: never).
	quit <-chan struct{}
}

// waitingState is the waiting icon's menu.
func waitingState() tray.State { return tray.State{Status: "Not set up yet", Setup: true} }

// wait shows the waiting icon (or offers the page once) and returns nil once the computer is
// set up, errQuitWhileWaiting on Quit, or ctx's error.
func (w setupWait) wait(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	quit := make(chan struct{})
	quitOnce := func() {
		select {
		case <-quit:
		default:
			close(quit)
		}
	}
	closeTray, err := w.showTray(waitingState(), tray.Actions{Setup: w.openSetup, Quit: quitOnce})
	if err != nil {
		w.log.Info("not set up yet; no status icon on this desktop", "reason", err)
		w.offerPageOnce()
	} else {
		w.log.Info("not set up yet: waiting, with \"Set up\" in the status icon's menu")
		defer closeTray()
	}
	for {
		if w.enrolled() {
			w.log.Info("this computer was set up: starting the agent")
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-quit:
			w.log.Info("Quit chosen in the menu before setup")
			return errQuitWhileWaiting
		case <-w.quit:
			w.log.Info("asked to quit before setup (stop, uninstall, or a reinstall replacing this agent)")
			return errQuitWhileWaiting
		case <-w.tick:
		}
	}
}

// offerPageOnce opens the setup page unless an earlier login already did.
func (w setupWait) offerPageOnce() {
	if w.marker == "" {
		return
	}
	if _, err := os.Stat(w.marker); err == nil {
		w.log.Info("the setup page was offered at an earlier login; not opening it again")
		return
	}
	if err := os.MkdirAll(filepath.Dir(w.marker), 0o700); err == nil {
		_ = os.WriteFile(w.marker, nil, 0o600)
	}
	w.openSetup()
}

// interactive: run was started from a terminal (someone typed it), not by a login item.
func interactive() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// waitingControl answers the control socket while the login item waits for setup: quit ends the
// wait (stop, uninstall, an installer replacing this binary), show-running (opening the agent
// again: its shortcut, the link) opens the setup page, ping answers; the rest need an agent
// that is set up. Once the page has saved the credentials, show-running opens nothing: this
// process is about to become the agent (the page's own start asks it).
func waitingControl(enrolled func() bool, openSetup, quit func()) control.Handler {
	return func(cmd string) error {
		switch cmd {
		case control.Quit:
			quit()
		case control.ShowRunning:
			if !enrolled() {
				openSetup()
			}
		case control.Health, control.VoiceOn, control.VoiceOff:
			return errors.New("this computer isn't set up yet")
		}
		return nil
	}
}

// waitForSetup is run on a computer that isn't set up, started by a login item: it waits as
// setupWait says, then becomes the agent (reexecRun). credsFile is where the page saves the
// credentials. ctl is the control socket run holds (nil when it couldn't): served meanwhile, so
// the waiting agent can be stopped, and closed before the agent starts in its place.
func waitForSetup(ctx context.Context, log *slog.Logger, credsFile string, noTray bool, ctl net.Listener) error {
	t := time.NewTicker(setupPollEvery)
	defer t.Stop()
	quit := make(chan struct{})
	var quitOnce sync.Once
	w := setupWait{
		log:       log,
		enrolled:  func() bool { _, err := node.LoadCreds(credsFile); return err == nil },
		tick:      t.C,
		openSetup: openSetupPage(log),
		marker:    filepath.Join(filepath.Dir(credsFile), setupOfferedMarker),
		quit:      quit,
	}
	stopControl := func() {}
	if ctl != nil {
		cctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			h := waitingControl(w.enrolled, w.openSetup, func() { quitOnce.Do(func() { close(quit) }) })
			if err := control.ServeVersion(cctx, ctl, h, version); err != nil {
				log.Warn("control socket stopped", "err", err)
			}
		}()
		stopControl = func() { cancel(); <-done }
	}
	var err error
	// macOS: the menu-bar icon needs AppKit's loop on the main thread.
	runErr := island.RunLoop(ctx, log, island.AppEvents{Reopen: w.openSetup}, func(loop bool) error {
		w.showTray = func(st tray.State, a tray.Actions) (func(), error) {
			if noTray {
				return nil, errors.New("hidden by the settings (tray: false or --no-tray)")
			}
			// On Linux the icon "starts" with no tray to show it (it waits for one); here only an
			// icon someone can see counts, or the page is offered instead.
			if p := tray.Probe(); strings.HasPrefix(p, "off") {
				return nil, errors.New(p)
			}
			tr, err := tray.Start(tray.Options{Log: log, State: st, Actions: a, MainLoop: loop})
			if err != nil {
				return nil, err
			}
			return tr.Close, nil
		}
		err = w.wait(ctx)
		return err
	})
	stopControl() // the agent that starts next takes the socket
	switch {
	case errors.Is(err, errQuitWhileWaiting):
		return nil
	case err != nil:
		return err
	case runErr != nil:
		return runErr
	}
	return reexecRun(log)
}
