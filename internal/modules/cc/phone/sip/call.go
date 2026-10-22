package sip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/zaf/g711"
)

// FrameSamples is one 20 ms frame at 8 kHz.
const FrameSamples = 160

const frameDur = 20 * time.Millisecond

// maxInbound bounds the far end's audio held while the call loop is busy (a turn runs on its
// goroutine): 60 s. Beyond it the oldest frames go; RTP cannot be paused.
const maxInbound = 3000

// ErrUnsupported is a call operation the far end did not negotiate (DTMF without
// telephone-event in its SDP).
var ErrUnsupported = errors.New("sip: not negotiated with the far end")

// Call is one call, placed (Dial) or answered (Config.Inbound). Its audio is 8 kHz mono PCM16
// both ways, in 20 ms frames.
type Call struct {
	ua     *UA
	id     string
	d      *diago.DialogClientSession // an outbound call's dialog (nil inbound)
	srv    *diago.DialogServerSession // an inbound call's dialog (nil outbound), for Transfer
	dlg    dialog                     // the call's dialog, either way
	ctx    context.Context            // the INVITE's: cancelling it before the answer sends CANCEL
	cancel context.CancelFunc

	started   chan struct{}
	startOnce sync.Once
	done      chan struct{}
	doneOnce  sync.Once
	frames    chan []int16
	inReady   chan struct{} // inbound audio queued

	mu        sync.Mutex
	med       *diago.DialogMedia
	answered  bool
	early     bool // a 183 came before the final response
	end       End
	detail    string
	codec     media.Codec
	dtmfPT    int
	inbound   [][]int16
	partial   []int16
	queue     []int16 // playback not sent yet
	queuedEnd int64   // playback position at the end of everything queued
	played    int64   // playback position sent
	playedCh  chan struct{}
	dtmf      []rune
	ourSDP    []byte
	// ending is set just before we end an answered call ourselves (BYE), so the dialog's end
	// that follows is recorded as ours, not the far end's.
	ending       End
	endingDetail string
	xfer         chan struct{} // the transfer, closed when it is over (nil: none tried)

	lastRTP atomic.Int64 // unix nanos of the last RTP packet (or of the answer)
}

// dialog is what a call needs of its diago dialog, client (outbound) or server (inbound).
type dialog interface {
	Hangup(ctx context.Context) error
	Close() error
	Context() context.Context
}

// Dial places a call to number (already in the PBX's dial format: digits, optionally after a
// +) through the registrar. It returns once the INVITE is on its way; Started, Done and End
// say what became of it. Feature codes (*43, ##) never go through Dial: see DialFeature.
func (u *UA) Dial(ctx context.Context, number string) (*Call, error) {
	if !dialable(number) {
		return nil, fmt.Errorf("sip: bad number %q", number)
	}
	return u.dial(ctx, number)
}

// FeatureCodes are the PBX feature codes the self-tests dial (FreePBX's defaults): *43 the
// echo test, *65 says the extension's number. Nothing else: other codes change PBX state
// (*72 call forwarding, *69 call trace) or reach numbers the normal path refuses.
var FeatureCodes = []string{"*43", "*65"}

// DialFeature calls one of FeatureCodes: the self-tests' own path, apart from Dial, so the
// normal call path keeps refusing anything that is not a phone number.
func (u *UA) DialFeature(ctx context.Context, code string) (*Call, error) {
	if !slices.Contains(FeatureCodes, code) {
		return nil, fmt.Errorf("sip: %q is not a self-test feature code (%s)", code, strings.Join(FeatureCodes, ", "))
	}
	return u.dial(ctx, code)
}

func dialable(number string) bool {
	digits := strings.TrimPrefix(number, "+")
	return digits != "" && len(digits) <= 32 && strings.Trim(digits, "0123456789") == ""
}

func (u *UA) dial(ctx context.Context, number string) (*Call, error) {
	if err := u.Ready(); err != nil {
		return nil, err
	}
	to := u.registrar.Clone()
	to.User = number
	d, err := u.dg.NewDialog(*to, diago.NewDialogOptions{Transport: u.cfg.Transport})
	if err != nil {
		return nil, fmt.Errorf("sip: %w", err)
	}
	// Our own Call-ID, set now (sipgo would only add one when the INVITE goes out), so the
	// call is known by it before any request of its dialog can arrive.
	id := sip.CallIDHeader(sip.GenerateTagN(32) + "@jarvis")
	d.InviteRequest.AppendHeader(&id)
	ictx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c := &Call{ua: u, d: d, dlg: d, ctx: ictx, cancel: cancel, id: id.Value(),
		started: make(chan struct{}), done: make(chan struct{}), frames: make(chan []int16),
		inReady: make(chan struct{}, 1), playedCh: make(chan struct{}), dtmfPT: -1}
	u.track(c)
	go c.run()
	go c.deliver()
	return c, nil
}

// ID is the call's SIP Call-ID.
func (c *Call) ID() string { return c.id }

// Started is closed once the call is answered (200 OK, never 183) and the far end's first RTP
// packet has arrived: a carrier's answer supervision can run ahead of its audio.
func (c *Call) Started() <-chan struct{} { return c.started }

// Frames yields the far end's audio from Started on, 20 ms per frame; closed when the call ends.
func (c *Call) Frames() <-chan []int16 { return c.frames }

// Done is closed when the call has ended; End says why.
func (c *Call) Done() <-chan struct{} { return c.done }

// End is why the call ended, and a detail (the SIP response, the watchdog) for logs and the
// session's failure reason.
func (c *Call) End() (End, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.end, c.detail
}

// Answered reports whether the far end answered (200 OK).
func (c *Call) Answered() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.answered
}

// Play queues pcm behind what is queued; the pacer sends it in real time. It returns the
// playback position at which pcm ends.
func (c *Call) Play(pcm []int16) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.end != EndNone {
		return c.queuedEnd, fmt.Errorf("sip: call ended (%s)", c.end)
	}
	c.queue = append(c.queue, pcm...)
	c.queuedEnd += int64(len(pcm))
	return c.queuedEnd, nil
}

// Played is the playback position the pacer has sent, and a channel closed at its next change.
func (c *Call) Played() (int64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.played, c.playedCh
}

// Flush drops queued playback (barge-in): the position jumps to the end of what was queued.
func (c *Call) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = nil
	c.advance(c.queuedEnd)
}

func (c *Call) advance(pos int64) {
	if pos == c.played {
		return
	}
	c.played = pos
	close(c.playedCh)
	c.playedCh = make(chan struct{})
}

// SendDTMF sends digits as RFC 4733 events with the far end's telephone-event payload type.
func (c *Call) SendDTMF(digits string) error {
	for _, r := range digits {
		if !strings.ContainsRune("0123456789*#ABCD", r) {
			return fmt.Errorf("sip: %q is not a DTMF digit", r)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.answered || c.dtmfPT < 0 {
		return ErrUnsupported
	}
	c.dtmf = append(c.dtmf, []rune(digits)...)
	return nil
}

// Hangup ends the call: BYE once answered, CANCEL while it rings.
func (c *Call) Hangup(ctx context.Context) error {
	c.mu.Lock()
	answered, ended := c.answered, c.end != EndNone
	c.mu.Unlock()
	if ended {
		return nil
	}
	if !answered {
		c.cancel()
		return nil
	}
	c.endWith(EndClosed, "hung up")
	err := c.dlg.Hangup(ctx)
	c.finish(EndClosed, "hung up")
	return err
}

// Close hangs up if needed and releases the call. Safe to call more than once.
func (c *Call) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.Hangup(ctx)
	c.finish(EndClosed, "closed")
}

// finish records the first end and releases everything.
func (c *Call) finish(end End, detail string) {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		c.end, c.detail = end, detail
		med := c.med
		c.mu.Unlock()
		c.cancel()
		close(c.done)
		if med != nil {
			_ = med.Close()
		}
		_ = c.dlg.Close()
		c.ua.untrack(c)
		c.ua.cfg.Log.Info("sip: call ended", "call_id", c.id, "end", string(end), "detail", detail)
	})
}

// run places the INVITE and, once answered, runs the call's media until it ends.
func (c *Call) run() {
	defer func() {
		if p := recover(); p != nil {
			c.ua.cfg.Log.Error("sip: call panicked", "call_id", c.id, "panic", p)
			c.finish(EndFailed, fmt.Sprintf("internal error: %v", p))
		}
	}()
	med, err := c.d.Invite(c.ctx, diago.InviteClientOptions{
		Username: c.ua.cfg.Extension, Password: c.ua.cfg.Password, OnResponse: c.onResponse,
	})
	if err != nil {
		c.mu.Lock()
		early := c.early
		c.mu.Unlock()
		end, detail := outcome(err, c.ctx.Err() != nil, early)
		c.finish(end, detail)
		return
	}
	if err := c.d.Ack(c.ctx); err != nil {
		_ = med.Close()
		c.finish(EndTransport, "ACK failed: "+err.Error())
		return
	}
	c.answer(med)
	c.serve(med)
}

// serve runs an answered call's media until the call ends, either way.
func (c *Call) serve(med *diago.DialogMedia) {
	go c.read(med)
	go c.pace(med)
	go c.watch()
	select {
	case <-c.dlg.Context().Done():
		if x := c.transferring(); x != nil {
			// The PBX may end our leg around a transfer's final NOTIFY: Transfer says which end it was.
			select {
			case <-x:
			case <-time.After(2 * time.Second):
			}
		}
		c.mu.Lock()
		end, detail := c.ending, c.endingDetail
		c.mu.Unlock()
		if end == EndNone {
			end, detail = EndRemote, "the far end hung up"
		}
		c.finish(end, detail)
	case <-c.done:
	}
}

// endWith notes the end we are about to cause.
func (c *Call) endWith(end End, detail string) {
	c.mu.Lock()
	if c.ending == EndNone {
		c.ending, c.endingDetail = end, detail
	}
	c.mu.Unlock()
}

// onResponse sees every response to the INVITE before diago does: it notes early media
// (183 is never an answer) and canonicalises the answer's SDP (canonicalSDP).
func (c *Call) onResponse(res *sip.Response) error {
	c.mu.Lock()
	if c.ourSDP == nil {
		c.ourSDP = bytes.Clone(c.d.InviteRequest.Body())
	}
	c.mu.Unlock()
	if res.StatusCode == sip.StatusSessionInProgress {
		c.mu.Lock()
		c.early = true
		c.mu.Unlock()
	}
	if !res.IsSuccess() {
		return nil
	}
	r, err := canonicalSDP(res.Body())
	if err != nil {
		return err
	}
	res.SetBody(r.body)
	c.remoteMedia(r)
	return nil
}

// remoteMedia takes what a remote SDP says about DTMF.
func (c *Call) remoteMedia(r *remoteSDP) {
	c.mu.Lock()
	c.dtmfPT = r.dtmfPT
	c.mu.Unlock()
}

func (c *Call) answer(med *diago.DialogMedia) {
	codec := media.CodecAudioUlaw
	for _, cd := range med.MediaSession().CommonCodecs() {
		if cd.Name == "PCMU" || cd.Name == "PCMA" {
			codec = cd
			break
		}
	}
	c.mu.Lock()
	c.med, c.answered, c.codec = med, true, codec
	c.mu.Unlock()
	c.lastRTP.Store(time.Now().UnixNano())
	c.ua.cfg.Log.Info("sip: call answered", "call_id", c.id, "codec", codec.Name)
}

// localSDP is our offer, which diago also answers a re-INVITE with: an UPDATE gets it again.
// (A copy: diago's MediaSession is not safe to read beside its own re-INVITE handling.)
func (c *Call) localSDP() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ourSDP
}

// outcome maps a failed INVITE to an End: the final response, our own CANCEL, or silence.
func outcome(err error, cancelled, early bool) (End, string) {
	var res *sip.Response
	var pr *sipgo.ErrDialogResponse
	var vr sipgo.ErrDialogResponse
	switch {
	case errors.As(err, &pr):
		res = pr.Res
	case errors.As(err, &vr):
		res = vr.Res
	}
	if res == nil {
		if cancelled {
			return EndClosed, "cancelled before the answer"
		}
		return EndTransport, "the PBX did not answer the call: " + err.Error()
	}
	detail := fmt.Sprintf("%d %s", res.StatusCode, res.Reason)
	switch res.StatusCode {
	case 486, 600:
		return EndBusy, detail
	case 480, 408, 487:
		if cancelled && res.StatusCode == 487 {
			return EndClosed, "cancelled before the answer"
		}
		return EndNoAnswer, detail
	case 404, 410, 484, 604:
		return EndNotFound, detail
	case 503:
		if early {
			// FreePBX's no answer / no route: an announcement on 183, then 503.
			return EndNoAnswer, detail + " after early media"
		}
	}
	return EndFailed, detail
}

// read decodes the far end's RTP into 20 ms frames. The first packet starts the call.
func (c *Call) read(med *diago.DialogMedia) {
	defer func() {
		if p := recover(); p != nil {
			c.ua.cfg.Log.Error("sip: RTP reader panicked", "call_id", c.id, "panic", p)
			c.finish(EndTransport, "media failed")
		}
	}()
	r := med.RTPPacketReader
	buf := make([]byte, media.RTPBufSize)
	timeouts := 0
	for {
		n, err := r.Read(buf)
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() && timeouts < 50 {
			// A media re-INVITE swaps diago's RTP session and sets a read deadline to stop the
			// old reader; a read that starts after the swap still sees it. Clear it and go on.
			timeouts++
			_ = med.MediaSession().StartRTP(1)
			continue
		}
		timeouts = 0
		if err != nil {
			// The dialog ending (a BYE either way) closes the media first: run records that
			// end. Only a socket that fails on its own is a transport end.
			select {
			case <-c.done:
			case <-c.dlg.Context().Done():
			case <-time.After(time.Second):
				c.finish(EndTransport, "media: "+err.Error())
			}
			return
		}
		if n == 0 {
			continue
		}
		var pcm []int16
		switch r.PacketHeader.PayloadType {
		case 0:
			pcm = decode(buf[:n], g711.DecodeUlawFrame)
		case 8:
			pcm = decode(buf[:n], g711.DecodeAlawFrame)
		default:
			continue // telephone-event, comfort noise: not audio
		}
		c.lastRTP.Store(time.Now().UnixNano())
		c.startOnce.Do(func() { close(c.started) })
		c.push(pcm)
	}
}

func decode(b []byte, f func(byte) int16) []int16 {
	out := make([]int16, len(b))
	for i, x := range b {
		out[i] = f(x)
	}
	return out
}

// push cuts pcm into frames and queues them for deliver.
func (c *Call) push(pcm []int16) {
	c.mu.Lock()
	c.partial = append(c.partial, pcm...)
	for len(c.partial) >= FrameSamples {
		f := make([]int16, FrameSamples)
		copy(f, c.partial)
		c.partial = c.partial[FrameSamples:]
		c.inbound = append(c.inbound, f)
	}
	if over := len(c.inbound) - maxInbound; over > 0 {
		c.inbound = c.inbound[over:]
	}
	c.mu.Unlock()
	select {
	case c.inReady <- struct{}{}:
	default:
	}
}

// deliver hands queued frames to Frames' reader, in order, and closes Frames at the end.
func (c *Call) deliver() {
	defer close(c.frames)
	for {
		c.mu.Lock()
		var f []int16
		if len(c.inbound) > 0 {
			f = c.inbound[0]
			c.inbound = c.inbound[1:]
		}
		c.mu.Unlock()
		if f == nil {
			select {
			case <-c.inReady:
				continue
			case <-c.done:
				return
			}
		}
		select {
		case c.frames <- f:
		case <-c.done:
			return
		}
	}
}

// pace sends one packet every 20 ms for the whole call: queued playback, a DTMF event, or
// silence. Asterisk (rtptimeout=30) hangs up a leg that goes quiet while the model thinks.
func (c *Call) pace(med *diago.DialogMedia) {
	defer func() {
		if p := recover(); p != nil {
			c.ua.cfg.Log.Error("sip: pacer panicked", "call_id", c.id, "panic", p)
			c.finish(EndTransport, "media failed")
		}
	}()
	w := med.RTPPacketWriter
	t := time.NewTicker(frameDur)
	defer t.Stop()
	frame := make([]int16, FrameSamples)
	payload := make([]byte, FrameSamples)
	marker := true
	var ev *dtmfEvent
	gap := 0
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		c.mu.Lock()
		if ev == nil && gap > 0 {
			gap--
		} else if ev == nil && len(c.dtmf) > 0 {
			ev = &dtmfEvent{digit: c.dtmf[0], pt: uint8(c.dtmfPT)}
			c.dtmf = c.dtmf[1:]
		}
		codec := c.codec
		if ev == nil {
			n := copy(frame, c.queue)
			clear(frame[n:])
			c.queue = c.queue[n:]
			c.advance(c.played + int64(n))
		}
		c.mu.Unlock()
		if ev != nil {
			payload, tsInc, mark, done := ev.next()
			_, _ = w.WriteSamples(payload, tsInc, mark, ev.pt)
			if done {
				ev, gap = nil, dtmfQuiet
			}
			continue
		}
		enc := g711.EncodeUlawFrame
		if codec.PayloadType == 8 {
			enc = g711.EncodeAlawFrame
		}
		for i, s := range frame {
			payload[i] = enc(s)
		}
		_, _ = w.WriteSamples(payload, FrameSamples, marker, codec.PayloadType)
		marker = false
	}
}

// watch ends a call whose far end has sent no RTP for NoRTPTimeout (diago has no RTP timeout).
func (c *Call) watch() {
	t := time.NewTicker(min(time.Second, c.ua.cfg.NoRTPTimeout/4))
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, c.lastRTP.Load())) < c.ua.cfg.NoRTPTimeout {
			continue
		}
		c.ua.cfg.Log.Warn("sip: no audio from the far end — ending the call", "call_id", c.id, "after", c.ua.cfg.NoRTPTimeout)
		detail := fmt.Sprintf("no audio from the far end for %s", c.ua.cfg.NoRTPTimeout)
		c.endWith(EndNoMedia, detail)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = c.dlg.Hangup(ctx)
		cancel()
		c.finish(EndNoMedia, detail)
		return
	}
}

// dtmfEvent is one RFC 4733 digit: 5 packets of 20 ms (100 ms), then 3 end packets; the pacer
// leaves dtmfQuiet ticks of audio before the next digit. All of an event's packets carry its
// start timestamp; the last advances the clock by every tick it used.
type dtmfEvent struct {
	digit rune
	pt    uint8
	tick  int
}

const (
	dtmfTone  = 5
	dtmfEnd   = 3
	dtmfQuiet = 3
)

func dtmfCode(r rune) byte {
	switch {
	case r >= '0' && r <= '9':
		return byte(r - '0')
	case r == '*':
		return 10
	case r == '#':
		return 11
	default:
		return byte(r-'A') + 12
	}
}

// next is the event's next packet: payload, timestamp increment, marker, and whether it was
// the last.
func (e *dtmfEvent) next() ([]byte, uint32, bool, bool) {
	e.tick++
	dur := uint16(min(e.tick, dtmfTone) * FrameSamples)
	p := []byte{dtmfCode(e.digit), 10, byte(dur >> 8), byte(dur)} // volume -10 dBm0
	if e.tick > dtmfTone {
		p[1] |= 0x80 // E: end of event
	}
	total := dtmfTone + dtmfEnd
	if e.tick == total {
		return p, uint32(total * FrameSamples), false, true
	}
	return p, 0, e.tick == 1, false
}
