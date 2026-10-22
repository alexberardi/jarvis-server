package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// flowsDir is a .maestro/ like the mobile repo's: flows, a skipped one, a subflow, a config.
func flowsDir(t *testing.T) string {
	d := filepath.Join(t.TempDir(), ".maestro")
	write(t, filepath.Join(d, "login-admin.yaml"), "appId: com.jarvis.nodemobile\nname: login as the household admin\ntags: [auth]\n---\n- launchApp\n")
	write(t, filepath.Join(d, "chat-fail.yaml"), "appId: com.jarvis.nodemobile\nname: chat round trip\n---\n- assertVisible: nope\n")
	write(t, filepath.Join(d, "biometric-login.yaml"), "appId: com.jarvis.nodemobile\nname: Biometric login\ntags:\n  - auth\n  - skip\nproperties:\n  skip_reason: needs biometric enrollment, which a headless emulator has not got\n---\n- launchApp\n")
	write(t, filepath.Join(d, "bare.yml"), "- launchApp\n")
	write(t, filepath.Join(d, "config.yaml"), "flows:\n  - '*'\n")
	write(t, filepath.Join(d, "subflows", "login.yaml"), "appId: com.jarvis.nodemobile\n---\n- tapOn: x\n")
	write(t, filepath.Join(d, "README.md"), "# flows\n")
	return d
}

func TestDiscover(t *testing.T) {
	flows, err := Discover(flowsDir(t), ".maestro", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range flows {
		got = append(got, f.File+"|"+f.Name+"|"+f.SkipReason)
	}
	want := []string{
		".maestro/bare.yml|bare|",
		".maestro/biometric-login.yaml|Biometric login|needs biometric enrollment, which a headless emulator has not got",
		".maestro/chat-fail.yaml|chat round trip|",
		".maestro/login-admin.yaml|login as the household admin|",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("flows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !flows[1].Skipped() || flows[3].Skipped() {
		t.Fatal("only the skip-tagged flow is skipped")
	}
}

func TestDiscoverFilterMatchesNameOrFile(t *testing.T) {
	d := flowsDir(t)
	for re, want := range map[string]int{"household admin": 1, "^chat-": 1, "login": 2, "nothing" + "here": 0} {
		flows, err := Discover(d, ".maestro", regexp.MustCompile(re))
		if err != nil {
			t.Fatal(err)
		}
		if len(flows) != want {
			t.Errorf("--filter %q: %d flows, want %d", re, len(flows), want)
		}
	}
}

func TestSkipWithoutReasonSaysSo(t *testing.T) {
	d := filepath.Join(t.TempDir(), ".maestro")
	write(t, filepath.Join(d, "x.yaml"), "appId: a\ntags: [skip]\n---\n- launchApp\n")
	flows, err := Discover(d, ".maestro", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(flows) != 1 || !strings.Contains(flows[0].SkipReason, "no properties.skip_reason") {
		t.Fatalf("got %+v", flows)
	}
}

func TestBrokenHeaderIsAnError(t *testing.T) {
	d := filepath.Join(t.TempDir(), ".maestro")
	write(t, filepath.Join(d, "x.yaml"), "appId: [unclosed\n---\n- launchApp\n")
	if _, err := Discover(d, ".maestro", nil); err == nil || !strings.Contains(err.Error(), "x.yaml") {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

// fakes writes a maestro and an adb that log their arguments to calls.log. The maestro fails
// any flow whose path has "fail" in it (and writes a JUnit report saying why), sleeps on
// "slow", and on "drop" dies the way an adb transport drop kills it, the first time only
// ("droptwice": the first two times, "dropall": every time). A drop leaves the device offline to
// adb's next two get-state calls. The adb's /health probe answers 200 on the emulator's port 7700
// only.
func fakes(t *testing.T) (maestro, adb, calls string) {
	d := t.TempDir()
	calls = filepath.Join(d, "calls.log")
	maestro, adb = filepath.Join(d, "maestro"), filepath.Join(d, "adb")
	write(t, maestro, `#!/bin/sh
printf 'maestro %s\n' "$*" >>`+calls+`
out=
flow=
while [ $# -gt 0 ]; do
  case $1 in
    --output) out=$2; shift 2 ;;
    --debug-output) mkdir -p "$2"; echo cmds >"$2/commands.json"; shift 2 ;;
    test) flow=$2; shift 2 ;;
    hierarchy) echo '{"attributes": {"text": "Chat"}}'; exit 0 ;;
    *) shift ;;
  esac
done
case $flow in
  *slow*) sleep 3 ;;
  *dropall*)
    echo 2 >`+d+`/offline
    echo "Exception in thread main dadb.DeviceServerDied: device offline"
    exit 1 ;;
  *droptwice*)
    n=$(cat `+d+`/drops 2>/dev/null || echo 0)
    if [ "$n" -lt 2 ]; then
      echo $((n + 1)) >`+d+`/drops
      echo 2 >`+d+`/offline
      echo "Exception in thread main dadb.DeviceServerDied: device offline"
      exit 1
    fi ;;
  *drop*)
    if [ ! -f `+d+`/dropped ]; then
      touch `+d+`/dropped
      echo 2 >`+d+`/offline
      echo "Exception in thread main dadb.DeviceServerDied: device offline"
      exit 1
    fi ;;
  *fail*)
    echo "Assertion is false: \"84\" is visible"
    cat >"$out" <<X
<?xml version="1.0"?><testsuites><testsuite name="Test Suite"><testcase name="chat round trip"><failure message="Assertion is false: &quot;84&quot; is visible"></failure></testcase></testsuite></testsuites>
X
    exit 1 ;;
esac
echo "[Passed] ok"
`)
	write(t, adb, `#!/bin/sh
printf 'adb %s\n' "$*" >>`+calls+`
case "$*" in
  *" get-state")
    n=$(cat `+d+`/offline 2>/dev/null || echo 0)
    if [ "$n" -gt 0 ]; then
      echo $((n - 1)) >`+d+`/offline
      echo "error: device offline" >&2
      exit 1
    fi
    echo device ;;
  *"getprop sys.boot_completed"*) echo 1 ;;
  *"screencap -p"*) printf '\211PNG\r\n\032\nfake' ;;
  *"exec-out cat"*) echo '<hierarchy><node text="Chat"/></hierarchy>' ;;
  *"nc 127.0.0.1 7700"*) echo 'HTTP/1.0 200 OK' ;;
  *"logcat -d"*) echo '10-18 09:41:00.000  1234  1250 E ReactNativeJS: TypeError: x is undefined'; echo '10-18 09:41:00.000  1234  1250 I ReactNativeJS: fine' ;;
esac
`)
	return maestro, adb, calls
}

func TestRun(t *testing.T) {
	maestro, adb, calls := fakes(t)
	runDir := t.TempDir()
	c := &config{flowsDir: flowsDir(t), rel: ".maestro", out: filepath.Join(runDir, "mobile"), maestro: maestro, adb: adb,
		serial: "emulator-5580", env: []string{"EMAIL=a@b.c", "PASSWORD=pw"}, flowTimeout: time.Minute, adbTimeout: 10 * time.Second}
	flows, err := Discover(c.flowsDir, c.rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || len(res.Tests) != 4 {
		t.Fatalf("status %s, %d tests", res.Status, len(res.Tests))
	}

	var file Results
	raw, err := os.ReadFile(filepath.Join(c.out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	by := map[string]Record{}
	for _, r := range file.Tests {
		by[r.ID] = r
		if r.Suite != "mobile" || r.Screenshots == nil || r.ConsoleErrors == nil {
			t.Errorf("%s: suite %q, nil lists in %+v", r.ID, r.Suite, r)
		}
	}
	if s := by["login-admin"]; s.Status != "passed" || s.Error != nil || len(s.Screenshots) != 0 || s.Title != "login as the household admin" {
		t.Errorf("passing flow: %+v", s)
	}
	if s := by["biometric-login"]; s.Status != "skipped" || !strings.Contains(s.SkipReason, "biometric enrollment") {
		t.Errorf("skipped flow: %+v", s)
	}
	f := by["chat-fail"]
	if f.Status != "failed" || f.Error == nil || !strings.Contains(*f.Error, `"84" is visible`) {
		t.Fatalf("failing flow: %+v", f)
	}
	if strings.Join(f.Screenshots, ",") != "mobile/chat-fail/screenshot.png" {
		t.Errorf("screenshots %v (relative to the run dir)", f.Screenshots)
	}
	for _, a := range []string{"mobile/chat-fail/maestro.log", "mobile/chat-fail/window.xml", "mobile/chat-fail/logcat.txt", "mobile/chat-fail/maestro"} {
		if !contains(f.Attachments, a) {
			t.Errorf("attachments %v: no %s", f.Attachments, a)
		}
		if _, err := os.Stat(filepath.Join(runDir, a)); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	if len(f.ConsoleErrors) != 1 || !strings.Contains(f.ConsoleErrors[0], "TypeError") {
		t.Errorf("console errors %v: want the one E ReactNativeJS line", f.ConsoleErrors)
	}
	if png, _ := os.ReadFile(filepath.Join(runDir, f.Screenshots[0])); !strings.HasPrefix(string(png), "\x89PNG") {
		t.Error("screenshot is not the PNG adb gave")
	}

	log, _ := os.ReadFile(calls)
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		switch {
		case strings.HasPrefix(l, "adb "):
			if !strings.HasPrefix(l, "adb -s emulator-5580 ") {
				t.Errorf("adb call without the harness serial: %s", l)
			}
		case strings.HasPrefix(l, "maestro "):
			if !strings.HasPrefix(l, "maestro --device emulator-5580 test ") || !strings.Contains(l, "-e EMAIL=a@b.c -e PASSWORD=pw") {
				t.Errorf("maestro call: %s", l)
			}
			if strings.Contains(l, "biometric") {
				t.Errorf("a skipped flow was run: %s", l)
			}
		}
	}
	if n := strings.Count(string(log), "logcat -c"); n != 3 {
		t.Errorf("logcat cleared %d times, want once per flow run (3)", n)
	}
}

func TestRunTimeout(t *testing.T) {
	maestro, adb, _ := fakes(t)
	d := filepath.Join(t.TempDir(), ".maestro")
	write(t, filepath.Join(d, "slow.yaml"), "appId: a\n---\n- launchApp\n")
	c := &config{flowsDir: d, rel: ".maestro", out: filepath.Join(t.TempDir(), "mobile"), maestro: maestro, adb: adb,
		serial: "emulator-5580", flowTimeout: 200 * time.Millisecond, adbTimeout: 10 * time.Second}
	flows, _ := Discover(d, ".maestro", nil)
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "failed" || res.Tests[0].Status != "timedOut" {
		t.Fatalf("got %+v", res)
	}
}

func TestRunRefusesTheUsersEmulatorAndEmptyPlans(t *testing.T) {
	c := &config{serial: userSerial, out: t.TempDir()}
	if _, err := run(context.Background(), c, []Flow{{Slug: "x"}}); err == nil {
		t.Fatal("ran against emulator-5554")
	}
	c.serial = "emulator-5580"
	if _, err := run(context.Background(), c, nil); err != errNoFlows {
		t.Fatalf("no flows: got %v", err)
	}
}

func TestParseConfig(t *testing.T) {
	if _, _, err := parseConfig("run", []string{"--flows", "d"}); err == nil {
		t.Error("run without --out/--serial")
	}
	if _, _, err := parseConfig("list", []string{"--flows", "d", "-e", "NOEQUALS"}); err == nil {
		t.Error("-e without =")
	}
	c, re, err := parseConfig("run", []string{"--flows", "d", "--out", "o", "--serial", "emulator-5580", "-e", "A=1", "-e", "B=x=y", "--filter", "chat",
		"--reverse", "tcp:7700=tcp:18700", "--reverse", "tcp:1=tcp:1", "--health-port", "7700"})
	if err != nil || re == nil || strings.Join(c.env, " ") != "A=1 B=x=y" ||
		strings.Join(c.reverse, " ") != "tcp:7700=tcp:18700 tcp:1=tcp:1" || c.healthPort != "7700" {
		t.Fatalf("%+v %v %v", c, re, err)
	}
	if _, _, err := parseConfig("run", []string{"--flows", "d", "--reverse", "7700"}); err == nil {
		t.Error("--reverse without =")
	}
}

// prepCalls are the adb calls before each maestro run, one string per run.
func prepCalls(t *testing.T, calls string) []string {
	t.Helper()
	log, _ := os.ReadFile(calls)
	var out []string
	var cur []string
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if strings.HasPrefix(l, "maestro ") {
			out = append(out, strings.Join(cur, "\n"))
			cur = nil
			continue
		}
		if !strings.HasPrefix(l, "adb -s emulator-5580 ") {
			t.Errorf("adb call without the harness serial: %s", l)
		}
		// `adb -s S reconnect` kicks that one transport; `reconnect offline` (or `device`) is host-wide.
		if strings.Contains(l, "kill-server") || strings.Contains(l, "remove-all") ||
			(strings.Contains(l, "reconnect") && l != "adb -s emulator-5580 reconnect") {
			t.Errorf("a global adb call: %s", l)
		}
		cur = append(cur, l)
	}
	return out
}

func dropConfig(t *testing.T, flows map[string]string) (*config, []Flow, string) {
	maestro, adb, calls := fakes(t)
	d := filepath.Join(t.TempDir(), ".maestro")
	for name, title := range flows {
		write(t, filepath.Join(d, name), "appId: a\nname: "+title+"\n---\n- launchApp\n")
	}
	c := &config{flowsDir: d, rel: ".maestro", out: filepath.Join(t.TempDir(), "mobile"), maestro: maestro, adb: adb,
		serial: "emulator-5580", flowTimeout: time.Minute, adbTimeout: 10 * time.Second, probePause: time.Millisecond,
		retryPause: time.Millisecond, pollPause: time.Millisecond, recoverTimeout: 5 * time.Second,
		reverse: []string{"tcp:18710=tcp:18710", "tcp:7700=tcp:18700"}, healthPort: "7700"}
	fl, err := Discover(d, ".maestro", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c, fl, calls
}

// An adb transport drop wipes every reverse tunnel: before each flow the runner waits for the
// device, sets the tunnels again and probes /health through them.
func TestRunReadiesTheDeviceBeforeEveryFlow(t *testing.T) {
	c, flows, calls := dropConfig(t, map[string]string{"a.yaml": "a", "b.yaml": "b"})
	res, err := run(context.Background(), c, flows)
	if err != nil || res.Status != "passed" {
		t.Fatalf("%+v %v", res, err)
	}
	runs := prepCalls(t, calls)
	if len(runs) != 2 {
		t.Fatalf("%d maestro runs, want 2", len(runs))
	}
	for i, pre := range runs {
		wait := strings.Index(pre, "adb -s emulator-5580 wait-for-device")
		r1 := strings.Index(pre, "adb -s emulator-5580 reverse tcp:18710 tcp:18710")
		r2 := strings.Index(pre, "adb -s emulator-5580 reverse tcp:7700 tcp:18700")
		probe := strings.Index(pre, "| nc 127.0.0.1 7700")
		if wait < 0 || r1 < wait || r2 < r1 || probe < r2 {
			t.Errorf("flow %d: want wait-for-device, the two tunnels, then the probe, got:\n%s", i+1, pre)
		}
	}
}

// A drop leaves the transport offline for a moment and Maestro's driver dead: before the retry
// the runner kicks this one transport, waits until the device is steadily up, stops the driver
// and readies the device again.
func TestRunRecoversTheDeviceAndRetriesWhenItDrops(t *testing.T) {
	c, flows, calls := dropConfig(t, map[string]string{"drop.yaml": "chat after a drop", "z-fail.yaml": "plain failure"})
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tests) != 2 {
		t.Fatalf("%+v", res)
	}
	d := res.Tests[0]
	if d.Status != "passed" || !strings.HasPrefix(d.Note, "retried 1×") || !strings.Contains(d.Note, "DeviceServerDied") {
		t.Errorf("dropped flow: %+v", d)
	}
	f := res.Tests[1]
	if f.Status != "failed" || f.Note != "" {
		t.Errorf("an ordinary failure is not retried: %+v", f)
	}
	if res.DeviceRetries != 1 {
		t.Errorf("device_retries = %d, want 1", res.DeviceRetries)
	}
	runs := prepCalls(t, calls)
	if len(runs) != 3 {
		t.Fatalf("%d maestro runs, want 3 (the dropped flow twice, the failing one once)", len(runs))
	}
	retry := runs[1]
	kick := strings.Index(retry, "adb -s emulator-5580 reconnect\n")
	stop1 := strings.Index(retry, "adb -s emulator-5580 shell am force-stop dev.mobile.maestro.test")
	stop2 := strings.Index(retry, "adb -s emulator-5580 shell am force-stop dev.mobile.maestro\n")
	tunnel := strings.LastIndex(retry, "reverse tcp:7700 tcp:18700")
	if kick < 0 || stop1 < kick || stop2 < kick || tunnel < stop2 {
		t.Errorf("want reconnect, then the driver stopped, then the tunnels, got:\n%s", retry)
	}
	// Two offline answers, then two good checks in a row before the driver is stopped.
	if n := strings.Count(retry[:stop1], "get-state"); n != 4 {
		t.Errorf("%d get-state calls before the retry, want 4 (2 offline, 2 up):\n%s", n, retry)
	}
	if strings.Contains(runs[0], "reconnect") || strings.Contains(runs[2], "reconnect") {
		t.Error("kicked a transport that had not dropped")
	}
	if _, err := os.Stat(filepath.Join(c.out, "drop", "maestro.log")); err != nil {
		t.Error("the retry left no maestro.log")
	}
	if log, _ := os.ReadFile(filepath.Join(c.out, "drop.try1", "maestro.log")); !strings.Contains(string(log), "DeviceServerDied") {
		t.Errorf("the first try's maestro.log was not kept: %q", log)
	}
	raw, _ := os.ReadFile(filepath.Join(c.out, "results.json"))
	if !strings.Contains(string(raw), `"note": "retried 1×`) || !strings.Contains(string(raw), `"device_retries": 1`) {
		t.Errorf("results.json lacks the note or the count:\n%s", raw)
	}
}

// Drops came about every 20 s on a bad run: one retry is not enough.
func TestRunRetriesUpToThreeTries(t *testing.T) {
	c, flows, calls := dropConfig(t, map[string]string{"droptwice.yaml": "two drops", "dropall.yaml": "always drops"})
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]Record{}
	for _, r := range res.Tests {
		byTitle[r.Title] = r
	}
	if r := byTitle["two drops"]; r.Status != "passed" || !strings.HasPrefix(r.Note, "retried 2×") ||
		!strings.Contains(r.Note, "on try 1") || !strings.Contains(r.Note, "on try 2") {
		t.Errorf("two drops: %+v", r)
	}
	if r := byTitle["always drops"]; r.Status != "failed" || !strings.HasPrefix(r.Note, "retried 2× (tries 3 of 3)") {
		t.Errorf("always drops: %+v", r)
	}
	if res.DeviceRetries != 4 {
		t.Errorf("device_retries = %d, want 4", res.DeviceRetries)
	}
	if runs := prepCalls(t, calls); len(runs) != 6 {
		t.Errorf("%d maestro runs, want 3 per flow", len(runs))
	}
}

// A device that never comes back: the retry is still played (and fails on its own), the run
// does not hang.
func TestRecoverDeviceGivesUp(t *testing.T) {
	c, _, calls := dropConfig(t, map[string]string{"a.yaml": "a"})
	c.recoverTimeout = 50 * time.Millisecond
	write(t, filepath.Join(filepath.Dir(calls), "offline"), "1000000\n")
	err := c.recoverDevice(context.Background())
	if err == nil || !strings.Contains(err.Error(), "device offline") {
		t.Fatalf("err = %v", err)
	}
	log, _ := os.ReadFile(calls)
	if strings.Contains(string(log), "force-stop") {
		t.Error("stopped the driver on a device that is not back")
	}
	if n := strings.Count(string(log), "adb -s emulator-5580 reconnect\n"); n < 1 {
		t.Error("never kicked the transport")
	}
}

func TestRunFailsAFlowWhenTheSandboxIsUnreachable(t *testing.T) {
	c, flows, calls := dropConfig(t, map[string]string{"a.yaml": "a"})
	c.healthPort = "7799" // the fake answers nothing there
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Tests[0]
	if r.Status != "failed" || r.Error == nil || !strings.Contains(*r.Error, "can't reach the sandbox") || !strings.Contains(r.Note, "retried 2×") {
		t.Errorf("%+v", r)
	}
	if !strings.Contains(r.Note, "the emulator was not ready on try 1") || strings.Contains(r.Note, "transport dropped") {
		t.Errorf("note %q: a failed readiness check is not a transport drop", r.Note)
	}
	log, _ := os.ReadFile(calls)
	if strings.Contains(string(log), "maestro ") {
		t.Error("ran the flow without a tunnel to the sandbox")
	}
	if n := strings.Count(string(log), "nc 127.0.0.1 7799"); n != 9 {
		t.Errorf("%d probes, want 3 per try, 3 tries", n)
	}
}

func TestHostLoad(t *testing.T) {
	if _, err := os.Stat("/proc/loadavg"); err != nil {
		t.Skip("no /proc")
	}
	if h := hostLoad(); !regexp.MustCompile(`^, host load [0-9.]+, [0-9.]+ GB available$`).MatchString(h) {
		t.Errorf("hostLoad() = %q", h)
	}
}
