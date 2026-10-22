package sip

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
)

func TestRegistersWithAStableContactAndKeepsAlive(t *testing.T) {
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)

	regs := p.authorized()
	if len(regs) != 1 {
		t.Fatalf("%d authorized REGISTERs, want 1", len(regs))
	}
	c := regs[0].Contact()
	if c == nil || c.Address.Host != "127.0.0.1" || c.Address.Port != cfg.LocalPort || c.Address.User != "100" {
		t.Fatalf("Contact %v, want sip:100@127.0.0.1:%d", c, cfg.LocalPort)
	}
	if e := regs[0].GetHeader("Expires"); e == nil || e.Value() != "300" {
		t.Fatalf("Expires %v, want 300", e)
	}
	if to := regs[0].To(); to == nil || to.Address.User != "100" {
		t.Fatalf("To %v, want the extension's address of record", to)
	}
	// OPTIONS to the registrar every KeepAlive.
	eventually(t, 2*time.Second, "OPTIONS keepalives", func() bool { return p.optionsSeen() >= 3 })

	// A clean shutdown unregisters (Expires: 0), so a restart's REGISTER is not refused by
	// max_contacts=1 + remove_existing=no.
	u.Close(context.Background())
	all := p.registers()
	last := all[len(all)-1]
	if e := last.GetHeader("Expires"); e == nil || e.Value() != "0" {
		t.Fatalf("last REGISTER's Expires %v, want 0", e)
	}
	if u.Status().State != StateClosed {
		t.Fatalf("state %s after Close", u.Status().State)
	}

	// Restarted on the same account: the same Contact.
	u2 := startUA(t, cfg)
	waitState(t, u2, StateRegistered, 3*time.Second)
	regs = p.authorized()
	c2 := regs[len(regs)-1].Contact()
	if c2.Address.Host != c.Address.Host || c2.Address.Port != c.Address.Port {
		t.Fatalf("Contact moved across a restart: %v → %v", c, c2)
	}
}

func TestRefreshesBeforeTheGrantedExpiry(t *testing.T) {
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	cfg.RegisterExpiry = 2 * time.Second // refresh at 1.5 s
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)
	eventually(t, 4*time.Second, "a refresh", func() bool { return len(p.authorized()) >= 2 })
	if st := u.Status(); st.State != StateRegistered {
		t.Fatalf("state %s after refresh", st.State)
	}
}

func TestWrongPasswordStopsAfterThreeAttempts(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "not-the-password"))
	st := waitState(t, u, StateStopped, 5*time.Second)
	if st.AuthFailures != 3 || st.Hint != HintBlocked {
		t.Fatalf("status %+v", st)
	}
	if !strings.Contains(st.Hint, "Firewall") {
		t.Fatalf("hint %q does not mention the PBX firewall", st.Hint)
	}
	// Exactly three answered challenges, and no fourth however long we wait: each one feeds
	// the PBX's intrusion detection.
	time.Sleep(300 * time.Millisecond)
	if n := len(p.authorized()); n != 3 {
		t.Fatalf("%d REGISTERs with credentials, want exactly 3", n)
	}
	before := len(p.registers())
	u.Close(context.Background())
	if n := len(p.registers()); n != before {
		t.Fatalf("Close sent %d more REGISTERs to a PBX that refuses us", n-before)
	}
	err := u.Ready()
	if err == nil {
		t.Fatal("Ready: nil for a closed UA")
	}
}

func TestReadyExplainsAStop(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "wrong"))
	waitState(t, u, StateStopped, 5*time.Second)
	err := u.Ready()
	if err == nil || !strings.Contains(err.Error(), "Firewall") {
		t.Fatalf("Ready: %v", err)
	}
	if _, err := u.Dial(context.Background(), "2125550100"); err == nil {
		t.Fatal("Dial on a stopped UA succeeded")
	}
}

func TestForbiddenAndThenSilenceCountAsBlocked(t *testing.T) {
	// The real FreePBX firewall drops a banned host's packets; the lane answers 403. Both
	// count once the PBX has refused us.
	p := newPBX(t, "secret")
	p.setRegMode("forbid", "drop")
	u := startUA(t, p.config(t, "secret"))
	st := waitState(t, u, StateStopped, 5*time.Second)
	if st.AuthFailures != 3 {
		t.Fatalf("status %+v", st)
	}
	if n := len(p.registers()); n != 3 {
		t.Fatalf("%d REGISTERs, want 3", n)
	}
}

func TestAPBXThatNeverAnswersIsRetriedNotStopped(t *testing.T) {
	p := newPBX(t, "secret")
	p.setRegMode("drop")
	cfg := p.config(t, "secret")
	cfg.RequestTimeout = 100 * time.Millisecond
	u := startUA(t, cfg)
	eventually(t, 5*time.Second, "5 attempts", func() bool { return u.Status().Attempts >= 5 })
	st := u.Status()
	if st.State != StateRetrying || st.AuthFailures != 0 {
		t.Fatalf("status %+v: a PBX that is down is retried, not given up on", st)
	}
	if !strings.Contains(st.Hint, "did not answer") {
		t.Fatalf("hint %q", st.Hint)
	}
	// It recovers when the PBX comes back.
	p.setRegMode()
	waitState(t, u, StateRegistered, 3*time.Second)
}

func TestBacksOffExponentially(t *testing.T) {
	p := newPBX(t, "secret")
	p.setRegMode("drop")
	cfg := p.config(t, "secret")
	cfg.RequestTimeout = 20 * time.Millisecond
	cfg.BackoffMin, cfg.BackoffMax = 40*time.Millisecond, 320*time.Millisecond
	u := startUA(t, cfg)
	eventually(t, 5*time.Second, "6 attempts", func() bool { return len(p.registers()) >= 6 })
	p.mu.Lock()
	at := append([]time.Time(nil), p.regAt...)
	p.mu.Unlock()
	var gaps []time.Duration
	for i := 1; i < 6; i++ {
		gaps = append(gaps, at[i].Sub(at[i-1]))
	}
	// timeout + 40, 80, 160, 320, 320 ms
	if gaps[2] < 2*gaps[0] || gaps[3] < gaps[2] {
		t.Fatalf("gaps %v do not grow", gaps)
	}
	if gaps[4] > cfg.BackoffMax+cfg.RequestTimeout+200*time.Millisecond {
		t.Fatalf("gap %v beyond the cap %v", gaps[4], cfg.BackoffMax)
	}
	_ = u
}

func TestAnswersOptionsAndRefusesAnyoneButTheRegistrar(t *testing.T) {
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)
	us := sip.Uri{Scheme: "sip", User: "100", Host: "127.0.0.1", Port: cfg.LocalPort}

	ask := func(c *sipgo.Client) int {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		res, err := c.Do(ctx, sip.NewRequest(sip.OPTIONS, us))
		if err != nil {
			t.Fatalf("OPTIONS: %v", err)
		}
		return res.StatusCode
	}
	// The PBX qualifies us (qualify_frequency=60): 200, or inbound calls go to voicemail.
	if code := ask(p.client); code != 200 {
		t.Fatalf("OPTIONS from the PBX: %d, want 200", code)
	}
	// A scanner elsewhere gets nothing useful.
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname("127.0.0.66"))
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	other, err := sipgo.NewClient(ua, sipgo.WithClientConnectionAddr("127.0.0.66:0"))
	if err != nil {
		t.Fatal(err)
	}
	if code := ask(other); code != 403 {
		t.Fatalf("OPTIONS from 127.0.0.66: %d, want 403", code)
	}
	// Nor can it place a call to us.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	inv := sip.NewRequest(sip.INVITE, us)
	inv.SetBody(answerSDP(newFarMedia(t), -1))
	res, err := other.Do(ctx, inv)
	if err != nil || res.StatusCode != 403 {
		t.Fatalf("INVITE from 127.0.0.66: %v %v, want 403", res, err)
	}
}

func TestAHandlerPanicIsAnErrorResponseNotACrash(t *testing.T) {
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "secret"))
	req := sip.NewRequest(sip.INFO, sip.Uri{Scheme: "sip", User: "100", Host: "127.0.0.1"})
	req.SetSource("127.0.0.1:5060")
	via := &sip.ViaHeader{ProtocolName: "SIP", ProtocolVersion: "2.0", Transport: "UDP", Host: "127.0.0.1", Port: 5060, Params: sip.NewParams()}
	via.Params.Add("branch", sip.GenerateBranch())
	req.AppendHeader(via)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INFO})
	tx := siptest.NewServerTxRecorder(req)
	u.guard(func(*sip.Request, sip.ServerTransaction) { panic("index out of range") })(req, tx)
	res := tx.Result()
	if len(res) != 1 || res[0].StatusCode != 500 {
		t.Fatalf("responses %v, want one 500", res)
	}
}
