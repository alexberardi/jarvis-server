package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// computerCmd is `jarvis-agent computer stop|status`: stop what Jarvis is doing with this
// computer (a computer task, a recording), as the status icon's "Stop Jarvis" does, or say
// what it is doing. Both ask the running agent over its control socket. (`computer snapshot`
// is computerSnapshotCmd, which main dispatches before this.)
func computerCmd(args []string, w io.Writer, dial control.Dialer) error {
	if len(args) != 1 || (args[0] != "stop" && args[0] != "status") {
		return errors.New("usage: jarvis-agent computer stop|status|snapshot URL")
	}
	cmd := control.ComputerStatus
	if args[0] == "stop" {
		cmd = control.ComputerStop
	}
	r, err := control.Ask(dial, cmd)
	if err != nil {
		return err
	}
	switch {
	case cmd == control.ComputerStop && r.Answer == "stopped":
		fmt.Fprintln(w, "Stopped.")
	case cmd == control.ComputerStop:
		fmt.Fprintln(w, "Jarvis wasn't using this computer.")
	case r.Answer == "idle" || r.Answer == "":
		fmt.Fprintln(w, "Jarvis isn't using this computer.")
	default:
		// "controlling: Ordering the blue mug" (computer.Activity's String).
		kind, text, _ := strings.Cut(r.Answer, ": ")
		line := map[string]string{string(computer.Controlling): "Jarvis is controlling this computer",
			string(computer.Paused): "Jarvis is paused", string(computer.Recording): "Jarvis is recording"}[kind]
		if line == "" {
			line = r.Answer
		} else if text != "" {
			line += ": " + text
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

// islandActivity is the island's state for an activity.
func islandActivity(k computer.Kind) island.State {
	switch k {
	case computer.Controlling:
		return island.Controlling
	case computer.Paused:
		return island.Paused
	case computer.Recording:
		return island.Recording
	}
	return island.Hidden
}

// --- jarvis-agent computer snapshot URL: computer control's browser calls, by hand ---
//
// The computer-task executor calls these directly (they are not model tools, so `try` can't
// reach them). This debug command serves the extension itself, like `try`, so it needs no
// running agent holding the browser socket.

// computerSnapshotCmd is `jarvis-agent computer snapshot`; args follow "snapshot".
func computerSnapshotCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("computer snapshot", flag.ExitOnError)
	wait := fs.Duration("wait", 10*time.Second, "how long to wait for the browser extension to connect")
	maxEls := fs.Int("max", browser.DefaultSnapshotMax, "at most this many elements")
	asJSON := fs.Bool("json", false, "print the snapshot as JSON")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: jarvis-agent computer snapshot [--wait 10s] [--max 300] [--json] URL")
	}
	hub, stopBrowser := startBrowser(ctx, logger())
	defer stopBrowser()
	wctx, cancel := context.WithTimeout(ctx, *wait)
	connected := hub.WaitConnected(wctx)
	cancel()
	if !connected {
		return fmt.Errorf("no browser extension connected in %s (if a jarvis-agent is running, it holds browser control: stop it first)", *wait)
	}
	bt := browser.NewTools(hub)
	tab, window, err := bt.OpenBackground(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "opened tab %s in background window %s (left open)\n", tab, window)
	snap, err := settledSnapshot(ctx, bt, tab, *maxEls)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	printSnapshot(os.Stdout, snap)
	return nil
}

// settledSnapshot snapshots tab once the new page has loaded (its first moments are about:blank,
// which the browser doesn't let extensions script, or an empty document).
func settledSnapshot(ctx context.Context, bt *browser.Tools, tab string, max int) (computeruse.Snapshot, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := bt.Snapshot(ctx, tab, max)
		if (err == nil && len(s.Elements) > 0) || time.Now().After(deadline) {
			return s, err
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func printSnapshot(w io.Writer, s computeruse.Snapshot) {
	fmt.Fprintf(w, "gen %d  %s  %q  (%d elements)\n", s.Gen, s.URL, s.Title, len(s.Elements))
	for _, e := range s.Elements {
		var b strings.Builder
		fmt.Fprintf(&b, "%-5s %-10s %q", e.Ref, e.Role, e.Name)
		if e.Type != "" {
			fmt.Fprintf(&b, " type=%s", e.Type)
		}
		if e.Autocomplete != "" {
			fmt.Fprintf(&b, " autocomplete=%s", e.Autocomplete)
		}
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		if len(e.State) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(e.State, " "))
		}
		if e.Frame != 0 {
			fmt.Fprintf(&b, " frame=%d", e.Frame)
		}
		fmt.Fprintln(w, b.String())
	}
}
