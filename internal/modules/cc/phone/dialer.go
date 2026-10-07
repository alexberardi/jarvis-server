package phone

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
)

// The absorbed dial worker (gateway services/dial_worker.py), in process (D16): claim CAS →
// disclosure prewarm (no disclosure, no dial) → single-use stream token → TwiML → place the
// call → wait for the media stream → in_call → heartbeat and watchdogs → wrap-up assessment,
// recording, outcome. No Redis: the hand-off is a goroutine, and the confirmed → dialing CAS
// stays the only authorisation to dial. A crash between confirm and claim leaves the session
// confirmed; the reaper fails it after 5 minutes.

// MediaPath is where the provider's media WebSocket connects: MediaPath + <single-use token>.
// It is the only route a public tunnel needs to expose.
const MediaPath = "/phone/media/"

const (
	defaultStreamStart = 60 * time.Second
	defaultHeartbeat   = 25 * time.Second
	defaultTurnTimeout = 20 * time.Second
	hangupGrace        = 10 * time.Second
	recordingPrefix    = "phone-calls/"
)

// callRuntime is one live call's in-process state (the gateway's CallRuntime).
type callRuntime struct {
	sessionID   string
	householdID string
	brief       live.Brief
	maxSeconds  int64
	escalation  *live.EscalationWindow
	recorder    *live.Recorder
	disclosure  []int16
	pipeline    *turnPipeline
	ctx         context.Context
	cancel      context.CancelFunc
	// tel is the household's provider and signing key, resolved once at dial time.
	tel Telephony

	started     chan struct{}
	startOnce   sync.Once
	done        chan struct{}
	doneOnce    sync.Once
	mu          sync.Mutex
	callSID     string
	cancelled   bool
	cancelCause string
}

func (rt *callRuntime) markStarted() { rt.startOnce.Do(func() { close(rt.started) }) }
func (rt *callRuntime) markDone()    { rt.doneOnce.Do(func() { close(rt.done) }) }

func (rt *callRuntime) sid() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.callSID
}

func (s *Service) runtime(id string) *callRuntime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimes[id]
}

// cancelRuntime stops a live call (Cancel is context cancellation, D16).
func (s *Service) cancelRuntime(id string) {
	if rt := s.runtime(id); rt != nil {
		rt.mu.Lock()
		rt.cancelled = true
		rt.mu.Unlock()
		rt.cancel()
	}
}

// enqueueDial resolves the household's telephony (AD6) and hands a confirmed session to the
// dialer, which keeps that provider and signing key for the whole call.
func (s *Service) enqueueDial(ctx context.Context, sess *Session) error {
	tel, err := s.Telephony(ctx, sess.HouseholdID)
	if err != nil {
		return err
	}
	s.init()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runCall(sess.ID, tel)
	}()
	return nil
}

func (s *Service) wssBase() string {
	if s.Options.PublicWSSURL != "" {
		return strings.TrimRight(s.Options.PublicWSSURL, "/")
	}
	u := strings.TrimRight(s.Options.PublicURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + u[len("https://"):]
	case strings.HasPrefix(u, "http://"):
		return "ws://" + u[len("http://"):]
	}
	return u
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// runCall drives one call from claim to outcome.
func (s *Service) runCall(id string, tel Telephony) {
	base := s.baseCtx()
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	// Writes that must land even when the call is cancelled or jarvisd is stopping.
	bg := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(base), 90*time.Second)
	}

	ok, err := s.claimDial(ctx, id)
	if err != nil || !ok {
		s.log().Info("phone: session not claimable — dropping dial", "session", id, "err", err)
		return
	}
	sess, err := s.session(ctx, s.DB.Read, id)
	if err != nil {
		return
	}
	fail := func(reason string) {
		wctx, wcancel := bg()
		defer wcancel()
		s.log().Error("phone: call failed", "session", id, "reason", reason)
		if _, err := s.setState(wctx, id, StateFailed, reason, ""); err != nil {
			s.log().Error("phone: could not record failure", "session", id, "err", err)
		}
	}
	if sess.DialedNumber == "" {
		fail("session has no dialed_number")
		return
	}
	wss := s.wssBase()
	if wss == "" {
		fail("no public URL is configured for the call's media stream")
		return
	}
	brief := live.Brief{Goal: sess.Goal, Details: sess.Details,
		Constraints: ExtractConstraintEnvelope(sess.Details), InitiatorName: s.initiatorName(ctx, sess.UserID)}

	// Prewarm, and a hard gate: no disclosure, no dial.
	disclosure, _ := s.synthesize(ctx, live.BuildDisclosure(brief))
	if len(disclosure) == 0 {
		fail("disclosure synthesis failed (TTS down?)")
		return
	}

	rt := &callRuntime{
		sessionID: id, householdID: sess.HouseholdID, brief: brief,
		maxSeconds: s.intSetting(ctx, SettingMaxCallSeconds, sess.HouseholdID, 600),
		escalation: &live.EscalationWindow{Timeout: s.EscalationWindow}, tel: tel,
		recorder:   &live.Recorder{}, disclosure: disclosure, ctx: ctx, cancel: cancel,
		started: make(chan struct{}), done: make(chan struct{}),
	}
	rt.pipeline = s.newTurnPipeline(rt, s.restrictedFor(ctx, sess))
	s.mu.Lock()
	s.runtimes[id] = rt
	s.mu.Unlock()
	token := s.tokens.Issue(id)
	defer func() {
		s.tokens.Revoke(token)
		s.mu.Lock()
		delete(s.runtimes, id)
		s.mu.Unlock()
	}()

	twiml := live.StreamTwiML(wss+MediaPath+token, [][2]string{{"session_id", id}})
	sctx, scancel := context.WithTimeout(ctx, 15*time.Second)
	callSID, err := tel.Provider.StartCall(sctx, sess.DialedNumber, twiml)
	scancel()
	if err != nil {
		fail("calls.create failed: " + err.Error())
		return
	}
	rt.mu.Lock()
	rt.callSID = callSID
	rt.mu.Unlock()
	s.tokens.BindCallSID(token, callSID)
	s.log().Info("phone: dialing", "session", id, "call_sid", callSID)

	select {
	case <-rt.started:
	case <-time.After(orDefault(s.StreamStartTimeout, defaultStreamStart)):
		s.endCallQuietly(tel.Provider, callSID)
		fail("no media stream within 60s (no answer?)")
		return
	case <-ctx.Done():
		// Cancelled before the stream: the canceller already marked the session.
		s.endCallQuietly(tel.Provider, callSID)
		return
	}
	if ok, _ := s.setState(ctx, id, StateInCall, "", callSID); !ok {
		// The session moved on (cancelled or reaped) while ringing: hang up.
		cancel()
		s.endCallQuietly(tel.Provider, callSID)
		<-s.waitDone(rt, hangupGrace)
		return
	}

	s.supervise(ctx, rt)
	wctx, wcancel := bg()
	defer wcancel()
	s.wrapup(wctx, rt, sess)
}

// waitDone returns a channel closed when the media stream ended or after grace.
func (s *Service) waitDone(rt *callRuntime, grace time.Duration) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		defer close(out)
		select {
		case <-rt.done:
		case <-time.After(grace):
		}
	}()
	return out
}

// supervise heartbeats every 25 s until the stream ends, enforcing max_call_seconds, the
// gate (turning phone off ends live calls, D40 11.Q3) and cancellation.
func (s *Service) supervise(ctx context.Context, rt *callRuntime) {
	hb := time.NewTicker(orDefault(s.HeartbeatInterval, defaultHeartbeat))
	defer hb.Stop()
	limit := time.NewTimer(time.Duration(rt.maxSeconds) * time.Second)
	defer limit.Stop()
	for {
		select {
		case <-rt.done:
			return
		case <-ctx.Done():
			s.endCallQuietly(rt.tel.Provider, rt.sid())
			<-s.waitDone(rt, hangupGrace)
			return
		case <-limit.C:
			s.log().Warn("phone: max_call_seconds reached — ending call", "session", rt.sessionID)
			s.endCallQuietly(rt.tel.Provider, rt.sid())
			<-s.waitDone(rt, hangupGrace)
			// The provider should have stopped the stream; if not, close it ourselves so the
			// wrap-up never runs alongside a live turn.
			rt.cancel()
			<-s.waitDone(rt, hangupGrace)
			return
		case <-hb.C:
			if !s.Enabled(ctx, rt.householdID) {
				s.log().Warn("phone: phone calls turned off mid-call — ending", "session", rt.sessionID)
				if sess, err := s.session(ctx, s.DB.Read, rt.sessionID); err == nil {
					s.failAndCancel(context.WithoutCancel(ctx), sess, "phone calls were turned off")
				}
				continue // ctx is now cancelled; the next select hangs up
			}
			s.heartbeat(ctx, rt.sessionID)
		}
	}
}

func (s *Service) endCallQuietly(p Provider, callSID string) {
	if callSID == "" || p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), 10*time.Second)
	defer cancel()
	if err := p.EndCall(ctx, callSID); err != nil {
		s.log().Error("phone: hang-up failed", "call_sid", callSID, "err", err)
	}
}

// wrapup is the post-call phase: wrapup state, assessment, recording, outcome (which lands
// the session done, or, after a cancel or reap, stores the late outcome per D40 11.Q11).
func (s *Service) wrapup(ctx context.Context, rt *callRuntime, sess *Session) {
	_, _ = s.setState(ctx, rt.sessionID, StateWrapup, "", "")
	p := rt.pipeline
	summary, achieved := s.assess(ctx, p, sess.Goal)
	audioKey := ""
	if s.Blobs != nil {
		key := recordingPrefix + sess.HouseholdID + "/" + rt.sessionID + ".wav"
		if _, err := s.Blobs.Put(ctx, key, bytes.NewReader(rt.recorder.WAV()), "audio/wav"); err != nil {
			s.log().Error("phone: recording upload failed", "session", rt.sessionID, "err", err)
		} else {
			audioKey = key
		}
	}
	var ga any
	if achieved != nil {
		ga = *achieved
	}
	facts := make([]any, 0, len(p.outcomeFacts))
	for _, f := range p.outcomeFacts {
		facts = append(facts, f)
	}
	outcome := map[string]any{
		"summary": summary, "goal_achieved": ga, "facts": facts, "turns": len(p.records),
		"escalation_unanswered": p.escalationUnanswered, "audio_available": audioKey != "",
	}
	if err := s.recordOutcome(ctx, rt.sessionID, outcome, audioKey); err != nil {
		s.log().Error("phone: outcome report failed", "session", rt.sessionID, "err", err)
	}
}

// restrictedFor recomputes the give-if-asked fields from the stored context with the same
// selection the brief used, so the guard's denylist covers exactly what the brief loaded.
func (s *Service) restrictedFor(ctx context.Context, sess *Session) []live.RestrictedField {
	var out []live.RestrictedField
	for _, f := range RestrictedFields(SelectForCall(s.loadCallContext(ctx, sess.UserID))) {
		out = append(out, live.RestrictedField{Key: f.Key, Label: f.Label, Value: f.Value})
	}
	return out
}
