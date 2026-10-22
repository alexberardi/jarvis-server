package phone

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The call loop over a transport-neutral fake Call (no WebSocket, no Twilio): what any
// provider, SIP included, must give the loop.

type fakeLine struct {
	dialed chan *fakeCall
	ready  error
}

func newFakeLine() *fakeLine { return &fakeLine{dialed: make(chan *fakeCall, 4)} }

func (f *fakeLine) Ready() error { return f.ready }

func (f *fakeLine) Dial(_ context.Context, req DialRequest) (Call, error) {
	c := newFakeCall(req)
	f.dialed <- c
	return c, nil
}

func (f *fakeLine) LineType(context.Context, string) string { return "unknown" }

type fakePlay struct {
	cue     string
	samples int
}

// fakeCall plays back only when the test says so (advance). Its frames are fed by one pump
// goroutine, the only sender and closer of the channel.
type fakeCall struct {
	req     DialRequest
	frames  chan []int16
	feed    chan []int16
	started chan struct{}
	done    chan struct{}

	startOnce, closeOnce sync.Once

	mu      sync.Mutex
	plays   []fakePlay
	queued  int64
	pos     int64
	changed chan struct{}
	hangups int
	flushes int
	reason  EndReason
}

func newFakeCall(req DialRequest) *fakeCall {
	c := &fakeCall{req: req, frames: make(chan []int16), feed: make(chan []int16, 4096),
		started: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{})}
	go func() {
		defer close(c.frames)
		for {
			select {
			case pcm := <-c.feed:
				select {
				case c.frames <- pcm:
				case <-c.done:
					return
				}
			case <-c.done:
				return
			}
		}
	}()
	return c
}

func (c *fakeCall) ID() string               { return "CALL-" + c.req.SessionID[:8] }
func (c *fakeCall) Started() <-chan struct{} { return c.started }
func (c *fakeCall) Frames() <-chan []int16   { return c.frames }
func (c *fakeCall) Done() <-chan struct{}    { return c.done }
func (c *fakeCall) SendDTMF(string) error    { return ErrUnsupported }

func (c *fakeCall) Play(pcm []int16, cue string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.plays = append(c.plays, fakePlay{cue: cue, samples: len(pcm)})
	c.queued += int64(len(pcm))
	return c.queued, nil
}

// Flush drops what is queued the way Twilio's clear does: playback jumps to the end of it
// (its marks are echoed at once), so a later Play's end stays on the same scale.
func (c *fakeCall) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushes++
	c.pos = c.queued
	close(c.changed)
	c.changed = make(chan struct{})
	return nil
}

func (c *fakeCall) flushed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flushes
}

func (c *fakeCall) Played() (int64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pos, c.changed
}

func (c *fakeCall) EndReason() EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *fakeCall) Hangup(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hangups++
	return nil
}

func (c *fakeCall) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.reason = EndClosed
		c.mu.Unlock()
		close(c.done)
	})
}

func (c *fakeCall) answer() { c.startOnce.Do(func() { close(c.started) }) }

// advance plays everything queued so far.
func (c *fakeCall) advance() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pos = c.queued
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *fakeCall) played() []fakePlay {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]fakePlay(nil), c.plays...)
}

func (c *fakeCall) hungUp() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hangups
}

func (c *fakeCall) ended() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// say plays one far-end utterance: speech, then enough silence for the VAD to endpoint.
func (c *fakeCall) say() { c.sayAt(4000, 10) }

// sayAt plays frames 20 ms frames of a tone at amplitude amp, then silence.
func (c *fakeCall) sayAt(amp float64, frames int) {
	tone := make([]int16, 160)
	for i := range tone {
		tone[i] = int16(amp * math.Sin(2*math.Pi*440*float64(i)/8000))
	}
	for i := 0; i < frames; i++ {
		c.feed <- tone
	}
	for i := 0; i < 45; i++ {
		c.feed <- make([]int16, 160)
	}
}

func (e *env) neutralLine() *fakeLine {
	f := newFakeLine()
	e.s.Provider = f
	return f
}

func nextCall(t *testing.T, f *fakeLine) *fakeCall {
	t.Helper()
	select {
	case c := <-f.dialed:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no dial")
	}
	return nil
}

func TestCallLoopOverANeutralCall(t *testing.T) {
	e := newEnv(t)
	line := e.neutralLine()
	e.stt.heard = []string{"Thanks for calling Tony's, how can I help?", "Sure, a large pie, ready in 20 minutes. Goodbye!"}
	e.llm.replies = []string{
		"Hi, I'd like to order a large cheese pie for pickup. ",
		"Great, thank you! [OUTCOME: large cheese pie ready in 20 minutes] Goodbye. [HANGUP]",
	}
	e.llm.assess = `{"summary": "Ordered a large pie.", "goal_achieved": true}`
	id := e.confirmed(t)
	c := nextCall(t, line)
	if c.req.SessionID != id || c.req.HouseholdID != hh || c.req.To != "+17325924183" {
		t.Fatalf("dial request: %+v", c.req)
	}
	// Nothing is spoken before the call is answered.
	time.Sleep(50 * time.Millisecond)
	if len(c.played()) != 0 || e.session(id).State == StateInCall {
		t.Fatalf("played before answer: %v", c.played())
	}
	c.answer()
	waitFor(t, "disclosure", func() bool { return len(c.played()) == 1 })
	if p := c.played()[0]; p.cue != "t0" || p.samples == 0 {
		t.Fatalf("disclosure play: %+v", p)
	}
	if s := e.waitState(t, id, StateInCall); s.CallID != c.ID() {
		t.Fatalf("call id: %q", s.CallID)
	}

	c.say()
	waitFor(t, "first reply", func() bool { return len(c.played()) == 2 })
	c.say()
	waitFor(t, "goodbye", func() bool { return len(c.played()) == 3 })
	if p := c.played(); p[1].cue != "t1" || p[2].cue != "t2" {
		t.Fatalf("cues: %+v", p)
	}
	// The hang-up waits for the goodbye to play.
	time.Sleep(100 * time.Millisecond)
	if c.ended() {
		t.Fatal("call closed before the goodbye played")
	}
	c.advance()
	waitFor(t, "media closed", c.ended)
	s := e.waitState(t, id, StateDone)
	e.s.Wait()
	var turns []map[string]any
	if err := json.Unmarshal([]byte(s.TranscriptJSON), &turns); err != nil || len(turns) != 2 {
		t.Fatalf("transcript: %s", s.TranscriptJSON)
	}
	if c.hungUp() != 0 {
		t.Fatal("an agent hang-up closes the media; it needs no provider hang-up")
	}
}

func TestNeutralCallCancelHangsUp(t *testing.T) {
	e := newEnv(t)
	line := e.neutralLine()
	e.llm.assess = `{"summary": "Nothing happened.", "goal_achieved": false}`
	id := e.confirmed(t)
	c := nextCall(t, line)
	c.answer()
	e.waitState(t, id, StateInCall)
	if r := e.s.CancelCall(context.Background(), CallbackContext{HouseholdID: hh, UserID: 1,
		Data: map[string]any{"session_id": id}}); !r.Success {
		t.Fatalf("cancel: %+v", r)
	}
	waitFor(t, "media closed", c.ended)
	e.s.Wait()
	if c.hungUp() == 0 {
		t.Fatal("not hung up at the provider")
	}
}

func TestNeutralCallNeverAnswered(t *testing.T) {
	e := newEnv(t)
	e.s.StreamStartTimeout = 200 * time.Millisecond
	line := e.neutralLine()
	id := e.confirmed(t)
	c := nextCall(t, line)
	if s := e.waitState(t, id, StateFailed); s.ErrorMessage != "no media stream within 60s (no answer?)" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	e.s.Wait()
	if c.hungUp() != 1 || !c.ended() {
		t.Fatalf("unanswered call: hangups=%d closed=%v", c.hungUp(), c.ended())
	}
}

func TestProviderNotReadyFailsBeforeTheDisclosure(t *testing.T) {
	e := newEnv(t)
	line := e.neutralLine()
	line.ready = errors.New("not registered to the PBX")
	id := e.confirmed(t)
	if s := e.waitState(t, id, StateFailed); s.ErrorMessage != "not registered to the PBX" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	if len(e.tts.spoken()) != 0 || len(line.dialed) != 0 {
		t.Fatalf("spent on a call that couldn't be placed: tts=%v dialed=%d", e.tts.spoken(), len(line.dialed))
	}
}

// Twilio without a public URL for its media stream fails the call before anything is spent,
// with the same reason as before the provider seam.
func TestTwilioNeedsAPublicURL(t *testing.T) {
	e := newEnv(t)
	e.s.Options.PublicURL = ""
	id := e.confirmed(t)
	if s := e.waitState(t, id, StateFailed); s.ErrorMessage != "no public URL is configured for the call's media stream" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	if len(e.tts.spoken()) != 0 || len(e.provider.started) != 0 {
		t.Fatal("spent on a call that couldn't be placed")
	}
}

func TestTwilioDialFailure(t *testing.T) {
	e := newEnv(t)
	e.provider.failDial = true
	id := e.confirmed(t)
	if s := e.waitState(t, id, StateFailed); s.ErrorMessage != "calls.create failed: account suspended" {
		t.Fatalf("reason: %q", s.ErrorMessage)
	}
	e.s.Wait()
	if e.s.twilio.call(id) != nil {
		t.Fatal("a failed dial left its call registered")
	}
}

// Twilio's playback position follows its mark echoes, oldest first: two buffers cued with the
// same name (an escalation's hold line and the reply after it) are told apart.
func TestTwilioPlaybackFollowsMarks(t *testing.T) {
	c := newTwilioCall(&twilioProvider{}, DialRequest{SessionID: "s"})
	c.cues = []twilioCue{{"t1", 100}, {"t1", 250}, {"t2", 400}}
	if pos, _ := c.Played(); pos != 0 {
		t.Fatalf("start: %d", pos)
	}
	_, changed := c.Played()
	c.marked("t1")
	select {
	case <-changed:
	default:
		t.Fatal("no change signalled")
	}
	if pos, _ := c.Played(); pos != 100 {
		t.Fatalf("after the first t1: %d", pos)
	}
	c.marked("nope")
	c.marked("t1")
	if pos, _ := c.Played(); pos != 250 {
		t.Fatalf("after the second t1: %d", pos)
	}
	c.marked("t2")
	if pos, _ := c.Played(); pos != 400 || len(c.cues) != 0 {
		t.Fatalf("after t2: %d %v", pos, c.cues)
	}
	if c.SendDTMF("1") != ErrUnsupported {
		t.Fatal("Twilio streams send no DTMF")
	}
}

func TestProviderSetting(t *testing.T) {
	e := newEnv(t)
	a := e.perHousehold()
	ctx := context.Background()
	e.setCreds(settings.Scope{}, "AC-sys", "token-sys", "+15550000009")

	def, ok := e.s.Settings.Definition(SettingProvider)
	if !ok || def.Default != ProviderTwilio || len(def.Options) != 2 || def.Options[0] != ProviderTwilio || def.Options[1] != ProviderSIP {
		t.Fatalf("definition: %+v", def)
	}
	// Default and explicit twilio resolve the Twilio account.
	for _, v := range []any{nil, "twilio", " Twilio "} {
		if v != nil {
			e.set(SettingProvider, v, settings.Scope{HouseholdID: hh})
		}
		if tel, err := e.s.Telephony(ctx, hh); err != nil || twilioAPI(tel) != a.fake("AC-sys") {
			t.Fatalf("provider %v: %+v %v", v, tel, err)
		}
	}
	// Only known providers can be written (sip is TestProviderSettingAcceptsSIP's).
	for _, v := range []any{"carrier-pigeon", int64(3)} {
		err := e.s.Settings.Set(ctx, SettingProvider, v, settings.Scope{HouseholdID: hh})
		if !errors.Is(err, settings.ErrInvalidValue) {
			t.Fatalf("%v accepted: %v", v, err)
		}
	}
	// A value this build doesn't know (written before a downgrade) is "not configured", in
	// words that don't blame Twilio.
	if _, err := e.d.Write.Exec(`UPDATE cc_settings SET value = 'carrier-pigeon' WHERE key = ? AND household_id = ?`,
		SettingProvider, hh); err != nil {
		t.Fatal(err)
	}
	_, err := e.s.Telephony(ctx, hh)
	var unk *UnknownProviderError
	if !errors.As(err, &unk) || unk.Provider != "carrier-pigeon" || !isNotConfigured(err) {
		t.Fatalf("unknown provider: %v", err)
	}
	if msg := NotConfiguredMessage(err); strings.Contains(msg, "Twilio") || !strings.Contains(msg, "phone provider") {
		t.Fatalf("message: %s", msg)
	}
	// Another household still gets the system default.
	if tel, err := e.s.Telephony(ctx, otherHH); err != nil || twilioAPI(tel) != a.fake("AC-sys") {
		t.Fatalf("other household: %+v %v", tel, err)
	}
}
