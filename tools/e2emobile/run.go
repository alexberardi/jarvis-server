package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// userSerial is the user's own dev emulator. The harness never sends it a command.
const userSerial = "emulator-5554"

type config struct {
	flowsDir, rel, out      string
	coverage, nav           string // e2e/coverage.json and the checkout's src/navigation; both or neither
	maestro, adb, serial    string
	platform                string   // android (also when empty) or ios
	xcrun, udid, deviceName string   // ios: the harness simulator, by UDID and the name it was created with
	process, bundleID       string   // ios: the app's executable (its log) and bundle id (simctl push)
	env                     []string // KEY=VALUE, passed to every flow as -e
	filter                  string
	flowTimeout, adbTimeout time.Duration
	// reverse are the adb reverse tunnels ("tcp:7700=tcp:18700") and healthPort the emulator's
	// port whose /health must answer 200 through them: prepDevice sets them before every flow.
	reverse    []string
	healthPort string
	probePause time.Duration // between health probes (default 2 s)
	// After a drop: the pause before a retry (default 5 s, then twice that), recoverDevice's
	// poll (default 250 ms), how long the device must stay up before the retry (default 30 s)
	// and how long it waits for that (default 2 min).
	retryPause, pollPause, settle, recoverTimeout time.Duration
	// now is recoverDevice's clock for its settle window (nil: time.Now); tests step it, so a
	// slow runner can't end the window early.
	now     func() time.Time
	retries int // the retries after a drop this run (results.json)
}

// Record is one flow in results.json: the record shape of e2e/web/reporter.ts (merged into the
// run's report.json by scripts/lib/e2e_report.py), plus skip_reason and attachments. Paths are
// relative to the run directory (the parent of --out).
type Record struct {
	Suite         string   `json:"suite"`
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	File          string   `json:"file"`
	Line          int      `json:"line"`
	Status        string   `json:"status"` // passed, failed, timedOut, skipped
	DurationMS    int64    `json:"duration_ms"`
	Error         *string  `json:"error"`
	Screenshots   []string `json:"screenshots"`
	Trace         *string  `json:"trace"`
	ConsoleErrors []string `json:"console_errors"`
	SkipReason    string   `json:"skip_reason,omitempty"`
	Attachments   []string `json:"attachments,omitempty"`
	// Note says the flow was run twice: the emulator's adb transport dropped, or it was not ready, on the first try.
	// On iOS it says the simulator was rebooted after the flow failed (rebootSimulator).
	Note string `json:"note,omitempty"`
}

// Results is results.json.
type Results struct {
	Suite      string   `json:"suite"`
	Status     string   `json:"status"`
	DurationMS int64    `json:"duration_ms"`
	Tests      []Record `json:"tests"`
	// DeviceRetries counts the retries after an adb transport drop (or a device not ready) in the
	// run: a lane that measures drops rather than the app shows here.
	DeviceRetries int `json:"device_retries,omitempty"`
}

// errNoFlows: nothing to run; scripts/e2e reports the suite as not run (an error), not as green.
var errNoFlows = errors.New("no flows")

func (c *config) ios() bool { return c.platform == "ios" }

// suite is the name of the suite in the run's report: mobile (Android) or ios.
func (c *config) suite() string {
	if c.ios() {
		return "ios"
	}
	return "mobile"
}

// device is what Maestro's --device names: the emulator's serial or the simulator's UDID.
func (c *config) device() string {
	if c.ios() {
		return c.udid
	}
	return c.serial
}

// run plays every flow one at a time (each with its own Maestro process, logcat cleared before
// it, and its artifacts on failure) and writes <out>/results.json.
func run(ctx context.Context, c *config, flows []Flow) (*Results, error) {
	if c.ios() {
		if err := c.checkSimulator(ctx); err != nil {
			return nil, err
		}
	} else if c.serial == "" || c.serial == userSerial {
		return nil, fmt.Errorf("refusing serial %q: the harness only drives its own emulator", c.serial)
	}
	if len(flows) == 0 {
		return nil, errNoFlows
	}
	if err := os.MkdirAll(c.out, 0o755); err != nil {
		return nil, err
	}
	if c.ios() {
		url, stop, err := c.startPush()
		if err != nil {
			return nil, err
		}
		defer stop()
		cc := *c
		cc.env = append(slices.Clip(c.env), "PUSH_URL="+url)
		c = &cc
	}
	t0 := time.Now()
	res := &Results{Suite: c.suite(), Status: "passed", Tests: []Record{}}
	if c.coverage != "" {
		cr, rec, err := coverage(c.coverage, c.nav, c.flowsDir, c.rel, c.out)
		if err != nil {
			msg := err.Error()
			rec.Status, rec.Error = "failed", &msg
		}
		fmt.Fprintf(os.Stderr, "e2emobile: %-8s %s (%d screens: %d covered, %d manual, %d pending)\n",
			rec.Status, rec.Title, cr.Screens, len(cr.Covered), len(cr.Manual), len(cr.Pending))
		if rec.Error != nil {
			fmt.Fprint(os.Stderr, *rec.Error)
			res.Status = "failed"
		}
		res.Tests = append(res.Tests, rec)
	}
	reboot := -1 // ios: the failed flow in res.Tests to reboot the simulator after, before the next one runs
	for _, f := range flows {
		if ctx.Err() != nil {
			break
		}
		if reboot >= 0 && !f.Skipped() {
			res.Tests[reboot].Note = c.rebootSimulator(ctx)
			fmt.Fprintf(os.Stderr, "e2emobile:          %s\n", res.Tests[reboot].Note)
			reboot = -1
		}
		var r Record
		if f.Skipped() || c.ios() { // the simulator has no adb transport to drop
			r, _ = c.runFlow(ctx, f)
		} else {
			r = c.retryOnDrop(ctx, c.flowRecord(f), func() (Record, string) { return c.runFlow(ctx, f) })
		}
		fmt.Fprintf(os.Stderr, "e2emobile: %-8s %s (%s, %.1f s)\n", r.Status, f.Name, f.File, float64(r.DurationMS)/1000)
		if r.Note != "" {
			fmt.Fprintf(os.Stderr, "e2emobile:          %s\n", r.Note)
		}
		if r.Status != "passed" && r.Status != "skipped" {
			res.Status = "failed"
			if c.ios() {
				reboot = len(res.Tests)
			}
		}
		res.Tests = append(res.Tests, r)
	}
	if ctx.Err() != nil {
		res.Status = "interrupted"
	}
	res.DurationMS = time.Since(t0).Milliseconds()
	res.DeviceRetries = c.retries
	if c.retries > 0 {
		fmt.Fprintf(os.Stderr, "e2emobile: %d retries after an adb transport drop this run\n", c.retries)
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(filepath.Join(c.out, "results.json"), append(raw, '\n'), 0o644)
}

func (c *config) flowRecord(f Flow) Record {
	return Record{Suite: c.suite(), ID: f.Slug, Title: f.Name, File: f.File, Line: 1, Status: "skipped",
		Screenshots: []string{}, ConsoleErrors: []string{}}
}

// runFlow plays one flow; it also returns Maestro's output, for retryOnDrop.
func (c *config) runFlow(ctx context.Context, f Flow) (Record, string) {
	r := c.flowRecord(f)
	if f.Skipped() {
		r.SkipReason = f.SkipReason
		return r, ""
	}
	dir := filepath.Join(c.out, f.Slug)
	// A retry keeps the earlier try's artifacts (<slug>.try1, ...): they show what the drop did.
	if _, err := os.Stat(dir); err == nil {
		for n := 1; ; n++ {
			if old := fmt.Sprintf("%s.try%d", dir, n); !exists(old) {
				_ = os.Rename(dir, old)
				break
			}
		}
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return failed(r, err.Error()), ""
	}
	rel := func(p string) string {
		s, err := filepath.Rel(filepath.Dir(c.out), p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(s)
	}
	started := time.Now() // ios: the start of this flow's part of the simulator's log
	if !c.ios() {
		_, _ = c.adbCmd(ctx, "logcat", "-c") // this flow's log only
	}

	junit, debug, logf := filepath.Join(dir, "junit.xml"), filepath.Join(dir, "maestro"), filepath.Join(dir, "maestro.log")
	args := []string{"--device", c.device(), "test", f.Path, "--format", "junit", "--output", junit, "--debug-output", debug}
	for _, kv := range c.env {
		args = append(args, "-e", kv)
	}
	fctx, cancel := context.WithTimeout(ctx, c.flowTimeout)
	defer cancel()
	var log bytes.Buffer
	cmd := exec.CommandContext(fctx, c.maestro, args...)
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	err := cmd.Run()
	r.DurationMS = time.Since(start).Milliseconds()
	_ = os.WriteFile(logf, log.Bytes(), 0o644)
	r.Attachments = []string{rel(logf)}
	if err == nil {
		r.Status = "passed"
		return r, log.String()
	}

	msg := junitFailure(junit)
	if msg == "Unknown error" { // Maestro's text for any exception that is not its own
		msg = exceptionLine(log.String())
	}
	if msg == "" {
		msg = tail(log.String(), 20)
	}
	if fctx.Err() == context.DeadlineExceeded {
		r.Status = "timedOut"
		msg = fmt.Sprintf("the flow did not finish in %s\n%s", c.flowTimeout, msg)
	} else {
		r.Status = "failed"
	}
	if msg == "" {
		msg = err.Error()
	}
	r.Error = &msg

	if c.ios() {
		c.iosArtifacts(ctx, dir, started, &r, rel)
	} else {
		c.androidArtifacts(ctx, dir, &r, rel)
	}
	if st, err := os.Stat(debug); err == nil && st.IsDir() {
		r.Attachments = append(r.Attachments, rel(debug))
	}
	return r, log.String() + "\n" + msg
}

// androidArtifacts keeps the screen as the flow left it: a picture, the view tree (texts,
// testIDs) and the logcat.
func (c *config) androidArtifacts(ctx context.Context, dir string, r *Record, rel func(string) string) {
	png := filepath.Join(dir, "screenshot.png")
	if out, err := c.adbCmd(ctx, "exec-out", "screencap -p"); err == nil && bytes.HasPrefix(out, []byte("\x89PNG")) &&
		os.WriteFile(png, out, 0o644) == nil {
		r.Screenshots = append(r.Screenshots, rel(png))
	}
	if _, err := c.adbCmd(ctx, "shell", "uiautomator dump /data/local/tmp/e2emobile.xml >/dev/null"); err == nil {
		if out, err := c.adbCmd(ctx, "exec-out", "cat /data/local/tmp/e2emobile.xml"); err == nil && len(out) > 0 {
			p := filepath.Join(dir, "window.xml")
			if os.WriteFile(p, out, 0o644) == nil {
				r.Attachments = append(r.Attachments, rel(p))
			}
		}
	}
	if out, err := c.adbCmd(ctx, "logcat", "-d"); err == nil {
		p := filepath.Join(dir, "logcat.txt")
		if os.WriteFile(p, out, 0o644) == nil {
			r.Attachments = append(r.Attachments, rel(p))
		}
		r.ConsoleErrors = jsErrors(string(out), 20)
	}
}

// deviceDropped matches what an adb transport drop leaves in Maestro's output or adb's errors
// (adb's log has "emulator-5580: connection terminated: read failed"; every reverse tunnel goes
// with it).
var deviceDropped = regexp.MustCompile(`device offline|device '?[\w.:-]+'? not found|no devices/emulators found|DeviceServerDied|connection terminated`)

// prepDevice readies the harness emulator for a flow. An adb transport drop takes every reverse
// tunnel with it, so before each flow: wait for the device, set the tunnels again (adb reverse
// replaces one that is there; never --remove-all, which would be the only global call) and
// prove the sandbox answers from inside the emulator.
func (c *config) prepDevice(ctx context.Context) error {
	if _, err := c.adbCmd(ctx, "wait-for-device"); err != nil {
		return err
	}
	for _, p := range c.reverse {
		local, remote, _ := strings.Cut(p, "=")
		if _, err := c.adbCmd(ctx, "reverse", local, remote); err != nil {
			return err
		}
	}
	if c.healthPort == "" {
		return nil
	}
	// toybox nc drops the connection at stdin's EOF, hence the sleep.
	probe := fmt.Sprintf("(printf 'GET /health HTTP/1.0\\r\\n\\r\\n'; sleep 2) | nc 127.0.0.1 %s", c.healthPort)
	pause := c.probePause
	if pause == 0 {
		pause = 2 * time.Second
	}
	for i := 1; ; i++ {
		out, err := c.adbCmd(ctx, "shell", probe)
		if err == nil && bytes.Contains(out, []byte(" 200 ")) {
			return nil
		}
		if i == 3 {
			why := "no 200"
			if err != nil {
				why = err.Error()
			}
			return fmt.Errorf("the emulator can't reach the sandbox through adb reverse (localhost:%s/health): %s", c.healthPort, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pause):
		}
	}
}

// maxTries is how often a flow is played when the emulator's adb transport keeps dropping (the
// first try and two retries): drops came about every 20 s on a bad run, so one retry is not enough.
const maxTries = 3

// retryOnDrop readies the device and plays a test (play returns its record and the output to
// look for a drop in). When the device was not ready or the test failed on an adb transport
// drop, it brings the device back (recoverDevice), waits a little longer each time and plays it
// again, up to maxTries in all; it keeps the last result and notes the drops on it.
func (c *config) retryOnDrop(ctx context.Context, base Record, play func() (Record, string)) Record {
	// once returns why the try deserves another ("" when it does not).
	once := func() (Record, string) {
		if err := c.prepDevice(ctx); err != nil {
			return failed(base, "the emulator was not ready for it (adb device, reverse tunnels, sandbox /health): "+err.Error()),
				"the emulator was not ready"
		}
		r, out := play()
		if r.Status != "passed" && r.Status != "skipped" && deviceDropped.MatchString(out) {
			return r, "the emulator's adb transport dropped"
		}
		return r, ""
	}
	pause := c.retryPause
	if pause == 0 {
		pause = 5 * time.Second
	}
	r, why := once()
	var notes []string
	for try := 2; why != "" && try <= maxTries && ctx.Err() == nil; try++ {
		c.retries++
		first := ""
		if r.Error != nil {
			first, _, _ = strings.Cut(strings.TrimSpace(*r.Error), "\n")
			if len(first) > 300 {
				first = first[:300] + "…"
			}
		}
		notes = append(notes, fmt.Sprintf("%s on try %d%s (%s)", why, try-1, hostLoad(), first))
		fmt.Fprintf(os.Stderr, "e2emobile: %s: %s%s (%s); bringing the emulator back and retrying (try %d of %d)\n",
			r.Title, why, hostLoad(), first, try, maxTries)
		select {
		case <-ctx.Done():
			return r
		case <-time.After(time.Duration(try-1) * pause):
		}
		if err := c.recoverDevice(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "e2emobile: %s: the emulator did not come back: %v\n", r.Title, err)
		}
		r, why = once()
	}
	if why != "" && len(notes) > 0 && r.Error != nil {
		// Every try ended on a drop: the harness failed, not the flow, and the failure says so
		// first (the report shows only the error text in its failure section).
		e := fmt.Sprintf("%s on all %d tries (harness, not the flow): %s", why, len(notes)+1, *r.Error)
		r.Error = &e
	}
	if len(notes) > 0 {
		r.Note = fmt.Sprintf("retried %d× (tries %d of %d): %s", len(notes), len(notes)+1, maxTries, strings.Join(notes, "; "))
	}
	return r
}

// recoverDevice brings the harness emulator back after an adb transport drop, before a retry:
//   - when adb does not see it as a device, `adb -s <serial> reconnect`, which kicks this one
//     transport (host-serial:<serial>:reconnect). Not `reconnect offline`: that is a host-wide
//     command and would reset every offline device, the user's included;
//   - then it waits until the device is up and its shell answers (get-state is device and
//     sys.boot_completed is 1) on every check, a poll apart, for the settle window. The emulator
//     drops its adb transport in bursts (docs/dev/e2e.md, adb transport drops): adb has it back
//     in under a second, it drops again 15-20 s later, and a retry started inside the burst only
//     fails again. One check that is not up starts the window over;
//   - then it stops Maestro's on-device driver (its two packages, by name), which a drop leaves
//     dead or half-connected (DeviceServerDied on the next run); the next `maestro test`
//     installs and starts it afresh.
//
// prepDevice then sets the tunnels again and probes the sandbox, as before every flow.
func (c *config) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *config) recoverDevice(ctx context.Context) error {
	poll := c.pollPause
	if poll == 0 {
		poll = 250 * time.Millisecond // a drop is gone again in 0.5-0.8 s
	}
	settle := c.settle
	if settle == 0 {
		settle = 30 * time.Second
	}
	limit := c.recoverTimeout
	if limit == 0 {
		limit = 2 * time.Minute
	}
	deadline := c.clock().Add(limit)
	stable, sinceKick := 0, -1
	var upSince time.Time
	var last string
	for {
		state, err := c.adbCmd(ctx, "get-state")
		up := err == nil && strings.TrimSpace(string(state)) == "device"
		if up {
			boot, err := c.adbCmd(ctx, "shell", "getprop sys.boot_completed")
			up = err == nil && strings.TrimSpace(string(boot)) == "1"
			if !up {
				last = fmt.Sprintf("sys.boot_completed is %q (%v)", strings.TrimSpace(string(boot)), err)
			}
		} else {
			last = fmt.Sprintf("get-state: %q (%v)", strings.TrimSpace(string(state)), err)
		}
		if up {
			if stable == 0 {
				upSince = c.clock()
			}
			stable++
		} else {
			stable = 0
			// Kick the transport at once, then again if it stays down for 20 polls.
			if sinceKick < 0 || sinceKick >= 20 {
				_, _ = c.adbCmd(ctx, "reconnect")
				sinceKick = 0
			}
		}
		if sinceKick >= 0 {
			sinceKick++
		}
		if stable >= 2 && c.clock().Sub(upSince) >= settle {
			break
		}
		if c.clock().After(deadline) {
			if stable > 0 {
				last = fmt.Sprintf("up for only %s of %s", c.clock().Sub(upSince).Round(time.Second), settle)
			}
			return fmt.Errorf("not back in %s: %s", limit, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	for _, pkg := range []string{"dev.mobile.maestro.test", "dev.mobile.maestro"} {
		_, _ = c.adbCmd(ctx, "shell", "am force-stop "+pkg)
	}
	return nil
}

// hostLoad is ", host load L, M GB available" (the 1-minute load average and MemAvailable) on
// Linux, "" elsewhere: the host a drop happened on was busy and deep in swap, so each retry
// says how busy it was.
func hostLoad() string {
	out := ""
	if f := strings.Fields(readFile("/proc/loadavg")); len(f) > 0 {
		out = ", host load " + f[0]
	}
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		var kb int64
		if _, err := fmt.Sscanf(l, "MemAvailable: %d kB", &kb); err == nil {
			out += fmt.Sprintf(", %.1f GB available", float64(kb)/(1<<20))
		}
	}
	return out
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func readFile(p string) string {
	raw, _ := os.ReadFile(p)
	return string(raw)
}

func failed(r Record, msg string) Record {
	r.Status = "failed"
	r.Error = &msg
	return r
}

// adbCmd runs adb against the harness emulator only (`-s serial` on every call).
func (c *config) adbCmd(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.adbTimeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.adb, append([]string{"-s", c.serial}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("adb %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// junitFailure is the failure text in Maestro's JUnit report, if it wrote one.
func junitFailure(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Cases []struct {
			Failures []struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			} `xml:"failure"`
			Errors []struct {
				Message string `xml:"message,attr"`
				Text    string `xml:",chardata"`
			} `xml:"error"`
		} `xml:"testsuite>testcase"`
	}
	if xml.Unmarshal(raw, &doc) != nil {
		return ""
	}
	var parts []string
	for _, tc := range doc.Cases {
		for _, f := range append(tc.Failures, tc.Errors...) {
			for _, s := range []string{f.Message, f.Text} {
				if s = strings.TrimSpace(s); s != "" && !contains(parts, s) {
					parts = append(parts, s)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

// exceptionLine is the first line of Maestro's output that names an exception ("Exception in
// thread "main" " cut off), or "" when there is none.
func exceptionLine(log string) string {
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "at ") || !strings.Contains(l, "Exception") {
			continue
		}
		if i := strings.Index(l, "Exception in thread "); i >= 0 {
			rest := l[i+len("Exception in thread "):]
			if _, after, ok := strings.Cut(rest, " "); ok {
				l = strings.TrimSpace(after)
			}
		}
		return l
	}
	return ""
}

// jsErrors picks the app's JavaScript errors out of a logcat (threadtime format: the level
// letter, then the tag): React Native's console.error and uncaught exceptions.
func jsErrors(logcat string, max int) []string {
	out := []string{}
	for _, l := range strings.Split(logcat, "\n") {
		l = strings.TrimSpace(l)
		if (strings.Contains(l, " E ReactNativeJS") || strings.Contains(l, " E AndroidRuntime")) && len(out) < max {
			out = append(out, l)
		}
	}
	return out
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
