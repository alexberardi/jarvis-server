// Command e2emobile is the mobile suite of scripts/e2e: it plays the Maestro flows of a
// jarvis-node-mobile checkout's .maestro/ on the harness emulator (always addressed by serial),
// one Maestro process per flow, and writes results.json in the record shape of the web suite.
// On a failure it keeps the screen (screenshot, uiautomator dump), the logcat and Maestro's
// debug output in <out>/<flow>/.
//
//	e2emobile run --flows DIR --out DIR --serial emulator-5580 [--maestro PATH] [--adb PATH]
//	              [--rel .maestro] [--filter RE] [--flow-timeout 10m] [-e KEY=VALUE ...]
//	e2emobile list --flows DIR [--filter RE]   the flows it would run, and the skipped ones
//
// The emulator, the APK, the adb reverse tunnels and the sandbox are the script's job. Exit
// status: 0 all passed (or skipped), 1 a flow failed, 3 no flows to run, 2 bad usage.
// Contract with the flows: docs/decisions/2026-10-18-e2e-mobile-runner.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"
)

type envFlag []string

func (e *envFlag) String() string { return strings.Join(*e, ",") }
func (e *envFlag) Set(s string) error {
	if k, _, ok := strings.Cut(s, "="); !ok || k == "" {
		return fmt.Errorf("-e %q: want KEY=VALUE", s)
	}
	*e = append(*e, s)
	return nil
}

func parseConfig(cmd string, args []string) (*config, *regexp.Regexp, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	c := &config{}
	var env envFlag
	fs.StringVar(&c.flowsDir, "flows", "", "the checkout's .maestro directory (required)")
	fs.StringVar(&c.rel, "rel", ".maestro", "the flows directory's path in the checkout (for the report)")
	fs.StringVar(&c.out, "out", "", "output directory (required for run)")
	fs.StringVar(&c.maestro, "maestro", "maestro", "the maestro binary")
	fs.StringVar(&c.adb, "adb", "adb", "the adb binary")
	fs.StringVar(&c.serial, "serial", "", "the harness emulator's serial (required for run)")
	fs.StringVar(&c.filter, "filter", "", "run only flows whose name or file name matches this regexp")
	fs.DurationVar(&c.flowTimeout, "flow-timeout", 10*time.Minute, "give up on a flow after this long")
	fs.Var(&env, "e", "KEY=VALUE for every flow (repeatable)")
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	c.env, c.adbTimeout = env, 60*time.Second
	if c.flowsDir == "" {
		return nil, nil, errors.New("--flows is required")
	}
	if cmd == "run" && (c.out == "" || c.serial == "") {
		return nil, nil, errors.New("--out and --serial are required")
	}
	var re *regexp.Regexp
	if c.filter != "" {
		var err error
		if re, err = regexp.Compile(c.filter); err != nil {
			return nil, nil, fmt.Errorf("--filter: %w", err)
		}
	}
	return c, re, nil
}

func main() {
	if len(os.Args) < 2 || (os.Args[1] != "run" && os.Args[1] != "list") {
		fmt.Fprintln(os.Stderr, "usage: e2emobile run|list ...")
		os.Exit(2)
	}
	cmd := os.Args[1]
	c, re, err := parseConfig(cmd, os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2emobile:", err)
		os.Exit(2)
	}
	flows, err := Discover(c.flowsDir, c.rel, re)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2emobile:", err)
		os.Exit(1)
	}
	if cmd == "list" {
		for _, f := range flows {
			state := "run"
			if f.Skipped() {
				state = "skip: " + f.SkipReason
			}
			fmt.Printf("%s\t%s\t%s\n", f.File, f.Name, state)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := run(ctx, c, flows)
	switch {
	case errors.Is(err, errNoFlows):
		what := "no Maestro flows in " + c.rel
		if c.filter != "" {
			what = fmt.Sprintf("no Maestro flow in %s matches --filter %q", c.rel, c.filter)
		}
		fmt.Fprintln(os.Stderr, "e2emobile:", what)
		os.Exit(3)
	case err != nil:
		fmt.Fprintln(os.Stderr, "e2emobile:", err)
		os.Exit(1)
	case res.Status != "passed":
		os.Exit(1)
	}
}
