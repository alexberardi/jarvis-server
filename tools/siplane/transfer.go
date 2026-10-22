package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/sip"
)

// The jarvis-inbound-transfer* scenarios (loop #448; docs/cc/phone-sip.md §3.4, §4 step 4):
// jarvisd answers the carrier's call to the DID, as the receptionist does, then blind-transfers
// it with REFER to a target the household configured. They prove on a real Asterisk what
// diago's Refer cannot tell us (it returns on the 202): the PBX's NOTIFYs arrive, the final one
// decides, and on success the caller talks to the target while our leg is gone. Which target,
// and when, is the receptionist's (phone's inbound_test.go); here the target is fixed.

// answeredInbound registers jarvisd as 100, has the carrier call the DID and returns jarvisd's
// answered, started call. The caller of answeredInbound closes done.
func (l *lane) answeredInbound(ctx context.Context, r *report) (call *sip.Call, cs map[string]*ctrl, done func(), err error) {
	answered := make(chan *sip.Call, 1)
	ua, err := l.inboundUA(ctx, r, func(_ context.Context, in sip.Incoming) sip.Admission {
		return sip.Admission{Answered: func(c *sip.Call) { answered <- c },
			Abandoned: func(reason string) { r.failf("the call was not answered: %s", reason) }}
	})
	if err != nil {
		return nil, nil, nil, err
	}
	closeUA := func() { ua.Close(context.Background()) }
	if st := waitUA(ua, sip.StateRegistered, 15*time.Second); st.State != sip.StateRegistered {
		closeUA()
		r.failf("not registered: %+v", st)
		return nil, nil, nil, nil
	}
	names := []string{"carrier", "alice"}
	cs, cdone, err := l.connect(ctx, names...)
	if err != nil {
		closeUA()
		return nil, nil, nil, err
	}
	release := func() { cdone(); closeUA() }
	if err := cs["carrier"].Command(ctx, "dial", l.uri("5551230000")); err != nil {
		release()
		return nil, nil, nil, err
	}
	select {
	case call = <-answered:
	case <-time.After(15 * time.Second):
		r.failf("jarvisd did not answer")
		release()
		return nil, nil, nil, nil
	}
	select {
	case <-call.Started():
	case <-time.After(10 * time.Second):
		r.failf("no audio from the caller after the answer")
		call.Close()
		release()
		return nil, nil, nil, nil
	}
	go func() {
		for range call.Frames() {
		}
	}()
	return call, cs, func() { call.Close(); release() }, nil
}

// jarvisInboundTransfer: jarvisd transfers the caller to Alice (101); Alice answers, the PBX's
// final NOTIFY says 200, jarvisd hangs up its leg, and the caller and Alice hear each other.
func jarvisInboundTransfer(ctx context.Context, l *lane, r *report) error {
	const callerSays = "Thanks, I will hold for Alice."
	const aliceSays = "Hello, this is Alice, how can I help?"
	if err := l.say(ctx, l.party("carrier"), callerSays, 7, 18); err != nil {
		return err
	}
	if err := l.say(ctx, l.party("alice"), aliceSays, 1, 9); err != nil {
		return err
	}
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	call, cs, done, err := l.answeredInbound(ctx, r)
	if err != nil || call == nil {
		return err
	}
	defer done()
	result := make(chan error, 1)
	start := time.Now()
	go func() {
		tctx, cancel := within(ctx, 45*time.Second)
		defer cancel()
		result <- call.Transfer(tctx, "101")
	}()
	w, cancel := within(ctx, 15*time.Second)
	_, err = cs["alice"].Wait(w, "CALL_INCOMING")
	cancel()
	if err != nil {
		r.failf("101 did not ring after the REFER: %v", err)
		return nil
	}
	r.notef("101 rang %.1f s after the REFER", time.Since(start).Seconds())
	// Asterisk's final NOTIFY (200) comes once the target rings, not when it answers: the caller
	// is the PBX's from the 202 on, and a target that does not answer is the PBX's to route.
	time.Sleep(time.Second)
	if err := cs["alice"].Command(ctx, "accept", ""); err != nil {
		return err
	}
	select {
	case err := <-result:
		if err != nil {
			r.failf("the transfer failed: %v", err)
			return nil
		}
		r.notef("the transfer was reported complete %.1f s after the REFER", time.Since(start).Seconds())
	case <-time.After(30 * time.Second):
		r.failf("the transfer did not complete")
		return nil
	}
	select {
	case <-call.Done():
	case <-time.After(5 * time.Second):
		r.failf("jarvisd's leg did not end after the transfer")
	}
	if end, detail := call.End(); end != sip.EndTransferred {
		r.failf("jarvisd's leg ended %q (%s), want transferred", end, detail)
	} else {
		r.notef("jarvisd's leg ended: %s (%s)", end, detail)
	}
	waitClosed(ctx, r, 30*time.Second, cs, "alice", "carrier")
	if err := l.expectHeard(ctx, r, "jarvis-inbound-transfer", l.party("carrier"), aliceSays); err != nil {
		return err
	}
	return l.expectHeard(ctx, r, "jarvis-inbound-transfer", l.party("alice"), callerSays)
}

// jarvisInboundTransferFails: the transfer's target is busy (the trunk's busy number, through
// the outbound route as an external target would go). The PBX reports the failure in its final
// NOTIFY, the caller hears busy from the PBX, and jarvisd neither takes it for a success nor
// stays on its leg, which no longer has the caller.
func jarvisInboundTransferFails(ctx context.Context, l *lane, r *report) error {
	if err := l.say(ctx, l.party("carrier"), "", 0, 25); err != nil {
		return err
	}
	restore, err := l.release(ctx)
	if err != nil {
		return err
	}
	defer restore()
	call, cs, done, err := l.answeredInbound(ctx, r)
	if err != nil || call == nil {
		return err
	}
	defer done()
	tctx, cancel := within(ctx, 45*time.Second)
	err = call.Transfer(tctx, "2125550486")
	cancel()
	var te *sip.TransferError
	switch {
	case err == nil:
		r.failf("a transfer to a busy number was reported as a success")
	case errors.As(err, &te):
		r.notef("the transfer failed: code %d, accepted %t: %s", te.Code, te.Accepted, te.Detail)
	default:
		r.failf("the transfer: %v", err)
	}
	// The PBX took the caller off our leg with the 202 (their Busy() runs on the caller's own
	// channel), so jarvisd must not go on talking into its leg: it hangs it up.
	if te != nil && !te.Gone {
		r.failf("the caller was taken for the transfer, but jarvisd thinks they are still on its leg")
	}
	select {
	case <-call.Done():
		end, detail := call.End()
		r.notef("jarvisd's leg ended: %s (%s)", end, detail)
		if end == sip.EndTransferred {
			r.failf("jarvisd's leg ended as transferred")
		}
	case <-time.After(5 * time.Second):
		r.failf("jarvisd's empty leg is still up after the failed transfer")
	}
	ev := waitClosed(ctx, r, 40*time.Second, cs, "carrier")
	r.notef("the caller's call ended: %s", strings.TrimSpace(ev["carrier"].Param))
	if !cs["carrier"].Saw("CALL_ESTABLISHED") {
		r.failf("the caller's call was never up: %s", strings.Join(cs["carrier"].Seen(), " "))
	}
	return nil
}
