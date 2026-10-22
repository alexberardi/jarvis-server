package sip

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The SIP trace (docs/cc/phone-sip.md §6): what goes in and what never comes out.

const registerWithAuth = "REGISTER sip:pbx.lan SIP/2.0\r\n" +
	"Via: SIP/2.0/UDP 10.0.0.5:5062;branch=z9hG4bK1\r\n" +
	"From: <sip:100@pbx.lan>;tag=abc\r\n" +
	"To: <sip:100@pbx.lan>\r\n" +
	"Call-ID: reg-1@jarvis\r\n" +
	"CSeq: 2 REGISTER\r\n" +
	"authorization: Digest username=\"100\", realm=\"asterisk\", nonce=\"1729-nonce\", uri=\"sip:pbx.lan\", " +
	"response=\"6629fae49393a05397450978507c4ef1\", cnonce=\"c-n\", nc=00000001, qop=auth, algorithm=MD5\r\n" +
	"Proxy-Authorization: Digest username=\"100\", response=\"deadbeefdeadbeef\",\r\n" +
	" nonce=\"folded-nonce\"\r\n" +
	"X-Debug: the password is hunter2-secret\r\n" +
	"Content-Length: 0\r\n\r\n"

func TestRedactDropsDigestSecretsAndPasswords(t *testing.T) {
	out := redact([]byte(registerWithAuth), []string{"hunter2-secret"})
	for _, secret := range []string{"6629fae49393a05397450978507c4ef1", "deadbeefdeadbeef", "1729-nonce",
		"folded-nonce", "c-n", "hunter2-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q survived redaction:\n%s", secret, out)
		}
	}
	// What a support reader needs stays: the method, who registered, the realm, the scheme.
	for _, keep := range []string{"REGISTER sip:pbx.lan SIP/2.0", "Call-ID: reg-1@jarvis", `username="100"`,
		`realm="asterisk"`, "authorization: Digest", "algorithm=MD5", "Proxy-Authorization: Digest"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q is missing:\n%s", keep, out)
		}
	}
}

func TestRedactKeepsSDPAndDropsBinaryBodies(t *testing.T) {
	sdp := "v=0\r\no=- 1 1 IN IP4 10.0.0.9\r\nc=IN IP4 10.0.0.9\r\nm=audio 20000 RTP/AVP 0 101\r\n"
	msg := "SIP/2.0 200 OK\r\nCall-ID: c1\r\nCSeq: 1 INVITE\r\nContent-Type: application/sdp\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n\r\n", len(sdp)) + sdp
	if out := redact([]byte(msg), nil); !strings.Contains(out, "m=audio 20000 RTP/AVP 0 101") {
		t.Fatalf("the SDP was dropped:\n%s", out)
	}
	bin := "INFO sip:100@x SIP/2.0\r\nCall-ID: c1\r\nCSeq: 3 INFO\r\nContent-Type: application/octet-stream\r\n\r\n\x00\x01\x02secret"
	out := redact([]byte(bin), nil)
	if strings.Contains(out, "secret") || !strings.Contains(out, "omitted") {
		t.Fatalf("a binary body was kept:\n%q", out)
	}
}

// A short password (a user's "1234") is still redacted wherever it appears.
func TestRedactShortPasswords(t *testing.T) {
	out := redact([]byte("OPTIONS sip:x SIP/2.0\r\nX-Note: 4321\r\n\r\n"), []string{"4321", ""})
	if strings.Contains(out, "4321") {
		t.Fatalf("password kept: %s", out)
	}
}

func traceMsg(callID, method string) []byte {
	return []byte(fmt.Sprintf("%s sip:x SIP/2.0\r\nCall-ID: %s\r\nCSeq: 1 %s\r\n\r\n", method, callID, method))
}

func TestTraceKeepsTheLastNCallsApartFromRegistrations(t *testing.T) {
	tr := newTracer(3, 2, 4)
	acct := &traceAccount{label: "100@pbx", laddr: "10.0.0.5:5062", registrar: "10.0.0.1:5060"}
	tr.add(acct)
	for i := range 5 {
		tr.record(false, "UDP", "10.0.0.5:5062", "10.0.0.1:5060", traceMsg(fmt.Sprintf("call-%d", i), "INVITE"))
	}
	for i := range 4 {
		tr.record(false, "UDP", "10.0.0.5:5062", "10.0.0.1:5060", traceMsg(fmt.Sprintf("opt-%d", i), "OPTIONS"))
	}
	// A message on a socket no UA owns is not ours.
	tr.record(true, "UDP", "10.0.0.7:5060", "10.0.0.8:5060", traceMsg("stranger", "INVITE"))

	got := tr.snapshot()
	var calls, other []string
	for _, d := range got {
		if d.Kind == TraceCall {
			calls = append(calls, d.CallID)
		} else {
			other = append(other, d.CallID)
		}
		if d.Account != "100@pbx" {
			t.Errorf("%s: account %q", d.CallID, d.Account)
		}
	}
	if strings.Join(calls, ",") != "call-4,call-3,call-2" {
		t.Errorf("calls %v, want the last 3, newest first", calls)
	}
	if strings.Join(other, ",") != "opt-3,opt-2" {
		t.Errorf("registration traffic %v, want the last 2", other)
	}
}

func TestTraceCapsMessagesPerDialog(t *testing.T) {
	tr := newTracer(3, 2, 4)
	tr.add(&traceAccount{label: "a", laddr: "10.0.0.5:5062"})
	for range 6 {
		tr.record(true, "UDP", "10.0.0.5:5062", "10.0.0.1:5060", traceMsg("c", "INVITE"))
	}
	d := tr.snapshot()[0]
	if len(d.Messages) != 4 || d.Dropped != 2 {
		t.Fatalf("%d messages, %d dropped; want 4 and 2", len(d.Messages), d.Dropped)
	}
	if d.Messages[0].Dir != "received" {
		t.Fatalf("dir %q", d.Messages[0].Dir)
	}
}

// Over TCP the UA's outgoing connection has an ephemeral local port: it is matched by the
// registrar it talks to.
func TestTraceMatchesTCPByRegistrar(t *testing.T) {
	tr := newTracer(3, 2, 4)
	tr.add(&traceAccount{label: "a", laddr: "10.0.0.5:5062", registrar: "10.0.0.1:5060"})
	tr.record(false, "TCP", "10.0.0.5:41234", "10.0.0.1:5060", traceMsg("t", "REGISTER"))
	if got := tr.snapshot(); len(got) != 1 || got[0].Account != "a" {
		t.Fatalf("%+v", got)
	}
}

func TestTraceTextExport(t *testing.T) {
	tr := newTracer(3, 2, 4)
	tr.add(&traceAccount{label: "100@pbx", laddr: "10.0.0.5:5062", passwords: []string{"hunter2-secret"}})
	tr.record(false, "UDP", "10.0.0.5:5062", "10.0.0.1:5060", []byte(registerWithAuth))
	var b strings.Builder
	WriteTraceText(&b, tr.snapshot())
	out := b.String()
	if !strings.Contains(out, "REGISTER sip:pbx.lan") || !strings.Contains(out, "sent UDP 10.0.0.5:5062 -> 10.0.0.1:5060") {
		t.Fatalf("export:\n%s", out)
	}
	if strings.Contains(out, "hunter2-secret") || strings.Contains(out, "6629fae4") {
		t.Fatalf("a secret is in the export:\n%s", out)
	}
}

// End to end over the fake PBX: the real UA's REGISTER is in the process-wide trace, under its
// account, with no password and no digest response.
func TestUATrafficIsTracedRedacted(t *testing.T) {
	p := newPBX(t, "trace-secret-pw")
	cfg := p.config(t, "trace-secret-pw")
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)
	label := u.traceLabel()
	var mine []TraceDialog
	for _, d := range Trace() {
		if d.Account == label {
			mine = append(mine, d)
		}
	}
	text := ""
	for _, d := range mine {
		for _, m := range d.Messages {
			text += m.Text
		}
	}
	if !strings.Contains(text, "REGISTER sip:") || !strings.Contains(text, "Authorization: Digest") {
		t.Fatalf("no REGISTER with its credentials in the trace (%d dialogs):\n%s", len(mine), text)
	}
	if strings.Contains(text, "trace-secret-pw") || strings.Contains(text, "response=\"") && !strings.Contains(text, "response=\"<redacted>\"") {
		t.Fatalf("a secret is in the trace:\n%s", text)
	}
}
