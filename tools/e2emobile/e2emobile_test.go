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
// any flow whose path has "fail" in it (and writes a JUnit report saying why), and sleeps on
// "slow".
func fakes(t *testing.T) (maestro, adb, calls string) {
	d := t.TempDir()
	calls = filepath.Join(d, "calls.log")
	maestro, adb = filepath.Join(d, "maestro"), filepath.Join(d, "adb")
	write(t, maestro, `#!/bin/sh
echo "maestro $*" >>`+calls+`
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
echo "adb $*" >>`+calls+`
case "$*" in
  *"screencap -p"*) printf '\211PNG\r\n\032\nfake' ;;
  *"exec-out cat"*) echo '<hierarchy><node text="Chat"/></hierarchy>' ;;
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
	c, re, err := parseConfig("run", []string{"--flows", "d", "--out", "o", "--serial", "emulator-5580", "-e", "A=1", "-e", "B=x=y", "--filter", "chat"})
	if err != nil || re == nil || strings.Join(c.env, " ") != "A=1 B=x=y" {
		t.Fatalf("%+v %v %v", c, re, err)
	}
}
