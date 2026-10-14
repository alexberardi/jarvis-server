package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// startCmd is what the app-menu entry, the shortcuts, opening the macOS app, the login
// autostart entry and the jarvis-agent://start link run (installers plan item 1.9). A URL
// argument must be exactly the start link; nothing is ever read from it.
func startCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	login := fs.Bool("login", false, "called at login by the autostart entry: no pill, no setup page")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch fs.NArg() {
	case 0:
	case 1:
		if err := install.CheckStartURL(fs.Arg(0)); err != nil {
			return err
		}
	default:
		return errors.New("usage: jarvis-agent start [--login] [jarvis-agent://start]")
	}
	e, err := installEnv()
	if err != nil {
		return err
	}
	s, err := installSys(io.Discard)
	if err != nil {
		return err
	}
	return newStarter(e, s).start(ctx, *login)
}

// starter is start's decisions, with what it touches swappable for tests.
type starter struct {
	env      install.Env
	sys      install.Sys
	send     func(cmd string) error // to the running agent
	waitSend func(cmd string) error // to an agent that is starting
	enrolled func() bool
	install  func() error // this user's first launch of a packaged copy
	firstRun func() error // nothing set up yet
}

func newStarter(e install.Env, s install.Sys) starter {
	return starter{
		env:  e,
		sys:  s,
		send: func(cmd string) error { return control.Send(controlDial, cmd) },
		waitSend: func(cmd string) error {
			return control.WaitSend(context.Background(), controlDial, cmd, 100, func() { time.Sleep(100 * time.Millisecond) })
		},
		enrolled: enrolled,
		install: func() error {
			ops, err := install.Plan(e, install.Options{Browser: true})
			if err != nil {
				return err
			}
			return install.Apply(context.Background(), ops, s, install.ApplyOptions{})
		},
		firstRun: firstRun,
	}
}

// start: an agent already running flashes its pill (and nothing else happens); otherwise a
// packaged copy sets this user up on its first launch, a computer that isn't set up goes to the
// first-run page, and the agent starts the way its login item would, then shows the pill.
func (st starter) start(ctx context.Context, login bool) error {
	probe := control.ShowRunning
	if login {
		probe = control.Ping
	}
	if st.send(probe) == nil {
		return nil
	}
	if !install.Installed(st.env, st.sys) && install.SetsUpOnFirstLaunch(st.env) {
		if err := st.install(); err != nil {
			return fmt.Errorf("setting up %s for this user: %w", st.env.Exe, err)
		}
	}
	if !st.enrolled() {
		if login {
			return nil
		}
		return st.firstRun()
	}
	if err := install.Start(ctx, st.env, st.sys, login); err != nil {
		return err
	}
	if login {
		return nil
	}
	if err := st.waitSend(control.ShowRunning); err != nil {
		return fmt.Errorf("started the agent, but it isn't answering: %w", err)
	}
	return nil
}

// enrolled reports whether this computer has its node credentials.
func enrolled() bool {
	p, err := node.DefaultCredsPath()
	if err != nil {
		return false
	}
	_, err = node.LoadCreds(p)
	return err == nil
}

// openedAsApp: macOS started the app bundle's binary with no command (or the old -psn_ one).
func openedAsApp() bool {
	if runtime.GOOS != "darwin" || (len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-psn_")) {
		return false
	}
	exe, err := agentExe()
	return err == nil && install.BundleOf(exe) != ""
}
