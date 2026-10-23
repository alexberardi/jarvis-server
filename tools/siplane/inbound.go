package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

// The jarvis-inbound* scenarios (loop #446): jarvisd's SIP provider holds 100 and answers the
// calls the PBX routes to it, as a household with phone.inbound.enabled has it do. They prove
// the SIP side of the receptionist on a real Asterisk: the inbound route reaches it from the
// registrar, the caller comes as P-Asserted-Identity, the answer waits out the answer delay
// with 180 Ringing, a ring group's other phone can pick up first (our leg is cancelled,
// never answered), a second call above the cap is refused 486 and the PBX tells the caller
// busy, and the audio goes both ways. The receptionist's conversation itself (the model, its
// two tools, the row and the notification) is phone's (inbound_test.go, and on a real model
// inbound_model_test.go); here the disclosure is played and the caller's message transcribed.

const laneHousehold = "the lane household"

// inboundUA is jarvisd's UA as 100 with decide as its Config.Inbound.
func (l *lane) inboundUA(ctx context.Context, r *report, decide func(ctx context.Context, in sip.Incoming) sip.Admission) (*sip.UA, error) {
	return l.jarvisUA(ctx, "127.0.0.1", jarvisPassword, r, func(c *sip.Config) { c.Inbound = decide })
}

// disclosurePCM is the receptionist's disclosure through the sandbox's TTS, at 8 kHz.
func (l *lane) disclosurePCM(ctx context.Context) (string, []int16, error) {
	text := live.BuildInboundDisclosure(live.InboundBrief{Household: laneHousehold})
	wav, err := l.voice.Speak(ctx, text)
	if err != nil {
		return "", nil, err
	}
	pcm, rate, err := wavPCM(wav)
	if err != nil {
		return "", nil, err
	}
	return text, live.Resample(pcm, rate, 8000), nil
}

// jarvisInbound: the carrier calls the Jarvis DID; jarvisd answers after the answer delay,
// says the disclosure, and records the caller's message until the caller hangs up.
func jarvisInbound(ctx context.Context, l *lane, r *report) error {
	return jarvisInboundCall(ctx, l, r, "jarvis-inbound", "5551230000",
		"Hi, this is Sam from the bike shop. Your repair is ready, please call me back tomorrow.",
		func(in sip.Incoming) string {
			if in.Number == "" || in.Source != "pai" {
				return "want the caller's number from P-Asserted-Identity (send_pai=yes)"
			}
			return ""
		})
}

// jarvisInboundWithheld: the same call from a caller who withheld their number (the withheld
// DID sets CALLERID(pres)=prohib, as a carrier's Privacy: id does). FreePBX 17's
// trust_id_outbound=no sends it to 100 with no PAI and an anonymous From: jarvisd records no
// caller ID and still answers and hears the message.
func jarvisInboundWithheld(ctx context.Context, l *lane, r *report) error {
	return jarvisInboundCall(ctx, l, r, "jarvis-inbound-withheld", "5551230009",
		"Hello, I am calling about the car you have for sale. Please call me back.",
		func(in sip.Incoming) string {
			if in.Number != "" || in.Name != "" || in.Source != "withheld" {
				return "want a withheld caller: no number or name, source withheld"
			}
			return ""
		})
}

// jarvisInboundCall is one inbound call to did, answered by jarvisd, the caller saying message;
// badCaller says what is wrong with the caller ID jarvisd was offered, "" when nothing is.
func jarvisInboundCall(ctx context.Context, l *lane, r *report, scenario, did, message string, badCaller func(sip.Incoming) string) error {
	const delay = 2 * time.Second
	const lead, total = 11.0, 19.0
	c := l.party("carrier")
	if err := l.say(ctx, c, message, lead, total); err != nil {
		return err
	}
	disclosure, ours, err := l.disclosurePCM(ctx)
	if err != nil {
		return err
	}
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	type offer struct {
		in sip.Incoming
		at time.Time
	}
	offers := make(chan offer, 4)
	answered := make(chan *sip.Call, 1)
	ua, err := l.inboundUA(ctx, r, func(_ context.Context, in sip.Incoming) sip.Admission {
		offers <- offer{in, time.Now()}
		return sip.Admission{Delay: delay, Answered: func(call *sip.Call) { answered <- call },
			Abandoned: func(reason string) { r.failf("the call was not answered: %s", reason) }}
	})
	if err != nil {
		return err
	}
	defer ua.Close(context.Background())
	if st := waitUA(ua, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("not registered: %+v", st)
		return nil
	}
	cs, done, err := l.connect(ctx, "carrier")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["carrier"].Command(ctx, "dial", l.uri(did)); err != nil {
		return err
	}
	var o offer
	select {
	case o = <-offers:
	case <-time.After(15 * time.Second):
		r.failf("the inbound route did not reach jarvisd (no INVITE)")
		return nil
	}
	r.notef("jarvisd was offered the call: caller %q (%q) from %s", o.in.Number, o.in.Name, o.in.Source)
	if bad := badCaller(o.in); bad != "" {
		r.failf("caller %+v: %s", o.in, bad)
	}
	var call *sip.Call
	select {
	case call = <-answered:
	case <-time.After(15 * time.Second):
		r.failf("jarvisd did not answer")
		return nil
	}
	defer call.Close()
	if after := time.Since(o.at); after < delay {
		r.failf("answered %.1f s after the INVITE, before the %s answer delay", after.Seconds(), delay)
	} else {
		r.notef("answered %.1f s after the INVITE (answer delay %s)", after.Seconds(), delay)
	}
	select {
	case <-call.Started():
	case <-call.Done():
		end, detail := call.End()
		r.failf("the call ended before its audio started: %s (%s)", end, detail)
		return nil
	case <-time.After(10 * time.Second):
		r.failf("no audio from the caller after the answer")
		return nil
	}
	if _, err := call.Play(ours); err != nil {
		r.failf("play: %v", err)
	}
	var heard []int16
	timeout := time.After(time.Duration(total+15) * time.Second)
record:
	for {
		select {
		case f, ok := <-call.Frames():
			if !ok {
				break record
			}
			heard = append(heard, f...)
		case <-timeout:
			r.failf("the call did not end")
			break record
		}
	}
	end, detail := call.End()
	r.notef("the call ended: %s (%s) after %.1f s of the caller's audio", end, detail, float64(len(heard))/8000)
	if end != sip.EndRemote {
		r.failf("end %q, want the caller hanging up", end)
	}
	waitClosed(ctx, r, 10*time.Second, cs, "carrier")
	wav := live.WAV(live.Resample(heard, 8000, 16000), 16000)
	if l.out != "" {
		os.WriteFile(filepath.Join(l.out, scenario+"-jarvisd.wav"), wav, 0o644)
	}
	text, err := l.voice.Transcribe(ctx, wav)
	if err != nil {
		return err
	}
	w, _ := bestWER([]string{message}, text)
	r.notef("jarvisd heard: %q (WER %.2f against %q)", text, w, message)
	l.keep(scenario+"-jarvisd.txt", text+"\n")
	if w > maxWER {
		r.failf("jarvisd heard %q, WER %.2f > %.2f", text, w, maxWER)
	}
	return l.expectHeard(ctx, r, scenario, c, disclosure)
}

// jarvisInboundRingGroup: the ring-group DID rings 101 and jarvisd's 100; jarvisd's answer
// delay lets Alice pick up first, and the PBX cancels jarvisd's leg before it answers.
func jarvisInboundRingGroup(ctx context.Context, l *lane, r *report) error {
	const total = 8
	if err := l.say(ctx, l.party("carrier"), "", 0, total); err != nil {
		return err
	}
	if err := l.say(ctx, l.party("alice"), "", 0, total); err != nil {
		return err
	}
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	abandoned := make(chan string, 1)
	ua, err := l.inboundUA(ctx, r, func(context.Context, sip.Incoming) sip.Admission {
		return sip.Admission{Delay: 8 * time.Second,
			Answered:  func(c *sip.Call) { r.failf("jarvisd answered though Alice picked up first"); c.Close() },
			Abandoned: func(reason string) { abandoned <- reason }}
	})
	if err != nil {
		return err
	}
	defer ua.Close(context.Background())
	if st := waitUA(ua, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("not registered: %+v", st)
		return nil
	}
	cs, done, err := l.connect(ctx, "carrier", "alice")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["carrier"].Command(ctx, "dial", l.uri("5551230600")); err != nil {
		return err
	}
	w, cancel := within(ctx, 15*time.Second)
	_, err = cs["alice"].Wait(w, "CALL_INCOMING")
	cancel()
	if err != nil {
		r.failf("101 did not ring: %v", err)
		return nil
	}
	time.Sleep(time.Second)
	if err := cs["alice"].Command(ctx, "accept", ""); err != nil {
		return err
	}
	select {
	case reason := <-abandoned:
		r.notef("jarvisd's leg was cancelled while it rang: %s", reason)
	case <-time.After(10 * time.Second):
		r.failf("jarvisd's leg was not cancelled when Alice answered")
	}
	waitClosed(ctx, r, (total+20)*time.Second, cs, "carrier", "alice")
	if !cs["alice"].Saw("CALL_ESTABLISHED") {
		r.failf("Alice did not get the call: %s", strings.Join(cs["alice"].Seen(), " "))
	} else {
		r.notef("Alice (101) answered the ring group during jarvisd's answer delay")
	}
	return nil
}

// jarvisInboundBusy: Alice is on a call jarvisd answered; the carrier calls the DID (call
// waiting rings 100 again); jarvisd's concurrent cap of 1 refuses it 486, and the PBX gives the
// caller busy (FreePBX: voicemail) while the first call goes on.
func jarvisInboundBusy(ctx context.Context, l *lane, r *report) error {
	if err := l.say(ctx, l.party("alice"), "", 0, 14); err != nil {
		return err
	}
	if err := l.say(ctx, l.party("carrier"), "", 0, 5); err != nil {
		return err
	}
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	var mu sync.Mutex
	active, refused := 0, 0
	first := make(chan *sip.Call, 1)
	ua, err := l.inboundUA(ctx, r, func(context.Context, sip.Incoming) sip.Admission {
		mu.Lock()
		defer mu.Unlock()
		if active >= 1 {
			refused++
			return sip.Admission{Refuse: sip.RefuseBusy}
		}
		active++
		return sip.Admission{Answered: func(c *sip.Call) {
			go func() {
				<-c.Done()
				mu.Lock()
				active--
				mu.Unlock()
			}()
			first <- c
		}, Abandoned: func(string) { mu.Lock(); active--; mu.Unlock() }}
	})
	if err != nil {
		return err
	}
	defer ua.Close(context.Background())
	if st := waitUA(ua, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		r.failf("not registered: %+v", st)
		return nil
	}
	cs, done, err := l.connect(ctx, "alice", "carrier")
	if err != nil {
		return err
	}
	defer done()
	if err := cs["alice"].Command(ctx, "dial", l.uri("100")); err != nil {
		return err
	}
	var call *sip.Call
	select {
	case call = <-first:
	case <-time.After(15 * time.Second):
		r.failf("jarvisd did not answer Alice's call")
		return nil
	}
	defer call.Close()
	go func() {
		for range call.Frames() {
		}
	}()
	time.Sleep(time.Second)
	if err := cs["carrier"].Command(ctx, "dial", l.uri("5551230000")); err != nil {
		return err
	}
	ev := waitClosed(ctx, r, 30*time.Second, cs, "carrier")
	mu.Lock()
	n := refused
	mu.Unlock()
	if n != 1 {
		r.failf("jarvisd refused %d calls, want the second one", n)
	}
	if !strings.HasPrefix(ev["carrier"].Param, "486") {
		r.failf("the carrier's call ended %q, want 486 Busy Here (voicemail on FreePBX)", ev["carrier"].Param)
	}
	select {
	case <-call.Done():
		end, detail := call.End()
		r.failf("the first call ended with the second: %s (%s)", end, detail)
	default:
		r.notef("the second call was refused busy (%q); the first stayed up", ev["carrier"].Param)
	}
	waitClosed(ctx, r, 30*time.Second, cs, "alice")
	return nil
}
