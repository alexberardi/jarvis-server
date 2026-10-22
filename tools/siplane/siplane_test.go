package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net"
)

// The lane's PBX must keep the FreePBX 17 extension defaults jarvisd has to cope with
// (docs/cc/phone-sip.md §3); losing one would let a later SIP change pass a lane that no
// longer looks like the user's PBX.
func TestLaneCopiesFreePBXDefaults(t *testing.T) {
	conf, err := parsePJSIPConf(filepath.Join("..", "..", "test", "sip", "asterisk", "pjsip.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"100", "101"} {
		for k, want := range freePBXEndpoint {
			if got := conf[ext+"/endpoint"][k]; got != want {
				t.Errorf("endpoint %s: %s = %q, want %q (FreePBX 17's default)", ext, k, got, want)
			}
		}
		for k, want := range freePBXAOR {
			if got := conf[ext+"/aor"][k]; got != want {
				t.Errorf("aor %s: %s = %q, want %q (FreePBX 17's default)", ext, k, got, want)
			}
		}
		if conf[ext+"-auth/auth"]["username"] != ext {
			t.Errorf("extension %s has no auth section %s-auth", ext, ext)
		}
	}
	if conf["carrier/endpoint"]["context"] != "from-pstn" {
		t.Errorf("the carrier's trunk must land in from-pstn (the inbound routes), got %q", conf["carrier/endpoint"]["context"])
	}
}

// The defaults the test above checks are the ones the ask and §3 name.
func TestFreePBXDefaultsListed(t *testing.T) {
	for k, v := range map[string]string{"direct_media": "yes", "timers": "yes", "rtp_timeout": "30", "send_pai": "yes"} {
		if freePBXEndpoint[k] != v {
			t.Errorf("freePBXEndpoint[%s] = %q, want %q", k, freePBXEndpoint[k], v)
		}
	}
	for k, v := range map[string]string{"max_contacts": "1", "remove_existing": "no", "qualify_frequency": "60"} {
		if freePBXAOR[k] != v {
			t.Errorf("freePBXAOR[%s] = %q, want %q", k, freePBXAOR[k], v)
		}
	}
}

func TestParsePJSIPConf(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pjsip.conf")
	os.WriteFile(p, []byte("; comment\n[100]\ntype=endpoint\nallow=ulaw ; trailing\n\n[100]\ntype=aor\nmax_contacts = 1\n#include x.conf\n"), 0o644)
	conf, err := parsePJSIPConf(p)
	if err != nil {
		t.Fatal(err)
	}
	if conf["100/endpoint"]["allow"] != "ulaw" || conf["100/aor"]["max_contacts"] != "1" {
		t.Fatalf("got %v", conf)
	}
}

// `pjsip show endpoint 100` on the running lane, as the registered scenario reads it.
func TestShownSettings(t *testing.T) {
	out := ` Endpoint:  100/100                                              Not in use    0 of inf
 direct_media                       : true
 rtp_timeout                        : 30
 send_pai                           : true
 timers                             : yes
 max_contacts                       : 1
 remove_existing                    : false
 qualify_frequency                  : 60
`
	got := shownSettings(out)
	if got["direct_media"] != "true" || got["qualify_frequency"] != "60" || got["remove_existing"] != "false" {
		t.Fatalf("got %v", got)
	}
	if bad := missingDefaults(got); len(bad) != 0 {
		t.Errorf("missing: %v", bad)
	}
	delete(got, "timers")
	got["direct_media"] = "false"
	if bad := missingDefaults(got); len(bad) != 2 {
		t.Errorf("missing: %v, want direct_media and timers", bad)
	}
}

func TestWordErrorRateWithin(t *testing.T) {
	for _, c := range []struct {
		said, heard string
		want        float64
	}{
		{"the quick brown fox", "the quick brown fox", 0},
		// The far end hears the PBX's prompt first: those words are not errors.
		{"the quick brown fox", "This is the echo test. The quick brown fox. Goodbye.", 0},
		{"the quick brown fox", "echo test the quick round fox", 0.25},
		{"the quick brown fox", "", 1},
		{"", "anything", 0},
	} {
		if got := wordErrorRateWithin(c.said, c.heard); got != c.want {
			t.Errorf("wordErrorRateWithin(%q, %q) = %v, want %v", c.said, c.heard, got, c.want)
		}
	}
}

func TestBestWER(t *testing.T) {
	alts := []string{"your extension number is one zero one", "your extension number is 101"}
	if w, i := bestWER(alts, "Your extension number is 101."); w != 0 || i != 1 {
		t.Fatalf("got %v, %d", w, i)
	}
}

// fakeBaresip speaks ctrl_tcp's netstring JSON: it answers each command, then sends the
// events the test queued.
func fakeBaresip(t *testing.T, events ...string) (addr string, got chan map[string]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan map[string]string, 4)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		for {
			b, err := readNetstring(r)
			if err != nil {
				return
			}
			var cmd map[string]string
			json.Unmarshal(b, &cmd)
			got <- cmd
			resp, _ := json.Marshal(map[string]any{"response": true, "ok": true, "data": "call id: x1\n", "token": cmd["token"]})
			writeNetstring(c, resp)
			for _, e := range events {
				writeNetstring(c, []byte(e))
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestBaresipCtrl(t *testing.T) {
	addr, got := fakeBaresip(t,
		`{"event":true,"type":"CALL_PROGRESS","class":"call","direction":"outgoing","peeruri":"sip:*43@pbx","id":"x1","param":"183"}`,
		`{"event":true,"type":"CALL_ESTABLISHED","class":"call","direction":"outgoing","peeruri":"sip:*43@pbx","id":"x1"}`,
		`{"event":true,"type":"CALL_CLOSED","class":"call","direction":"outgoing","peeruri":"sip:*43@pbx","id":"x1","param":"486 Busy Here"}`,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialCtrl(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Command(ctx, "dial", "sip:*43@pbx"); err != nil {
		t.Fatal(err)
	}
	if cmd := <-got; cmd["command"] != "dial" || cmd["params"] != "sip:*43@pbx" {
		t.Fatalf("baresip got %v", cmd)
	}
	e, err := c.Wait(ctx, "CALL_CLOSED")
	if err != nil {
		t.Fatal(err)
	}
	if e.Param != "486 Busy Here" {
		t.Errorf("closed with %q", e.Param)
	}
	if !c.Saw("CALL_ESTABLISHED") || !c.Saw("CALL_PROGRESS") || c.Saw("CALL_INCOMING") {
		t.Errorf("seen: %v", c.Seen())
	}
	if c.Calls("outgoing") != 1 || c.Calls("incoming") != 0 {
		t.Errorf("calls: %d out, %d in", c.Calls("outgoing"), c.Calls("incoming"))
	}
	short, cancel2 := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel2()
	if _, err := c.Wait(short, "CALL_INCOMING"); err == nil || !strings.Contains(err.Error(), "CALL_INCOMING") {
		t.Errorf("waiting for an event that never comes: %v", err)
	}
}

func TestBaresipCtrlRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		r := bufio.NewReader(c)
		readNetstring(r)
		writeNetstring(c, []byte(`{"response":true,"ok":false,"data":"no such command","token":"1"}`))
	}()
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := dialCtrl(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Command(ctx, "nope", ""); err == nil || !strings.Contains(err.Error(), "no such command") {
		t.Fatalf("got %v", err)
	}
}

func TestNetstring(t *testing.T) {
	var b strings.Builder
	writeNetstring(&b, []byte(`{"a":1}`))
	if b.String() != `7:{"a":1},` {
		t.Fatalf("wrote %q", b.String())
	}
	r := bufio.NewReader(strings.NewReader(`7:{"a":1},2:{},`))
	for _, want := range []string{`{"a":1}`, `{}`} {
		got, err := readNetstring(r)
		if err != nil || string(got) != want {
			t.Fatalf("read %q, %v; want %q", got, err, want)
		}
	}
	if _, err := readNetstring(bufio.NewReader(strings.NewReader(`3:abcd`))); err == nil {
		t.Error("a netstring without its comma reads")
	}
}

func TestReadLaneEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lane.env")
	os.WriteFile(p, []byte("# comment\nSIP_LANE_ASTERISK=jarvis-sip-x-asterisk\nSIP_LANE_SIP=127.0.0.1:15060\nSIP_LANE_ALICE_CTRL=127.0.0.1:15171\n"), 0o644)
	env, err := readLaneEnv(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["SIP_LANE_SIP"] != "127.0.0.1:15060" || env["SIP_LANE_ALICE_CTRL"] != "127.0.0.1:15171" {
		t.Fatalf("got %v", env)
	}
	if _, err := readLaneEnv(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "--sip") {
		t.Errorf("a missing lane says how to start it: %v", err)
	}
}

func TestScenarios(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range scenarios {
		if seen[s.Name] {
			t.Errorf("scenario %s twice", s.Name)
		}
		seen[s.Name] = true
		if s.Run == nil || s.What == "" {
			t.Errorf("scenario %s: no Run or What", s.Name)
		}
	}
	// The FreePBX behaviours the lane copies (docs/cc/phone-sip.md §5 L1) each have one.
	for _, n := range []string{"registered", "echo", "speak-extension", "internal-call", "inbound-route", "outbound-route",
		"ring-group", "call-waiting", "busy-no-call-waiting", "trunk-busy", "no-answer", "all-trunks-busy",
		"contact-taken", "wrong-password-ban"} {
		if !seen[n] {
			t.Errorf("no scenario %s", n)
		}
	}
}

func TestSoxPadArgs(t *testing.T) {
	got := strings.Join(soxPad("/w/tts.wav", "/w/say.wav", 2, 10), " ")
	want := "/w/tts.wav -r 8000 -c 1 -b 16 /w/say.wav pad 2 10 trim 0 10"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSIPpTrace(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uac_1_messages.log"), []byte("----- 2026\nINVITE sip:2125550480@127.0.0.1:30060 SIP/2.0\nVia: x\n\n----- 2026\nSIP/2.0 180 Ringing\nVia: x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "uac_1_errors.log"), []byte("2026 Aborting call on unexpected message for Call-Id 'x': while expecting '183' (index 7), received 'SIP/2.0 200 OK'\n"), 0o644)
	if got := sippTrace(dir, "_messages.log"); got != "INVITE sip:2125550480@127.0.0.1:30060 SIP/2.0 | SIP/2.0 180 Ringing" {
		t.Errorf("messages: %q", got)
	}
	if got := sippTrace(dir, "_errors.log"); !strings.Contains(got, "while expecting '183'") {
		t.Errorf("errors: %q", got)
	}
	if got := sippTrace(t.TempDir(), "_errors.log"); got != "(nothing)" {
		t.Errorf("no trace: %q", got)
	}
}

// fakeDocker puts a docker on PATH that answers `inspect` from states (container → "running
// exit"), prints a log for `logs` and records every call in the returned file.
func fakeDocker(t *testing.T, states map[string]string) (calls string) {
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	var sh strings.Builder
	sh.WriteString("#!/bin/sh\necho \"$*\" >>" + calls + "\ncase $1 in\n inspect)\n  for c; do :; done\n  case $c in\n")
	for c, st := range states {
		sh.WriteString("   " + c + ") echo '" + st + "' ;;\n")
	}
	sh.WriteString("   *) echo 'No such object' >&2; exit 1 ;;\n  esac ;;\n logs) echo 'auframe: init: unsupported sample format 24128 (???)' >&2 ;;\nesac\n")
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(sh.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// A far end that exited (baresip 1.1.0 segfaulting) is named with its exit code, and its log
// is kept, so the scenario it died in says so instead of every later one failing on docker exec.
func TestDeadFarEnds(t *testing.T) {
	fakeDocker(t, map[string]string{"lane-jarvis": "false 139", "lane-alice": "true 0", "lane-carrier": "true 0"})
	out := t.TempDir()
	l := &lane{out: out, env: map[string]string{
		"SIP_LANE_JARVIS_CONTAINER": "lane-jarvis", "SIP_LANE_ALICE_CONTAINER": "lane-alice", "SIP_LANE_CARRIER_CONTAINER": "lane-carrier"}}
	dead, err := l.deadFarEnds(context.Background(), "outbound-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || !strings.Contains(dead[0], "jarvis") || !strings.Contains(dead[0], "139") || !strings.Contains(dead[0], "segmentation fault") {
		t.Fatalf("dead = %q, want jarvis exited 139 (segmentation fault)", dead)
	}
	b, err := os.ReadFile(filepath.Join(out, "outbound-route-jarvis-baresip.log"))
	if err != nil || !strings.Contains(string(b), "unsupported sample format") {
		t.Errorf("the dead far end's log is kept: %q, %v", b, err)
	}

	fakeDocker(t, map[string]string{"lane-jarvis": "true 0", "lane-alice": "true 0", "lane-carrier": "true 0"})
	if dead, err := l.deadFarEnds(context.Background(), "echo"); err != nil || len(dead) != 0 {
		t.Errorf("all running: dead = %q, %v", dead, err)
	}
	fakeDocker(t, map[string]string{})
	if _, err := l.deadFarEnds(context.Background(), "echo"); err == nil {
		t.Error("a missing container is a lane error")
	}
}

// The far ends never play ringback: baresip 1.1.0 plays it on the call's own player and
// segfaults tearing it down on answer when the machine is loaded (CI's outbound-route).
func TestFarEndsHaveNoRingback(t *testing.T) {
	b, err := os.ReadFile("../../scripts/sip-lane")
	if err != nil {
		t.Fatal(err)
	}
	conf := string(b)
	i, j := strings.Index(conf, `cat >"$d/config" <<EOF`), strings.Index(conf, "\nEOF\n")
	if i < 0 || j < i || !strings.Contains(conf[i:j], "\nringback_disabled yes\n") {
		t.Error("scripts/sip-lane's baresip config lacks ringback_disabled yes")
	}
}
