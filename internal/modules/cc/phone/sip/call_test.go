package sip

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/zaf/g711"
)

// registered is a UA registered to a fresh fake PBX.
func registered(t *testing.T) (*fakePBX, *UA) {
	t.Helper()
	p := newPBX(t, "secret")
	u := startUA(t, p.config(t, "secret"))
	waitState(t, u, StateRegistered, 3*time.Second)
	return p, u
}

func waitDone(t *testing.T, c *Call, d time.Duration) (End, string) {
	t.Helper()
	select {
	case <-c.Done():
	case <-time.After(d):
		t.Fatal("the call did not end")
	}
	return c.End()
}

func TestFinalResponsesMapToOutcomes(t *testing.T) {
	p, u := registered(t)
	p.setInvite(func(d *diago.DialogServerSession) {
		switch d.ToUser() {
		case "486":
			d.Respond(486, "Busy Here", nil)
		case "600":
			d.Respond(600, "Busy Everywhere", nil)
		case "480":
			d.Respond(480, "Temporarily Unavailable", nil)
		case "404":
			d.Respond(404, "Not Found", nil)
		case "484":
			d.Respond(484, "Address Incomplete", nil)
		case "403":
			d.Respond(403, "Forbidden", nil)
		case "503":
			d.Respond(503, "Service Unavailable", nil)
		case "183503":
			// FreePBX with no answer or no route: an announcement as early media, then 503.
			d.Respond(183, "Session Progress", nil)
			time.Sleep(100 * time.Millisecond)
			d.Respond(503, "Service Unavailable", nil)
		}
	})
	for _, tc := range []struct {
		number string
		want   End
	}{
		{"486", EndBusy}, {"600", EndBusy}, {"480", EndNoAnswer}, {"404", EndNotFound},
		{"484", EndNotFound}, {"403", EndFailed}, {"503", EndFailed}, {"183503", EndNoAnswer},
	} {
		t.Run(tc.number, func(t *testing.T) {
			c, err := u.Dial(context.Background(), tc.number)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			end, detail := waitDone(t, c, 5*time.Second)
			if end != tc.want {
				t.Fatalf("end %q (%s), want %q", end, detail, tc.want)
			}
			if !strings.Contains(detail, tc.number[len(tc.number)-3:]) {
				t.Fatalf("detail %q does not carry the response code", detail)
			}
			select {
			case <-c.Started():
				t.Fatal("a call that was never answered started")
			default:
			}
			if c.Answered() {
				t.Fatal("answered")
			}
		})
	}
}

func TestEarlyMediaIsNeverAnAnswer(t *testing.T) {
	p, u := registered(t)
	far := newFarMedia(t)
	p.setInvite(func(d *diago.DialogServerSession) {
		d.Respond(183, "Session Progress", answerSDP(far, -1), sip.NewHeader("Content-Type", "application/sdp"))
		<-d.Context().Done()
	})
	c, err := u.Dial(context.Background(), "2125550183")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	time.Sleep(500 * time.Millisecond)
	select {
	case <-c.Started():
		t.Fatal("183 started the call")
	case <-c.Done():
		t.Fatal("ended")
	default:
	}
	if c.Answered() {
		t.Fatal("183 counted as an answer")
	}
}

func TestHangupWhileRingingCancels(t *testing.T) {
	p, u := registered(t)
	cancelled := make(chan struct{})
	p.setInvite(func(d *diago.DialogServerSession) {
		d.Ringing()
		<-d.Context().Done()
		close(cancelled)
	})
	c, err := u.Dial(context.Background(), "2125550180")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := c.Hangup(context.Background()); err != nil {
		t.Fatal(err)
	}
	end, _ := waitDone(t, c, 5*time.Second)
	if end != EndClosed {
		t.Fatalf("end %q", end)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the PBX never saw a CANCEL")
	}
}

// answered is a call the fake PBX answered, with its media.
func answered(t *testing.T, dtmfPT int) (*fakePBX, *UA, *Call, *farMedia, chan *diago.DialogServerSession) {
	t.Helper()
	p, u := registered(t)
	far := newFarMedia(t)
	dialogs := make(chan *diago.DialogServerSession, 1)
	p.setInvite(func(d *diago.DialogServerSession) {
		answer(t, d, far, dtmfPT, func() { dialogs <- d })
	})
	c, err := u.Dial(context.Background(), "2125550100")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	eventually(t, 3*time.Second, "the answer", c.Answered)
	return p, u, c, far, dialogs
}

func TestStartedWaitsForTheFirstRTPAfterTheAnswer(t *testing.T) {
	_, _, c, far, _ := answered(t, -1)
	// Answered, but the far end's audio has not arrived: the disclosure must not start yet.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-c.Started():
		t.Fatal("started before any RTP")
	default:
	}
	far.stream(func(int) []int16 { return tone(FrameSamples, 8000) })
	select {
	case <-c.Started():
	case <-time.After(2 * time.Second):
		t.Fatal("not started by the far end's RTP")
	}
	frames := collect(c, 10, 2*time.Second)
	if len(frames) < 10 {
		t.Fatalf("%d frames", len(frames))
	}
	for _, f := range frames {
		if len(f) != FrameSamples || rms(f) < 2000 {
			t.Fatalf("frame of %d samples, rms %.0f: not the far end's tone", len(f), rms(f))
		}
	}
}

func TestPacerAlwaysSendsAndPlaysWhatIsQueued(t *testing.T) {
	_, _, c, far, _ := answered(t, -1)
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	drain(c)
	<-c.Started()

	// Nothing queued: silence every 20 ms (Asterisk's rtptimeout would hang up otherwise).
	far.reset()
	time.Sleep(time.Second)
	pkts := far.packets()
	if len(pkts) < 40 || len(pkts) > 60 {
		t.Fatalf("%d packets in 1 s of silence, want ~50", len(pkts))
	}
	for _, p := range pkts {
		if p.PayloadType != 0 || len(p.Payload) != FrameSamples || loud(p) {
			t.Fatalf("packet pt %d, %d bytes, loud=%t: want PCMU silence", p.PayloadType, len(p.Payload), loud(p))
		}
	}
	checkContinuous(t, pkts)

	// Queued audio goes out in real time, and the position follows what was sent.
	far.reset()
	end, err := c.Play(tone(8000/2, 8000))
	if err != nil || end != 4000 {
		t.Fatalf("Play: %d %v", end, err)
	}
	if pos, _ := c.Played(); pos != 0 {
		t.Fatalf("position %d before anything was sent", pos)
	}
	start := time.Now()
	for {
		pos, changed := c.Played()
		if pos >= end {
			break
		}
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
			t.Fatalf("playback stuck at %d", pos)
		}
	}
	if took := time.Since(start); took < 400*time.Millisecond {
		t.Fatalf("half a second of audio played in %s: not paced", took)
	}
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, p := range far.packets() {
		if loud(p) {
			n++
		}
	}
	if n != 25 {
		t.Fatalf("%d loud packets, want 25 (0.5 s)", n)
	}
	checkContinuous(t, far.packets())
}

func TestFlushDropsTheQueue(t *testing.T) {
	_, _, c, far, _ := answered(t, -1)
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	drain(c)
	<-c.Started()
	far.reset()
	end, _ := c.Play(tone(8000*3, 8000))
	time.Sleep(200 * time.Millisecond)
	c.Flush()
	if pos, _ := c.Played(); pos != end {
		t.Fatalf("position %d after flush, want the queue's end %d", pos, end)
	}
	time.Sleep(300 * time.Millisecond)
	n := 0
	for _, p := range far.packets() {
		if loud(p) {
			n++
		}
	}
	if n > 15 {
		t.Fatalf("%d loud packets: the flushed queue kept playing", n)
	}
	// A later Play continues on the same scale.
	if e2, _ := c.Play(tone(160, 8000)); e2 != end+160 {
		t.Fatalf("next end %d, want %d", e2, end+160)
	}
}

func TestNoRTPWatchdogEndsTheCall(t *testing.T) {
	p, _, c, far, dialogs := answered(t, -1)
	_ = p
	d := <-dialogs
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	<-c.Started()
	drain(c)
	far.close() // the far end goes quiet (a dead PBX, a network cut)
	end, detail := waitDone(t, c, 5*time.Second)
	if end != EndNoMedia || !strings.Contains(detail, "no audio") {
		t.Fatalf("end %q (%s)", end, detail)
	}
	select {
	case <-d.Context().Done(): // our BYE reached the PBX
	case <-time.After(3 * time.Second):
		t.Fatal("the PBX's dialog is still up: no BYE")
	}
}

func TestTheFarEndHangingUpEndsTheCall(t *testing.T) {
	_, _, c, far, dialogs := answered(t, -1)
	d := <-dialogs
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	<-c.Started()
	drain(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Hangup(ctx); err != nil {
		t.Fatal(err)
	}
	if end, _ := waitDone(t, c, 3*time.Second); end != EndRemote {
		t.Fatalf("end %q", end)
	}
	if _, err := c.Play(tone(160, 1)); err == nil {
		t.Fatal("Play after the end succeeded")
	}
}

func TestOurHangupSendsBye(t *testing.T) {
	_, _, c, far, dialogs := answered(t, -1)
	d := <-dialogs
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	<-c.Started()
	drain(c)
	if err := c.Hangup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if end, _ := waitDone(t, c, 3*time.Second); end != EndClosed {
		t.Fatalf("end %q", end)
	}
	select {
	case <-d.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("no BYE at the PBX")
	}
}

func TestReinviteAndUpdateAreAnsweredWithTheSameSDP(t *testing.T) {
	_, _, c, far, dialogs := answered(t, -1)
	d := <-dialogs
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	<-c.Started()
	var loudFrames atomic.Int32
	go func() {
		for f := range c.Frames() {
			if rms(f) > 2000 {
				loudFrames.Add(1)
			}
		}
	}()
	ours, err := canonicalSDP(c.localSDP())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Session-timer refresh (timers=yes): a re-INVITE with the same offer.
	res, err := inDialog(ctx, d, sip.INVITE, answerSDP(far, -1))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("re-INVITE: %v %v", res, err)
	}
	sameMedia(t, "re-INVITE", res.Body(), ours)
	// RFC 4028 by UPDATE, with and without SDP.
	res, err = inDialog(ctx, d, sip.UPDATE, answerSDP(far, -1))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("UPDATE: %v %v", res, err)
	}
	sameMedia(t, "UPDATE", res.Body(), ours)
	res, err = inDialog(ctx, d, sip.UPDATE, nil)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("UPDATE without SDP: %v %v", res, err)
	}

	// direct_media=yes: a re-INVITE moves the far end's media; ours follows it.
	moved := newFarMedia(t)
	far.mu.Lock()
	moved.peer = far.peer
	far.mu.Unlock()
	res, err = inDialog(ctx, d, sip.INVITE, answerSDP(moved, -1))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("media re-INVITE: %v %v", res, err)
	}
	sameMedia(t, "media re-INVITE", res.Body(), ours)
	eventually(t, 2*time.Second, "RTP at the new address", func() bool { return len(moved.packets()) > 5 })
	// And the far end's audio from there reaches the call.
	far.close()
	moved.stream(func(int) []int16 { return tone(FrameSamples, 8000) })
	eventually(t, 2*time.Second, "audio from the new address", func() bool { return loudFrames.Load() > 10 })

	// An unusable offer is refused, and the call goes on.
	res, err = inDialog(ctx, d, sip.INVITE, []byte("v=0\r\nc=IN\r\nm=audio\r\n"))
	if err != nil || res.StatusCode != 488 {
		t.Fatalf("bad re-INVITE: %v %v, want 488", res, err)
	}
	select {
	case <-c.Done():
		t.Fatal("a bad re-INVITE ended the call")
	default:
	}
}

func TestOurOfferAdvertisesNoSessionTimer(t *testing.T) {
	p, _, _, _, _ := answered(t, -1)
	p.mu.Lock()
	inv := p.invites[0]
	p.mu.Unlock()
	for _, name := range []string{"Supported", "Require"} {
		for _, h := range inv.GetHeaders(name) {
			if strings.Contains(strings.ToLower(h.Value()), "timer") {
				t.Fatalf("INVITE %s: %s", name, h.Value())
			}
		}
	}
	offer, err := canonicalSDP(inv.Body())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"0", "8", "101"} {
		if !contains(offer.formats, f) {
			t.Fatalf("offer formats %v lack %s (PCMU, PCMA, telephone-event)", offer.formats, f)
		}
	}
}

func TestAnSSRCChangeKeepsTheAudioComing(t *testing.T) {
	_, _, c, far, _ := answered(t, -1)
	far.stream(func(int) []int16 { return tone(FrameSamples, 8000) })
	<-c.Started()
	if n := len(collect(c, 10, 2*time.Second)); n < 10 {
		t.Fatalf("%d frames before the change", n)
	}
	far.newSSRC()
	frames := collect(c, 25, 3*time.Second)
	if len(frames) < 25 {
		t.Fatalf("%d frames after the SSRC change", len(frames))
	}
	if rms(frames[len(frames)-1]) < 2000 {
		t.Fatal("audio after the SSRC change is not the far end's")
	}
}

func TestDTMFUsesThePayloadTypeFromTheAnswer(t *testing.T) {
	_, _, c, far, _ := answered(t, 96)
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	drain(c)
	<-c.Started()
	far.reset()
	if err := c.SendDTMF("1#"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	var ev []rtp.Packet
	for _, p := range far.packets() {
		switch p.PayloadType {
		case 96:
			ev = append(ev, p)
		case 101:
			t.Fatal("DTMF on 101, which the far end did not offer")
		}
	}
	if len(ev) != 2*(dtmfTone+dtmfEnd) {
		t.Fatalf("%d event packets, want %d", len(ev), 2*(dtmfTone+dtmfEnd))
	}
	first, second := ev[:dtmfTone+dtmfEnd], ev[dtmfTone+dtmfEnd:]
	for i, set := range [][]rtp.Packet{first, second} {
		want := []byte{1, 11}[i]
		for j, p := range set {
			if p.Payload[0] != want || p.Timestamp != set[0].Timestamp {
				t.Fatalf("digit %d packet %d: event %d ts %d", i, j, p.Payload[0], p.Timestamp)
			}
			if end := p.Payload[1]&0x80 != 0; end != (j >= dtmfTone) {
				t.Fatalf("digit %d packet %d: E bit %t", i, j, end)
			}
		}
		if !set[0].Marker {
			t.Fatalf("digit %d: no marker on its first packet", i)
		}
	}
	if second[0].Timestamp <= first[0].Timestamp {
		t.Fatal("the second digit reuses the first's timestamp")
	}
	if err := c.SendDTMF("x"); err == nil {
		t.Fatal("x accepted as a digit")
	}
}

func TestDTMFWithoutTelephoneEventIsUnsupported(t *testing.T) {
	_, _, c, far, _ := answered(t, -1)
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	drain(c)
	<-c.Started()
	if err := c.SendDTMF("1"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SendDTMF: %v, want ErrUnsupported", err)
	}
}

func TestCloseEndsLiveCalls(t *testing.T) {
	_, u, c, far, dialogs := answered(t, -1)
	d := <-dialogs
	far.stream(func(int) []int16 { return make([]int16, FrameSamples) })
	drain(c)
	<-c.Started()
	u.Close(context.Background())
	if end, _ := waitDone(t, c, 3*time.Second); end != EndClosed {
		t.Fatalf("end %q", end)
	}
	select {
	case <-d.Context().Done():
	case <-time.After(3 * time.Second):
		t.Fatal("no BYE at the PBX on shutdown")
	}
}

// loud reports a PCMU packet carrying more than silence.
func loud(p rtp.Packet) bool {
	if p.PayloadType != 0 {
		return false
	}
	pcm := decode(p.Payload, g711.DecodeUlawFrame)
	return rms(pcm) > 1000
}

// checkContinuous fails on a sequence or timestamp gap in our stream.
func checkContinuous(t *testing.T, pkts []rtp.Packet) {
	t.Helper()
	for i := 1; i < len(pkts); i++ {
		a, b := pkts[i-1], pkts[i]
		if b.SequenceNumber != a.SequenceNumber+1 || b.Timestamp != a.Timestamp+FrameSamples || b.SSRC != a.SSRC {
			t.Fatalf("packet %d: seq %d→%d ts %d→%d ssrc %x→%x", i, a.SequenceNumber, b.SequenceNumber, a.Timestamp, b.Timestamp, a.SSRC, b.SSRC)
		}
	}
}

// sameMedia checks an answer's SDP is the one we answered with: same address and port.
func sameMedia(t *testing.T, what string, body []byte, ours *remoteSDP) {
	t.Helper()
	got, err := canonicalSDP(body)
	if err != nil {
		t.Fatalf("%s answer: %v", what, err)
	}
	if !got.ip.Equal(ours.ip) || got.port != ours.port {
		t.Fatalf("%s answered with %s:%d, want our %s:%d", what, got.ip, got.port, ours.ip, ours.port)
	}
}
