package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/install"
)

// startCmd is what the app-menu entry, the desktop shortcut, the login autostart entry and the
// jarvis-agent://start link run: start the agent. A URL argument must be exactly the start link;
// nothing is ever read from it.
func startCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	login := fs.Bool("login", false, "called at login by the autostart entry")
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
	return install.Start(ctx, e, s, *login)
}
