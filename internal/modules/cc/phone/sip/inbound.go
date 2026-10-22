package sip

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo/sip"
)

// Inbound calls (docs/cc/phone-sip.md §3.4, §4 step 3): the PBX routes a call to our extension,
// Config.Inbound decides it (refuse, or answer after a delay), and an answered call is a Call
// like a placed one. Only the registrar reaches handleInvite (guard answers anyone else 403).

// Incoming is an inbound call offered to Config.Inbound, before it is answered.
type Incoming struct {
	// CallID is the INVITE's Call-ID, the answered Call's ID.
	CallID string
	// Number and Name are who the PBX says is calling: P-Asserted-Identity (FreePBX's
	// sendrpid=pai), else From. A hint for the message, never authentication: a caller (or a
	// trunk) can put anything there. Both are trimmed to printable text.
	Number, Name string
	// Source is the header they came from: "pai" or "from".
	Source string
}

// Admission is what Config.Inbound decides about an incoming call.
type Admission struct {
	// Refuse is a final status to send instead of answering: 480 sends the caller on to the
	// PBX's next destination, 486 is busy (FreePBX: voicemail). 0 answers.
	Refuse int
	// Delay is how long the call rings (180 Ringing) before the answer, so the people in a
	// ring group get to pick up first. A CANCEL meanwhile ends it unanswered.
	Delay time.Duration
	// Answered receives the answered call. It must return promptly (the call loop runs on
	// its own goroutine); the dialog lasts until the Call is Done.
	Answered func(c *Call)
	// Abandoned is told why an admitted call was never answered: the caller hung up while it
	// rang (or another phone of a ring group answered), its SDP was unusable, or the answer
	// failed. Each admitted call gets exactly one of Answered or Abandoned.
	Abandoned func(reason string)
}

// Inbound reasons for refusals, as sent.
var refusals = map[int]string{
	sip.StatusForbidden:              "Forbidden",
	sip.StatusTemporarilyUnavailable: "Temporarily Unavailable",
	sip.StatusBusyHere:               "Busy Here",
	sip.StatusNotAcceptableHere:      "Not Acceptable Here",
	603:                              "Decline",
}

// handleInvite offers a new inbound call to Config.Inbound and answers or refuses it as it
// says. It returns when the call has ended: diago ends the dialog on the way out.
func (u *UA) handleInvite(d *diago.DialogServerSession) {
	in := incoming(d.InviteRequest)
	log := u.cfg.Log.With("call_id", in.CallID, "caller", in.Number, "caller_from", in.Source)
	if u.cfg.Inbound == nil {
		log.Info("sip: inbound call refused (inbound calls are off)")
		u.refuse(d, sip.StatusTemporarilyUnavailable)
		return
	}
	adm := u.cfg.Inbound(d.Context(), in)
	if adm.Refuse == 0 && adm.Answered == nil {
		adm.Refuse = sip.StatusTemporarilyUnavailable
	}
	if adm.Refuse != 0 {
		log.Info("sip: inbound call refused", "status", adm.Refuse)
		u.refuse(d, adm.Refuse)
		return
	}
	abandon := func(reason string) {
		log.Info("sip: inbound call not answered", "reason", reason)
		if adm.Abandoned != nil {
			adm.Abandoned(reason)
		}
	}
	offer, err := canonicalSDP(d.InviteRequest.Body())
	if err != nil {
		log.Warn("sip: refused an inbound call's SDP", "err", err)
		u.refuse(d, sip.StatusNotAcceptableHere)
		abandon("its SDP was unusable")
		return
	}
	d.InviteRequest.SetBody(offer.body)
	if err := d.Ringing(); err != nil {
		abandon("ringing failed: " + err.Error())
		return
	}
	if adm.Delay > 0 {
		t := time.NewTimer(adm.Delay)
		select {
		case <-t.C:
		case <-d.Context().Done():
			t.Stop()
			abandon("the caller hung up, or another phone answered, before the answer delay ran out")
			return
		case <-u.done:
			t.Stop()
			u.refuse(d, sip.StatusTemporarilyUnavailable)
			abandon("shutting down")
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Call{ua: u, dlg: d, ctx: ctx, cancel: cancel, id: in.CallID,
		started: make(chan struct{}), done: make(chan struct{}), frames: make(chan []int16),
		inReady: make(chan struct{}, 1), playedCh: make(chan struct{}), dtmfPT: offer.dtmfPT}
	// Known by its Call-ID before the 200 goes out: an UPDATE may follow the ACK at once.
	u.track(c)
	med, err := d.Answer(diago.AnswerOptions{})
	if err != nil {
		u.untrack(c)
		cancel()
		abandon("answering failed: " + err.Error())
		return
	}
	c.mu.Lock()
	c.ourSDP = med.MediaSession().LocalSDP()
	c.mu.Unlock()
	c.answer(med)
	go c.deliver()
	log.Info("sip: inbound call answered", "after", adm.Delay)
	adm.Answered(c)
	defer func() {
		if p := recover(); p != nil {
			log.Error("sip: inbound call panicked", "panic", p)
			c.finish(EndFailed, "internal error")
		}
	}()
	c.serve(med)
}

func (u *UA) refuse(d *diago.DialogServerSession, code int) {
	reason, ok := refusals[code]
	if !ok {
		code, reason = sip.StatusTemporarilyUnavailable, refusals[sip.StatusTemporarilyUnavailable]
	}
	if err := d.Respond(code, reason, nil); err != nil {
		u.cfg.Log.Warn("sip: refusing an inbound call failed", "status", code, "err", err)
	}
}

// incoming reads an INVITE's caller: P-Asserted-Identity first, then From.
func incoming(req *sip.Request) Incoming {
	in := Incoming{}
	if id := req.CallID(); id != nil {
		in.CallID = id.Value()
	}
	for _, h := range req.GetHeaders("P-Asserted-Identity") {
		for _, v := range splitAddresses(h.Value()) {
			if num, name, ok := parseIdentity(v); ok {
				in.Number, in.Name, in.Source = num, name, "pai"
				return in
			}
		}
	}
	if f := req.From(); f != nil {
		in.Number, in.Name, in.Source = clean(f.Address.User), clean(f.DisplayName), "from"
	}
	return in
}

// parseIdentity reads one name-addr: `"Name" <sip:+12125550100@host>` or `<tel:+12125550100>`.
func parseIdentity(v string) (number, name string, ok bool) {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "<tel:"); i >= 0 {
		rest := v[i+len("<tel:"):]
		end := strings.IndexAny(rest, ";>")
		if end < 0 {
			return "", "", false
		}
		name = strings.Trim(strings.TrimSpace(v[:i]), `"`)
		return clean(rest[:end]), clean(name), clean(rest[:end]) != ""
	}
	var uri sip.Uri
	display, err := sip.ParseAddressValue(v, &uri, nil)
	if err != nil || uri.User == "" {
		return "", "", false
	}
	return clean(uri.User), clean(display), true
}

// splitAddresses splits a header value holding several name-addrs at commas outside quotes
// and angle brackets.
func splitAddresses(v string) []string {
	var out []string
	quoted, angle, start := false, false, 0
	for i, r := range v {
		switch {
		case r == '"':
			quoted = !quoted
		case r == '<' && !quoted:
			angle = true
		case r == '>' && !quoted:
			angle = false
		case r == ',' && !quoted && !angle:
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

// clean keeps a caller-ID field to 64 printable characters: it is shown in a notification
// and stored, and anyone can write anything in it.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	return s
}
