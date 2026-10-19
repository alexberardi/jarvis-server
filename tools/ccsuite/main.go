// Command ccsuite is the computer-task suite (docs/dev/computer-suite.md): it brings up a
// sandbox, the fixture sites (tools/ccsites) and, when a case needs them, a headless desktop
// session (sway, or Xvfb for X11) with the real jarvis-agent and Playwright's Chromium carrying
// the browser extension; then it says each case's errand from the seeded user to the agent,
// answers the phone's cards as the case says, and scores the case from the fixtures' own state
// (orders placed, files downloaded, spreadsheets, notes), never from the model's words alone.
//
//	ccsuite run [--filter RE] [--repeat N] [--llm shared|scripted] [--label live|background]
//	            [--include-pending] [--out DIR] [--keep] [--settle 1m] [--chromium PATH]
//
// Writes report.md, report.json and results.json (the E2E report's suite shape, for
// scripts/e2e computer) into .ccsuite/<run>/ (or --out). Pending cases are skipped unless
// --include-pending, and never fail the run; a required case that fails exits 1, a run that
// could not bring its world up exits 2.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := dispatch(ctx, os.Args[1:], os.Stdout)
	var fail errFailed
	switch {
	case errors.As(err, &fail):
		fmt.Fprintln(os.Stderr, "ccsuite:", err)
		os.Exit(1)
	case err != nil:
		fmt.Fprintln(os.Stderr, "ccsuite:", err)
		os.Exit(2)
	}
}

func dispatch(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: ccsuite run [--filter RE] [--repeat N] [--llm shared|scripted] [--label live|background] [--include-pending] [--out DIR] [--keep]")
	}
	return runCmd(ctx, args[1:], stdout)
}

// errFailed is a run whose required cases failed (exit 1, already reported).
type errFailed struct{ n int }

func (e errFailed) Error() string { return fmt.Sprintf("%d required case(s) failed", e.n) }
