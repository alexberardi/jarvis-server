// Command e2emobile is the mobile suite of scripts/e2e: it plays the Maestro flows of a
// jarvis-node-mobile checkout's .maestro/ on the harness emulator (always addressed by serial),
// one Maestro process per flow, and writes results.json in the record shape of the web suite.
// On a failure it keeps the screen (screenshot, uiautomator dump), the logcat and Maestro's
// debug output in <out>/<flow>/.
//
//	e2emobile run --flows DIR --out DIR --serial emulator-5580 [--maestro PATH] [--adb PATH]
//	              [--rel .maestro] [--filter RE] [--flow-timeout 10m] [-e KEY=VALUE ...]
//	e2emobile list --flows DIR [--filter RE]   the flows it would run, and the skipped ones
//	e2emobile realpush --out DIR --serial S --login-flow FILE [--maestro PATH] [--adb PATH]
//	              [--skip REASON] [-e KEY=VALUE ...]
//	              the real push lane (realpush.go): sign in, wait for the app's push token, send
//	              a node alert, find it in the shade, tap it, check the app opened it. The
//	              sandbox comes from the environment (sandbox.env: SBX_NOTIFICATIONS, SBX_CC,
//	              SBX_TOKEN, SBX_USER_ID, SBX_NODE_ID, SBX_NODE_KEY). --skip records the lane as
//	              skipped with that reason and runs nothing.
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
	"strconv"
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
	if len(os.Args) < 2 || (os.Args[1] != "run" && os.Args[1] != "list" && os.Args[1] != "realpush") {
		fmt.Fprintln(os.Stderr, "usage: e2emobile run|list|realpush ...")
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "realpush" {
		os.Exit(mainRealPush(os.Args[2:]))
	}
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

func mainRealPush(args []string) int {
	fs := flag.NewFlagSet("realpush", flag.ContinueOnError)
	pc := &pushConfig{tokenWait: 2 * time.Minute, shadeWait: 3 * time.Minute, openWait: 30 * time.Second, poll: 3 * time.Second}
	var env envFlag
	fs.StringVar(&pc.out, "out", "", "output directory (required)")
	fs.StringVar(&pc.maestro, "maestro", "maestro", "the maestro binary")
	fs.StringVar(&pc.adb, "adb", "adb", "the adb binary")
	fs.StringVar(&pc.serial, "serial", "", "the harness emulator's serial (required unless --skip)")
	fs.StringVar(&pc.loginFlow, "login-flow", "", "the mobile repo's login subflow (required unless --skip)")
	fs.StringVar(&pc.skipReason, "skip", "", "record the lane as skipped with this reason")
	fs.Var(&env, "e", "KEY=VALUE for the login flow (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	pc.env, pc.adbTimeout = env, 60*time.Second
	if pc.out == "" {
		fmt.Fprintln(os.Stderr, "e2emobile realpush: --out is required")
		return 2
	}
	if pc.skipReason == "" {
		pc.notifications, pc.cc, pc.userToken = os.Getenv("SBX_NOTIFICATIONS"), os.Getenv("SBX_CC"), os.Getenv("SBX_TOKEN")
		pc.nodeKey = os.Getenv("SBX_NODE_ID") + ":" + os.Getenv("SBX_NODE_KEY")
		uid, err := strconv.ParseInt(os.Getenv("SBX_USER_ID"), 10, 64)
		if pc.serial == "" || pc.loginFlow == "" || pc.notifications == "" || pc.cc == "" || pc.userToken == "" ||
			pc.nodeKey == ":" || err != nil {
			fmt.Fprintln(os.Stderr, "e2emobile realpush: needs --serial, --login-flow and the sandbox's env (source sandbox.env)")
			return 2
		}
		pc.userID = uid
	} else if pc.serial == "" {
		pc.serial = "none" // nothing runs
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	res, err := realPush(ctx, pc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2emobile:", err)
		return 1
	}
	t := res.Tests[0]
	switch t.Status {
	case "skipped":
		fmt.Fprintf(os.Stderr, "e2emobile: skipped  real push: %s\n", t.SkipReason)
	case "passed":
		fmt.Fprintf(os.Stderr, "e2emobile: passed   real push (%.1f s)\n", float64(t.DurationMS)/1000)
	default:
		fmt.Fprintf(os.Stderr, "e2emobile: %-8s real push: %s\n", t.Status, *t.Error)
		return 1
	}
	return 0
}
