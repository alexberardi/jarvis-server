package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const harnessUDID = "6F1B2C3D-0000-4000-8000-000000000E2E"

// fakeXcrun writes an xcrun whose simctl knows two devices: the harness's (harnessUDID, named
// jarvis-harness-e2e) and one of the user's. It logs every call to calls, takes screenshots,
// prints a device log with one JavaScript error, and copies pushed payloads to <calls>.push.
func fakeXcrun(t *testing.T, calls string) string {
	xcrun := filepath.Join(filepath.Dir(calls), "xcrun")
	write(t, xcrun, `#!/bin/sh
echo "xcrun $*" >>`+calls+`
[ "$1" = simctl ] || exit 2
shift
case $1 in
  list) cat <<'X'
{"devices": {"com.apple.CoreSimulator.SimRuntime.iOS-18-0": [
  {"udid": "`+harnessUDID+`", "name": "jarvis-harness-e2e", "state": "Booted"},
  {"udid": "11111111-2222-3333-4444-555555555555", "name": "iPhone 16 Pro", "state": "Booted"}]}}
X
  ;;
  io) printf '\211PNG\r\n\032\nfake' >"$4" ;;
  spawn) echo '2026-10-18 09:41:00.000 E  JarvisAutomation[123:456] [com.facebook.react.log:javascript] TypeError: x is undefined'
         echo '2026-10-18 09:41:00.001 Df JarvisAutomation[123:456] [com.facebook.react.log:javascript] fine' ;;
  push) cp "$4" `+calls+`.push ;;
esac
`)
	return xcrun
}

func iosConfig(t *testing.T, flows, out string) (*config, string) {
	maestro, _, calls := fakes(t)
	return &config{platform: "ios", flowsDir: flows, rel: ".maestro", out: out, maestro: maestro,
		xcrun: fakeXcrun(t, calls), udid: harnessUDID, deviceName: "jarvis-harness-e2e", process: "JarvisAutomation",
		bundleID: "com.jarvis.nodemobile", env: []string{"EMAIL=a@b.c"}, flowTimeout: time.Minute, adbTimeout: 10 * time.Second}, calls
}

// idleListener stands in for the push endpoint's socket in run's tests: it accepts nothing.
type idleListener struct{ done chan struct{} }

func (l idleListener) Accept() (net.Conn, error) { <-l.done; return nil, net.ErrClosed }
func (l idleListener) Close() error              { close(l.done); return nil }
func (l idleListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 7999} }

func noSocket(t *testing.T) {
	old := listenPush
	listenPush = func() (net.Listener, error) { return idleListener{make(chan struct{})}, nil }
	t.Cleanup(func() { listenPush = old })
}

func TestRunIOS(t *testing.T) {
	noSocket(t)
	runDir := t.TempDir()
	c, calls := iosConfig(t, flowsDir(t), filepath.Join(runDir, "ios"))
	flows, err := Discover(c.flowsDir, c.rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := run(context.Background(), c, flows)
	if err != nil {
		t.Fatal(err)
	}
	if res.Suite != "ios" || res.Status != "failed" || len(res.Tests) != 4 {
		t.Fatalf("suite %s, status %s, %d tests", res.Suite, res.Status, len(res.Tests))
	}
	raw, err := os.ReadFile(filepath.Join(c.out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file Results
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	by := map[string]Record{}
	for _, r := range file.Tests {
		by[r.ID] = r
		if r.Suite != "ios" {
			t.Errorf("%s: suite %q, want ios", r.ID, r.Suite)
		}
	}
	f := by["chat-fail"]
	if f.Status != "failed" || f.Error == nil || !strings.Contains(*f.Error, `"84" is visible`) {
		t.Fatalf("failing flow: %+v", f)
	}
	if strings.Join(f.Screenshots, ",") != "ios/chat-fail/screenshot.png" {
		t.Errorf("screenshots %v", f.Screenshots)
	}
	for _, a := range []string{"ios/chat-fail/maestro.log", "ios/chat-fail/hierarchy.json", "ios/chat-fail/device.log", "ios/chat-fail/maestro"} {
		if !contains(f.Attachments, a) {
			t.Errorf("attachments %v: no %s", f.Attachments, a)
		}
		if _, err := os.Stat(filepath.Join(runDir, a)); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	if len(f.ConsoleErrors) != 1 || !strings.Contains(f.ConsoleErrors[0], "TypeError") {
		t.Errorf("console errors %v: want the one error-level javascript line", f.ConsoleErrors)
	}
	if png, _ := os.ReadFile(filepath.Join(runDir, f.Screenshots[0])); !strings.HasPrefix(string(png), "\x89PNG") {
		t.Error("screenshot is not the PNG simctl gave")
	}
	if h, _ := os.ReadFile(filepath.Join(runDir, "ios/chat-fail/hierarchy.json")); !strings.Contains(string(h), "Chat") {
		t.Errorf("hierarchy.json: %q", h)
	}

	log, _ := os.ReadFile(calls)
	for _, l := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		switch {
		case strings.HasPrefix(l, "adb "):
			t.Errorf("adb on iOS: %s", l)
		case strings.HasPrefix(l, "xcrun simctl list"):
		case strings.HasPrefix(l, "xcrun "):
			if !strings.Contains(l, " "+harnessUDID+" ") && !strings.HasSuffix(l, " "+harnessUDID) {
				t.Errorf("simctl call without the harness UDID: %s", l)
			}
			if strings.Contains(l, "booted") {
				t.Errorf("simctl call on 'booted': %s", l)
			}
		case strings.HasPrefix(l, "maestro "):
			if !strings.HasPrefix(l, "maestro --device "+harnessUDID+" ") {
				t.Errorf("maestro call: %s", l)
			}
			if strings.Contains(l, " test ") && !strings.Contains(l, "-e EMAIL=a@b.c -e PUSH_URL=http://127.0.0.1:7999/push") {
				t.Errorf("maestro test without the env: %s", l)
			}
		}
	}
	if !strings.Contains(string(log), `--predicate process == "JarvisAutomation"`) {
		t.Errorf("device log not filtered to the app's process:\n%s", log)
	}
}

func TestRunIOSRefusesDevicesItDidNotCreate(t *testing.T) {
	noSocket(t)
	for _, udid := range []string{"", "booted", "11111111-2222-3333-4444-555555555555", "no-such-device"} {
		c, _ := iosConfig(t, flowsDir(t), t.TempDir())
		c.udid = udid
		if _, err := run(context.Background(), c, []Flow{{Slug: "x", Path: "x.yaml"}}); err == nil {
			t.Errorf("ran against %q", udid)
		}
	}
}

// A flow delivers a push through PUSH_URL: the runner hands the payload to simctl push for the
// harness simulator and the app's bundle id.
func TestPushEndpoint(t *testing.T) {
	c, calls := iosConfig(t, flowsDir(t), t.TempDir())
	if err := os.MkdirAll(c.out, 0o755); err != nil {
		t.Fatal(err)
	}
	h := c.pushHandler()
	payload := `{"aps": {"alert": {"title": "Jarvis", "body": "the dryer is done"}}, "type": "inbox"}`
	if code := post(h, payload); code != 200 {
		t.Fatalf("push: HTTP %d", code)
	}
	got, _ := os.ReadFile(calls + ".push")
	if string(got) != payload {
		t.Errorf("pushed %q, want the flow's payload", got)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "xcrun simctl push "+harnessUDID+" com.jarvis.nodemobile ") {
		t.Errorf("simctl push call:\n%s", log)
	}
	if left, _ := filepath.Glob(filepath.Join(c.out, ".push-*")); len(left) != 0 {
		t.Errorf("payload files left behind: %v", left)
	}
	if code := post(h, "not json"); code != 400 {
		t.Errorf("a payload that is not JSON: HTTP %d, want 400", code)
	}
}

func TestParseConfigIOS(t *testing.T) {
	if _, _, err := parseConfig("run", []string{"--platform", "ios", "--flows", "d", "--out", "o"}); err == nil {
		t.Error("ios run without --udid")
	}
	if _, _, err := parseConfig("run", []string{"--platform", "windows", "--flows", "d", "--out", "o", "--serial", "s"}); err == nil {
		t.Error("unknown platform")
	}
	c, _, err := parseConfig("run", []string{"--platform", "ios", "--flows", "d", "--out", "o", "--udid", harnessUDID, "--process", "JarvisAutomation"})
	if err != nil || c.udid != harnessUDID || c.deviceName != "jarvis-harness-e2e" || c.bundleID != "com.jarvis.nodemobile" || c.xcrun != "xcrun" {
		t.Fatalf("%+v %v", c, err)
	}
}

func post(h http.Handler, body string) int {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/push", strings.NewReader(body)))
	return w.Code
}
