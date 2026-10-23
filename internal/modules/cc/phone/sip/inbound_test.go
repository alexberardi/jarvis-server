package sip

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// pbxCall is a call the fake PBX places to the UA (the inbound route ringing 100), with every
// response it got.
type pbxCall struct {
	d      *sipgo.DialogClientSession
	cancel context.CancelFunc
	far    *farMedia

	mu        sync.Mutex
	responses []int
	at        []time.Time
	sent      time.Time
	final     chan error    // the answer (nil) or the refusal
	settled   chan struct{} // WaitAnswer (and the ACK) are over
}

func (c *pbxCall) codes() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.responses...)
}

// at200 is how long after the INVITE the 200 came (0: none).
func (c *pbxCall) at200() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, code := range c.responses {
		if code == 200 {
			return c.at[i].Sub(c.sent)
		}
	}
	return 0
}

// callUA has the PBX call the UA's extension with headers added to the INVITE.
func (p *fakePBX) callUA(t *testing.T, u *UA, headers ...sip.Header) *pbxCall {
	t.Helper()
	far := newFarMedia(t)
	dua := sipgo.DialogUA{Client: p.client, ContactHDR: sip.ContactHeader{Address: sip.Uri{Scheme: "sip", User: "pbx", Host: p.host, Port: p.port}}}
	to := sip.Uri{Scheme: "sip", User: u.cfg.Extension, Host: "127.0.0.1", Port: u.cfg.LocalPort}
	ctx, cancel := context.WithCancel(context.Background())
	c := &pbxCall{cancel: cancel, far: far, final: make(chan error, 1), settled: make(chan struct{}), sent: time.Now()}
	d, err := dua.Invite(ctx, to, answerSDP(far, 101), headers...)
	if err != nil {
		cancel()
		t.Fatalf("INVITE: %v", err)
	}
	c.d = d
	t.Cleanup(func() {
		cancel()
		<-c.settled
		bctx, bcancel := context.WithTimeout(context.Background(), time.Second)
		defer bcancel()
		_ = d.Bye(bctx)
		d.Close()
	})
	go func() {
		defer close(c.settled)
		err := d.WaitAnswer(ctx, sipgo.AnswerOptions{OnResponse: func(res *sip.Response) error {
			c.mu.Lock()
			c.responses = append(c.responses, res.StatusCode)
			c.at = append(c.at, time.Now())
			c.mu.Unlock()
			return nil
		}})
		if err == nil {
			if res := d.InviteResponse; res != nil {
				if r, perr := canonicalSDP(res.Body()); perr == nil {
					far.mu.Lock()
					far.peer = &net.UDPAddr{IP: r.ip, Port: r.port}
					far.mu.Unlock()
				}
			}
			err = d.Ack(ctx)
		}
		c.final <- err
	}()
	return c
}

// waitFinal waits for the call's final response: nil when answered, else its status.
func (c *pbxCall) waitFinal(t *testing.T, d time.Duration) (answered bool, status int) {
	t.Helper()
	select {
	case err := <-c.final:
		if err == nil {
			return true, 200
		}
		var re *sipgo.ErrDialogResponse
		if errors.As(err, &re) {
			return false, re.Res.StatusCode
		}
		return false, -1
	case <-time.After(d):
		t.Fatal("no final response to the inbound call")
	}
	return false, 0
}

// inboundUA is a UA registered to a fresh PBX whose inbound calls decide does.
func inboundUA(t *testing.T, decide func(in Incoming) Admission) (*fakePBX, *UA) {
	t.Helper()
	p := newPBX(t, "secret")
	cfg := p.config(t, "secret")
	if decide != nil {
		cfg.Inbound = func(_ context.Context, in Incoming) Admission { return decide(in) }
	}
	u := startUA(t, cfg)
	waitState(t, u, StateRegistered, 3*time.Second)
	return p, u
}

func TestInboundCallsAreRefused480WithoutAHandler(t *testing.T) {
	p, u := inboundUA(t, nil)
	c := p.callUA(t, u)
	if answered, code := c.waitFinal(t, 3*time.Second); answered || code != 480 {
		t.Fatalf("answered=%t status %d, want 480 (the PBX's next destination)", answered, code)
	}
}

func TestInboundRefusalIsSentAsDecided(t *testing.T) {
	p, u := inboundUA(t, func(Incoming) Admission { return Admission{Refuse: 486} })
	c := p.callUA(t, u)
	if answered, code := c.waitFinal(t, 3*time.Second); answered || code != 486 {
		t.Fatalf("answered=%t status %d, want 486 Busy Here (FreePBX: voicemail)", answered, code)
	}
}

func TestInboundCallerIsPAIThenFrom(t *testing.T) {
	got := make(chan Incoming, 2)
	p, u := inboundUA(t, func(in Incoming) Admission { got <- in; return Admission{Refuse: 480} })

	pai := sip.NewHeader("P-Asserted-Identity", `"Bob Smith" <sip:+12125550142@127.0.0.1>`)
	p.callUA(t, u, pai).waitFinal(t, 3*time.Second)
	in := <-got
	if in.Number != "+12125550142" || in.Name != "Bob Smith" || in.Source != "pai" || in.CallID == "" {
		t.Fatalf("with PAI: %+v, want +12125550142 / Bob Smith from pai", in)
	}
	// No PAI: From (the fake PBX's From user is its own "pbx").
	p.callUA(t, u).waitFinal(t, 3*time.Second)
	if in := <-got; in.Source != "from" || in.Number == "" || in.Number == "+12125550142" {
		t.Fatalf("without PAI: %+v, want From's user", in)
	}
}

// A caller who withholds their number: FreePBX 17 (trust_id_outbound=no) sends no PAI and an
// anonymous From (RFC 3323), so there is no number and no name to record, not "anonymous".
func TestIncomingWithheldCaller(t *testing.T) {
	for _, tc := range []struct {
		name, from string
		headers    []sip.Header
		want       Incoming
	}{
		{"anonymous From, no PAI", `"Anonymous" <sip:anonymous@anonymous.invalid>;tag=1`, nil,
			Incoming{Source: "withheld"}},
		{"anonymous user on the PBX's host", `<sip:anonymous@127.0.0.1>;tag=1`, nil,
			Incoming{Source: "withheld"}},
		{"Privacy: id", `"Bob" <sip:2125550142@127.0.0.1>;tag=1`, []sip.Header{sip.NewHeader("Privacy", "id")},
			Incoming{Source: "withheld"}},
		{"PAI with Privacy: id (a trusting PBX)", `"Anonymous" <sip:anonymous@anonymous.invalid>;tag=1`,
			[]sip.Header{sip.NewHeader("P-Asserted-Identity", `"Bob" <sip:+12125550142@127.0.0.1>`), sip.NewHeader("Privacy", "id;critical")},
			Incoming{Source: "withheld"}},
		{"Privacy: none", `"Bob" <sip:2125550142@127.0.0.1>;tag=1`, []sip.Header{sip.NewHeader("Privacy", "none")},
			Incoming{Number: "2125550142", Name: "Bob", Source: "from"}},
		{"a caller named Anonymous with a number", `"Anonymous" <sip:2125550142@127.0.0.1>;tag=1`, nil,
			Incoming{Number: "2125550142", Name: "Anonymous", Source: "from"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "100", Host: "127.0.0.1"})
			req.AppendHeader(sip.NewHeader("From", tc.from))
			for _, h := range tc.headers {
				req.AppendHeader(h)
			}
			if got := incoming(req); got != tc.want {
				t.Fatalf("incoming = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseIdentity(t *testing.T) {
	for _, tc := range []struct{ in, num, name string }{
		{`<tel:+12125550100>`, "+12125550100", ""},
		{`"Ann" <tel:+12125550100;cpc=ordinary>`, "+12125550100", "Ann"},
		{`"Ann" <sip:2125550100@pbx.lan;user=phone>`, "2125550100", "Ann"},
		{"\"Ign\x07ore\" <sip:555@h>", "555", "Ignore"},
	} {
		num, name, ok := parseIdentity(tc.in)
		if !ok || num != tc.num || name != tc.name {
			t.Errorf("parseIdentity(%q) = %q %q %t, want %q %q", tc.in, num, name, ok, tc.num, tc.name)
		}
	}
	if got := splitAddresses(`"A, B" <sip:1@h>, <tel:+2>`); len(got) != 2 {
		t.Errorf("splitAddresses: %q", got)
	}
	if long := clean(string(make([]byte, 0)) + "12345678901234567890123456789012345678901234567890123456789012345678901234567890"); len(long) != 64 {
		t.Errorf("clean kept %d characters, want 64", len(long))
	}
}

func TestInboundRingsForTheDelayThenAnswersWithAudioBothWays(t *testing.T) {
	answered := make(chan *Call, 1)
	const delay = 400 * time.Millisecond
	p, u := inboundUA(t, func(Incoming) Admission {
		return Admission{Delay: delay, Answered: func(c *Call) { answered <- c },
			Abandoned: func(r string) { t.Errorf("abandoned: %s", r) }}
	})
	pc := p.callUA(t, u)
	if ok, code := pc.waitFinal(t, 5*time.Second); !ok {
		t.Fatalf("not answered: %d", code)
	}
	codes := pc.codes()
	if len(codes) < 2 || codes[0] != 180 {
		t.Fatalf("responses %v, want 180 Ringing before the 200", codes)
	}
	if at := pc.at200(); at < delay {
		t.Fatalf("answered after %s, before the %s answer delay", at, delay)
	}
	var c *Call
	select {
	case c = <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("Answered was not called")
	}
	if c.ID() == "" || !c.Answered() {
		t.Fatalf("call %q answered=%t", c.ID(), c.Answered())
	}
	// The caller's audio starts the call, as an outbound call's does.
	pc.far.stream(func(int) []int16 { return tone(FrameSamples, 8000) })
	select {
	case <-c.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("not started by the caller's RTP")
	}
	if frames := collect(c, 5, 2*time.Second); len(frames) < 5 || rms(frames[4]) < 2000 {
		t.Fatalf("%d frames of the caller's audio", len(frames))
	}
	drain(c)
	if _, err := c.Play(tone(FrameSamples*10, 8000)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 3*time.Second, "our audio at the caller", func() bool {
		n := 0
		for _, pk := range pc.far.packets() {
			if loud(pk) {
				n++
			}
		}
		return n >= 5
	})
	// The caller hangs up: the call ends as the far end's.
	bctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pc.d.Bye(bctx); err != nil {
		t.Fatalf("BYE: %v", err)
	}
	if end, _ := waitDone(t, c, 3*time.Second); end != EndRemote {
		t.Fatalf("end %q, want remote", end)
	}
}

func TestCancelWhileRingingLeavesTheCallUnanswered(t *testing.T) {
	abandoned := make(chan string, 1)
	p, u := inboundUA(t, func(Incoming) Admission {
		return Admission{Delay: 3 * time.Second, Answered: func(*Call) { t.Error("answered after a CANCEL") },
			Abandoned: func(r string) { abandoned <- r }}
	})
	pc := p.callUA(t, u)
	eventually(t, 2*time.Second, "180 Ringing", func() bool { return len(pc.codes()) > 0 })
	// Another phone in the ring group answered: the PBX cancels our leg.
	pc.cancel()
	select {
	case <-abandoned:
	case <-time.After(2 * time.Second):
		t.Fatal("Abandoned was not called after the CANCEL")
	}
	time.Sleep(200 * time.Millisecond)
	for _, code := range pc.codes() {
		if code == 200 {
			t.Fatalf("responses %v: answered after the CANCEL", pc.codes())
		}
	}
}

func TestOurHangupOfAnInboundCallSendsBye(t *testing.T) {
	answered := make(chan *Call, 1)
	p, u := inboundUA(t, func(Incoming) Admission {
		return Admission{Answered: func(c *Call) { answered <- c }}
	})
	pc := p.callUA(t, u)
	if ok, code := pc.waitFinal(t, 5*time.Second); !ok {
		t.Fatalf("not answered: %d", code)
	}
	c := <-answered
	drain(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Hangup(ctx)
	if end, _ := waitDone(t, c, 3*time.Second); end != EndClosed {
		t.Fatalf("end %q, want closed", end)
	}
	eventually(t, 3*time.Second, "our BYE at the PBX", func() bool { return p.got(sip.BYE, c.ID()) })
}

func TestAScannerCannotReachTheInboundHandler(t *testing.T) {
	called := make(chan struct{}, 1)
	_, u := inboundUA(t, func(Incoming) Admission { called <- struct{}{}; return Admission{Refuse: 480} })
	ua, err := sipgo.NewUA(sipgo.WithUserAgentHostname(otherLoopback(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer ua.Close()
	other, err := sipgo.NewClient(ua, sipgo.WithClientConnectionAddr(otherLoopback(t)+":0"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	inv := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "100", Host: "127.0.0.1", Port: u.cfg.LocalPort})
	inv.SetBody(answerSDP(newFarMedia(t), -1))
	res, err := other.Do(ctx, inv)
	if err != nil || res.StatusCode != 403 {
		t.Fatalf("INVITE from 127.0.0.66: %v %v, want 403", res, err)
	}
	select {
	case <-called:
		t.Fatal("a call from outside the PBX reached Config.Inbound")
	case <-time.After(100 * time.Millisecond):
	}
}

// The inbound watch self-test shows what Jarvis answered: a refusal's code, or 200.
func TestLastInboundRecordsWhatWeAnswered(t *testing.T) {
	var answer atomic.Bool
	p, u := inboundUA(t, func(Incoming) Admission {
		if !answer.Load() {
			return Admission{Refuse: 486}
		}
		return Admission{Answered: func(c *Call) { go c.Hangup(context.Background()) }}
	})
	pai := sip.NewHeader("P-Asserted-Identity", `"Bob" <sip:+12125550142@127.0.0.1>`)
	p.callUA(t, u, pai).waitFinal(t, 3*time.Second)
	if in := u.Status().LastInbound; in.Response != 486 || in.Caller != "+12125550142" || in.CallerName != "Bob" || in.At.IsZero() {
		t.Fatalf("after a refusal: %+v, want 486 from +12125550142 (Bob)", in)
	}
	answer.Store(true)
	if ok, code := p.callUA(t, u).waitFinal(t, 3*time.Second); !ok {
		t.Fatalf("not answered: %d", code)
	}
	eventually(t, 2*time.Second, "LastInbound 200", func() bool { return u.Status().LastInbound.Response == 200 })
}
