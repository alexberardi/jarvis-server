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
// "slow", and on "drop" dies the way an adb transport drop kills it, the first time only. The
// adb's /health probe answers 200 on the emulator's port 7700 only.
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
    *) shift ;;
  esac
done
case $flow in
  *slow*) sleep 3 ;;
  *drop*)
    if [ ! -f `+d+`/dropped ]; then
      touch `+d+`/dropped
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
		if strings.Contains(l, "kill-server") || strings.Contains(l, "remove-all") || strings.Contains(l, "reconnect") {
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

func TestRunRetriesAFlowOnceWhenTheDeviceDrops(t *testing.T) {
	c, flows, calls := dropConfig(t, map[string]string{"drop.yaml": "chat after a drop", "z-fail.yaml": "plain failure"})
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tests) != 2 {
		t.Fatalf("%+v", res)
	}
	d := res.Tests[0]
	if d.Status != "passed" || !strings.Contains(d.Note, "retried once") || !strings.Contains(d.Note, "DeviceServerDied") {
		t.Errorf("dropped flow: %+v", d)
	}
	f := res.Tests[1]
	if f.Status != "failed" || f.Note != "" {
		t.Errorf("an ordinary failure is not retried: %+v", f)
	}
	runs := prepCalls(t, calls)
	if len(runs) != 3 {
		t.Fatalf("%d maestro runs, want 3 (the dropped flow twice, the failing one once)", len(runs))
	}
	if !strings.Contains(runs[1], "reverse tcp:7700 tcp:18700") {
		t.Errorf("the retry did not set the tunnels again:\n%s", runs[1])
	}
	raw, _ := os.ReadFile(filepath.Join(c.out, "results.json"))
	if !strings.Contains(string(raw), `"note": "retried once`) {
		t.Errorf("results.json lacks the note:\n%s", raw)
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
	if r.Status != "failed" || r.Error == nil || !strings.Contains(*r.Error, "can't reach the sandbox") || !strings.Contains(r.Note, "retried once") {
		t.Errorf("%+v", r)
	}
	if !strings.HasPrefix(r.Note, "retried once: the emulator was not ready") || strings.Contains(r.Note, "transport dropped") {
		t.Errorf("note %q: a failed readiness check is not a transport drop", r.Note)
	}
	log, _ := os.ReadFile(calls)
	if strings.Contains(string(log), "maestro ") {
		t.Error("ran the flow without a tunnel to the sandbox")
	}
	if n := strings.Count(string(log), "nc 127.0.0.1 7799"); n != 6 {
		t.Errorf("%d probes, want 3 per try, 2 tries", n)
	}
}
