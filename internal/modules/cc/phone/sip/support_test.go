package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo/sip"
)

// The support surface (docs/cc/phone-sip.md §6): OPTIONS round-trip time and active calls in
// the status, the connection check, the self-test feature codes and the inbound watch.

func TestStatusRecordsTheOptionsRoundTrip(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "secret"))
	waitState(t, u, StateRegistered, 3*time.Second)
	eventually(t, 2*time.Second, "an OPTIONS round trip", func() bool { return !u.Status().LastOptions.IsZero() })
	st := u.Status()
	if st.OptionsRTT <= 0 || st.OptionsRTT > time.Second {
		t.Fatalf("OptionsRTT %v", st.OptionsRTT)
	}
	if st.RegisterRTT <= 0 || st.LastRegister.IsZero() {
		t.Fatalf("register RTT %v at %v", st.RegisterRTT, st.LastRegister)
	}
}

func TestCheckReRegistersAndPings(t *testing.T) {
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	cfg.KeepAlive = time.Hour // only the check's own OPTIONS
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)
	before := len(p.authorized())
	res := u.Check(context.Background())
	if !res.Registered || res.Error != "" {
		t.Fatalf("check: %+v", res)
	}
	if res.OptionsRTT <= 0 || res.RegisterRTT <= 0 {
		t.Fatalf("RTTs: %+v", res)
	}
	if n := len(p.authorized()); n != before+1 {
		t.Fatalf("%d REGISTERs with credentials after the check, want %d", n, before+1)
	}
	if p.optionsSeen() != 1 {
		t.Fatalf("%d OPTIONS, want the check's one", p.optionsSeen())
	}
}

// A stopped registration is not retried by a check: one more refusal only feeds the firewall.
func TestCheckDoesNotRetryAStoppedRegistration(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "wrong"))
	waitState(t, u, StateStopped, 3*time.Second)
	n := len(p.registers())
	res := u.Check(context.Background())
	if res.Registered || !strings.Contains(res.Hint, "Firewall") {
		t.Fatalf("check: %+v", res)
	}
	time.Sleep(100 * time.Millisecond)
	if len(p.registers()) != n {
		t.Fatalf("the check sent a REGISTER after the stop")
	}
}

func TestDialFeatureAllowsOnlyTheSelfTestCodes(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "secret"))
	waitState(t, u, StateRegistered, 3*time.Second)
	for _, bad := range []string{"911", "*69", "*72", "#", "*43#", "*430", "2125550100", ""} {
		if _, err := u.DialFeature(context.Background(), bad); err == nil {
			t.Errorf("DialFeature(%q) was allowed", bad)
		}
	}
	// Dial itself takes digits only: a feature code cannot go through the normal path.
	for _, bad := range []string{"*43", "*65", "**", "##", "9*1"} {
		if _, err := u.Dial(context.Background(), bad); err == nil {
			t.Errorf("Dial(%q) was allowed", bad)
		}
	}
	got := make(chan string, 1)
	p.setInvite(func(d *diago.DialogServerSession) {
		got <- d.InviteRequest.Recipient.User
		d.Respond(sip.StatusBusyHere, "Busy Here", nil)
	})
	c, err := u.DialFeature(context.Background(), "*43")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case user := <-got:
		if user != "*43" {
			t.Fatalf("INVITE to %q", user)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no INVITE")
	}
}

func TestStatusCountsActiveCalls(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "secret"))
	waitState(t, u, StateRegistered, 3*time.Second)
	// The far end has to keep ringing while the count is read: diago hangs a dialog up as soon
	// as its INVITE handler returns, so a handler that only rings and returns ends the call —
	// and drops it from the count — on a machine slow enough to get there first.
	p.setInvite(func(d *diago.DialogServerSession) {
		d.Ringing()
		<-d.Context().Done()
	})
	c, err := u.Dial(context.Background(), "2125550100")
	if err != nil {
		t.Fatal(err)
	}
	if n := u.Status().ActiveCalls; n != 1 {
		t.Fatalf("ActiveCalls %d while ringing", n)
	}
	c.Hangup(context.Background())
	<-c.Done()
	eventually(t, 2*time.Second, "the call to go", func() bool { return u.Status().ActiveCalls == 0 })
}

// An inbound INVITE (refused 480 until inbound calls are built) is recorded for the inbound
// watch: caller ID from P-Asserted-Identity before From, and what the SDP offers.
func TestInboundInviteIsRecorded(t *testing.T) {
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)

	to := sip.Uri{Scheme: "sip", User: "100", Host: "127.0.0.1", Port: cfg.LocalPort}
	req := sip.NewRequest(sip.INVITE, to)
	req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "pbx", Host: p.host, Port: p.port}})
	req.AppendHeader(sip.NewHeader("P-Asserted-Identity", `"Alice Caller" <sip:+12125550199@pbx>`))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody([]byte("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.9\r\nt=0 0\r\n" +
		"m=audio 20010 RTP/AVP 0 8 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:8 PCMA/8000\r\na=rtpmap:101 telephone-event/8000\r\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := p.client.Do(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != sip.StatusTemporarilyUnavailable {
		t.Fatalf("answered %d, want 480 (inbound calls are not answered yet)", res.StatusCode)
	}
	in := u.Status().LastInbound
	if in.At.IsZero() || in.Caller != "+12125550199" || in.CallerName != "Alice Caller" {
		t.Fatalf("recorded %+v", in)
	}
	if in.Media != "127.0.0.9:20010" || strings.Join(in.Codecs, ",") != "PCMU,PCMA,telephone-event" {
		t.Fatalf("media %q codecs %v", in.Media, in.Codecs)
	}
}

// A REGISTER nobody answered has no round trip to show.
func TestATimedOutRegisterHasNoRoundTrip(t *testing.T) {
	p := newPBX(t, "secret")
	p.setRegMode("drop")
	u := startUA(t, p.config(t, "secret"))
	eventually(t, 3*time.Second, "a failed attempt", func() bool { return !u.Status().LastRegister.IsZero() })
	if st := u.Status(); st.RegisterRTT != 0 || st.LastError == "" {
		t.Fatalf("status %+v", st)
	}
}
