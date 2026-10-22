package sip

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/icholy/digest"
	"github.com/pion/rtp"
	"github.com/zaf/g711"
)

// fakePBX is the L0 far end (docs/cc/phone-sip.md §5): a registrar with digest auth and a
// callee whose behaviour each test sets, on loopback. Its calls answer with a hand-written SDP
// pointing at a raw RTP socket (farMedia), so a test controls every packet both ways.
type fakePBX struct {
	t        *testing.T
	host     string
	port     int
	password string
	ua       *sipgo.UserAgent
	client   *sipgo.Client

	mu       sync.Mutex
	regs     []*sip.Request // every REGISTER, as received
	regAt    []time.Time
	options  int
	regMode  []string // per REGISTER, in order; "" answers normally, "forbid" 403, "drop" nothing; the last repeats
	onInvite func(d *diago.DialogServerSession)
	invites  []*sip.Request
}

const pbxRealm, pbxNonce = "lane", "0123456789abcdef"

func freePort(t *testing.T, host string) int {
	t.Helper()
	c, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func testLog(t *testing.T) *slog.Logger {
	if testing.Verbose() {
		return slog.New(slog.NewTextHandler(&testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.DiscardHandler)
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}

func newPBX(t *testing.T, password string) *fakePBX {
	t.Helper()
	p := &fakePBX{t: t, host: "127.0.0.1", port: freePort(t, "127.0.0.1"), password: password}
	var err error
	if p.ua, err = sipgo.NewUA(sipgo.WithUserAgent("pbx"), sipgo.WithUserAgentHostname(p.host)); err != nil {
		t.Fatal(err)
	}
	if p.client, err = sipgo.NewClient(p.ua, sipgo.WithClientConnectionAddr(fmt.Sprintf("%s:%d", p.host, p.port))); err != nil {
		t.Fatal(err)
	}
	srv, err := sipgo.NewServer(p.ua)
	if err != nil {
		t.Fatal(err)
	}
	dg := diago.NewDiago(p.ua, diago.WithServer(srv), diago.WithClient(p.client), diago.WithLogger(slog.New(slog.DiscardHandler)),
		diago.WithTransport(diago.Transport{Transport: "udp", BindHost: p.host, BindPort: p.port}))
	srv.OnRegister(p.handleRegister)
	srv.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		p.mu.Lock()
		p.options++
		p.mu.Unlock()
		tx.Respond(sip.NewResponseFromRequest(req, 200, "OK", nil))
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := dg.ServeBackground(ctx, func(d *diago.DialogServerSession) {
		p.mu.Lock()
		p.invites = append(p.invites, d.InviteRequest)
		f := p.onInvite
		p.mu.Unlock()
		if f != nil {
			f(d)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		p.ua.Close()
	})
	return p
}

func (p *fakePBX) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	p.mu.Lock()
	n := len(p.regs)
	p.regs = append(p.regs, req.Clone())
	p.regAt = append(p.regAt, time.Now())
	mode := ""
	if len(p.regMode) > 0 {
		mode = p.regMode[min(n, len(p.regMode)-1)]
	}
	p.mu.Unlock()
	switch mode {
	case "drop":
		return
	case "forbid":
		tx.Respond(sip.NewResponseFromRequest(req, 403, "Forbidden", nil))
		return
	}
	challenge := func() {
		res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
		res.AppendHeader(sip.NewHeader("WWW-Authenticate", fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=MD5`, pbxRealm, pbxNonce)))
		tx.Respond(res)
	}
	h := req.GetHeader("Authorization")
	if h == nil {
		challenge()
		return
	}
	cred, err := digest.ParseCredentials(h.Value())
	if err != nil {
		challenge()
		return
	}
	want, err := digest.Digest(&digest.Challenge{Realm: pbxRealm, Nonce: pbxNonce, Algorithm: "MD5"},
		digest.Options{Method: "REGISTER", URI: cred.URI, Username: cred.Username, Password: p.password})
	if err != nil || want.Response != cred.Response {
		challenge()
		return
	}
	res := sip.NewResponseFromRequest(req, 200, "OK", nil)
	if c := req.Contact(); c != nil {
		cc := c.Clone()
		exp := "300"
		if e := req.GetHeader("Expires"); e != nil {
			exp = e.Value()
		}
		cc.Params.Add("expires", exp)
		res.AppendHeader(cc)
	}
	tx.Respond(res)
}

// authorized are the REGISTERs that carried credentials (one per attempt; the first of
// each attempt only draws the challenge).
func (p *fakePBX) authorized() []*sip.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*sip.Request
	for _, r := range p.regs {
		if r.GetHeader("Authorization") != nil {
			out = append(out, r)
		}
	}
	return out
}

func (p *fakePBX) registers() []*sip.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*sip.Request(nil), p.regs...)
}

func (p *fakePBX) optionsSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.options
}

func (p *fakePBX) setRegMode(modes ...string) {
	p.mu.Lock()
	p.regMode = modes
	p.mu.Unlock()
}

func (p *fakePBX) setInvite(f func(d *diago.DialogServerSession)) {
	p.mu.Lock()
	p.onInvite = f
	p.mu.Unlock()
}

// config is a UA account on this PBX with test timings.
func (p *fakePBX) config(t *testing.T, password string) Config {
	return Config{
		Server: p.host, Port: p.port, Extension: "100", Password: password,
		LocalPort: freePort(t, "127.0.0.1"), BindHost: "127.0.0.1", Log: testLog(t),
		RequestTimeout: 400 * time.Millisecond, KeepAlive: 100 * time.Millisecond,
		BackoffMin: 20 * time.Millisecond, BackoffMax: 50 * time.Millisecond, NoRTPTimeout: time.Second,
	}
}

func startUA(t *testing.T, cfg Config) *UA {
	t.Helper()
	u, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { u.Close(context.Background()) })
	return u
}

// waitState waits until u reaches want.
func waitState(t *testing.T, u *UA, want State, d time.Duration) Status {
	t.Helper()
	deadline := time.After(d)
	for {
		st := u.Status()
		if st.State == want {
			return st
		}
		select {
		case <-u.Changed():
		case <-deadline:
			t.Fatalf("state %s, want %s (last error %q)", st.State, want, st.LastError)
		}
	}
}

func eventually(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// farMedia is the far end's RTP: a raw socket that records what arrives and sends what the
// test says.
type farMedia struct {
	t    *testing.T
	conn *net.UDPConn
	mu   sync.Mutex
	peer *net.UDPAddr
	got  []rtp.Packet
	ssrc uint32
	seq  uint16
	ts   uint32
	stop chan struct{}
	once sync.Once
}

func newFarMedia(t *testing.T) *farMedia {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	m := &farMedia{t: t, conn: c, ssrc: 0x11111111, stop: make(chan struct{})}
	go m.recv()
	t.Cleanup(m.close)
	return m
}

func (m *farMedia) close() {
	m.once.Do(func() { close(m.stop); m.conn.Close() })
}

func (m *farMedia) port() int { return m.conn.LocalAddr().(*net.UDPAddr).Port }

func (m *farMedia) recv() {
	buf := make([]byte, 2000)
	for {
		n, err := m.conn.Read(buf)
		if err != nil {
			return
		}
		var p rtp.Packet
		if p.Unmarshal(append([]byte(nil), buf[:n]...)) != nil {
			continue
		}
		m.mu.Lock()
		m.got = append(m.got, p)
		m.mu.Unlock()
	}
}

func (m *farMedia) packets() []rtp.Packet {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]rtp.Packet(nil), m.got...)
}

func (m *farMedia) reset() {
	m.mu.Lock()
	m.got = nil
	m.mu.Unlock()
}

// send writes one 20 ms PCMU frame of pcm to the peer.
func (m *farMedia) send(pcm []int16) {
	m.mu.Lock()
	peer := m.peer
	m.seq++
	m.ts += FrameSamples
	h := rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: m.seq, Timestamp: m.ts, SSRC: m.ssrc}
	m.mu.Unlock()
	if peer == nil {
		return
	}
	payload := make([]byte, len(pcm))
	for i, s := range pcm {
		payload[i] = g711.EncodeUlawFrame(s)
	}
	b, _ := (&rtp.Packet{Header: h, Payload: payload}).Marshal()
	m.conn.WriteToUDP(b, peer)
}

// stream sends frames of f(i) every 20 ms until stop.
func (m *farMedia) stream(f func(i int) []int16) {
	go func() {
		t := time.NewTicker(frameDur)
		defer t.Stop()
		for i := 0; ; i++ {
			select {
			case <-m.stop:
				return
			case <-t.C:
			}
			m.send(f(i))
		}
	}()
}

// newSSRC switches the stream to another source with an unrelated sequence (a PBX bridge or
// transfer does this).
func (m *farMedia) newSSRC() {
	m.mu.Lock()
	m.ssrc, m.seq, m.ts = 0x22222222, 40000, 900000
	m.mu.Unlock()
}

func tone(n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*400*float64(i)/8000))
	}
	return out
}

func rms(pcm []int16) float64 {
	var s float64
	for _, x := range pcm {
		s += float64(x) * float64(x)
	}
	if len(pcm) == 0 {
		return 0
	}
	return math.Sqrt(s / float64(len(pcm)))
}

// answerSDP is the far end's answer: PCMU (and telephone-event at pt when pt >= 0), media at m.
func answerSDP(m *farMedia, dtmfPT int) []byte {
	fmts, attrs := "0", "a=rtpmap:0 PCMU/8000\r\n"
	if dtmfPT >= 0 {
		fmts += fmt.Sprintf(" %d", dtmfPT)
		attrs += fmt.Sprintf("a=rtpmap:%d telephone-event/8000\r\na=fmtp:%d 0-16\r\n", dtmfPT, dtmfPT)
	}
	return []byte("v=0\r\no=- 4242 4242 IN IP4 127.0.0.1\r\ns=pbx\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		fmt.Sprintf("m=audio %d RTP/AVP %s\r\n", m.port(), fmts) + attrs + "a=ptime:20\r\na=sendrecv\r\n")
}

// answer answers d with media m and points m at the caller's RTP, then calls answered (when
// set); it returns when the call ends.
func answer(t *testing.T, d *diago.DialogServerSession, m *farMedia, dtmfPT int, answered func()) {
	offer, err := canonicalSDP(d.InviteRequest.Body())
	if err != nil {
		t.Errorf("our INVITE's SDP: %v", err)
		return
	}
	m.mu.Lock()
	m.peer = &net.UDPAddr{IP: offer.ip, Port: offer.port}
	m.mu.Unlock()
	if err := d.RespondSDP(answerSDP(m, dtmfPT)); err != nil {
		t.Errorf("answer: %v", err)
		return
	}
	if answered != nil {
		answered()
	}
	<-d.Context().Done()
}

// inDialog sends method within d's dialog to the caller's Contact, with body as SDP.
func inDialog(ctx context.Context, d *diago.DialogServerSession, method sip.RequestMethod, body []byte) (*sip.Response, error) {
	req := sip.NewRequest(method, d.InviteRequest.Contact().Address)
	if body != nil {
		req.SetBody(body)
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res, err := d.DialogServerSession.Do(ctx, req)
	if err == nil && method == sip.INVITE && res.IsSuccess() {
		ack := sip.NewRequest(sip.ACK, d.InviteRequest.Contact().Address)
		_ = d.DialogServerSession.WriteRequest(ack)
	}
	return res, err
}

// collect reads n frames (or what arrives within d).
func collect(c *Call, n int, d time.Duration) [][]int16 {
	var out [][]int16
	deadline := time.After(d)
	for len(out) < n {
		select {
		case f, ok := <-c.Frames():
			if !ok {
				return out
			}
			out = append(out, f)
		case <-deadline:
			return out
		}
	}
	return out
}

// drain keeps reading a call's frames (the call loop always does) until it ends.
func drain(c *Call) {
	go func() {
		for range c.Frames() {
		}
	}()
}
