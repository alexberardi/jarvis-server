package phone

import (
	"fmt"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
)

// The call loop (gateway services/media_stream.py), transport-neutral: a Call's frames in →
// VAD → turn pipeline → PCM out through Call.Play. A turn runs on the loop's goroutine, so
// inbound audio is not processed while the agent thinks; the transport holds it back
// meanwhile (Call.Frames). While the agent's audio plays (Call.Played behind the last Play),
// the far end can barge in: speech louder and longer than our own echo flushes the queued
// audio (Call.Flush) and gets the next turn. The disclosure and the goodbye are never cut
// off (docs/decisions/2026-10-21-phone-barge-in.md).

const (
	finalPlaybackTimeout  = 10 * time.Second
	idleHangupAfterDefers = 2500 * time.Millisecond
)

// callLine is what a turn (turn.go, runEscalation) may do to the call it runs on.
type callLine interface {
	// speak plays one PCM buffer to the far end now (a hold line mid-turn).
	speak(pcm []int16)
	// requestHangup ends the call once the turn's reply has played.
	requestHangup()
	// armIdleHangup ends the call if the far end stays silent after a deferred hangup.
	armIdleHangup()
	// cutOff reports whether this turn's utterance barged in on the agent's last reply.
	cutOff() bool
}

// runMedia runs one call's loop until its media ends or the call is cancelled, then releases
// the media; rt.done says it is over.
func (s *Service) runMedia(rt *callRuntime, call Call) {
	defer rt.markDone()
	defer call.Close()
	// A cancelled call closes the media, which ends the loop.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-rt.ctx.Done():
			call.Close()
		case <-stop:
		}
	}()
	m := &callSession{s: s, call: call, rt: rt, vad: live.NewVAD(live.DefaultVADConfig())}
	m.run()
}

// callSession is one call's loop state; only runMedia's goroutine touches it.
type callSession struct {
	s    *Service
	call Call
	rt   *callRuntime
	vad  *live.VAD

	started       bool
	hangup        bool
	bargedIn      bool // the utterance in progress cut the agent's reply off
	turnNo        int
	playEnd       int64 // where the last buffer played ends
	disclosureEnd int64 // where the disclosure ends; nothing before it is flushed
	idleDeadline  time.Time
}

var _ callLine = (*callSession)(nil)

func (m *callSession) requestHangup() { m.hangup = true }

func (m *callSession) armIdleHangup() { m.idleDeadline = time.Now().Add(idleHangupAfterDefers) }

func (m *callSession) cutOff() bool { return m.bargedIn }

func (m *callSession) run() {
	select {
	case <-m.call.Started():
	case <-m.call.Done():
		return
	}
	m.started = true
	// Answered: speak the disclosure, then the call is in_call.
	m.rt.markStarted()
	m.speak(m.rt.disclosure)
	m.disclosureEnd = m.playEnd
	if m.hangup {
		m.awaitPlayback()
		return
	}
	for pcm := range m.call.Frames() {
		m.rt.recorder.AddInbound(pcm)
		pos, _ := m.call.Played()
		playing := pos < m.playEnd
		if !m.idleDeadline.IsZero() && time.Now().After(m.idleDeadline) && !playing {
			m.s.log().Warn("phone: idle after deferred hangup — ending the call", "session", m.rt.sessionID)
			m.hangup = true
			return
		}
		wasSpeech := m.vad.InSpeech()
		utt := m.vad.Feed(pcm, playing)
		if playing && !wasSpeech && m.vad.InSpeech() {
			m.bargeIn(pos)
		}
		if utt != nil {
			m.idleDeadline = time.Time{}
			m.runTurn(utt)
			m.bargedIn = false
			if m.hangup {
				m.awaitPlayback()
				return
			}
		}
	}
}

func (m *callSession) runTurn(utt []int16) {
	m.turnNo++
	defer func() {
		if p := recover(); p != nil {
			m.s.log().Error("phone: turn failed — staying on the line", "session", m.rt.sessionID, "panic", p)
		}
	}()
	reply := m.rt.pipeline.turn(m.rt.ctx, utt, m)
	if len(reply) > 0 {
		m.speak(reply)
	}
}

// bargeIn: the far end started talking over the agent at playback position pos. What has
// not been heard yet is dropped, unless the disclosure is still playing: it is a hard gate,
// so the far end is answered after it instead.
func (m *callSession) bargeIn(pos int64) {
	if pos < m.disclosureEnd {
		return
	}
	if err := m.call.Flush(); err != nil {
		m.s.log().Warn("phone: barge-in flush failed", "session", m.rt.sessionID, "err", err)
		return
	}
	m.s.log().Info("phone: barge-in — the far end cut the reply off", "session", m.rt.sessionID)
	m.playEnd = pos
	m.bargedIn = true
}

// speak plays one PCM buffer, cued "t<turn>" so the hang-up can wait for its playback.
func (m *callSession) speak(pcm []int16) {
	if !m.started || len(pcm) == 0 {
		return
	}
	m.rt.recorder.AddOutbound(pcm)
	end, err := m.call.Play(pcm, fmt.Sprintf("t%d", m.turnNo))
	if err != nil {
		return
	}
	m.playEnd = end
}

// awaitPlayback waits until the last buffer has played (or 10 s, or the media ends), still
// recording the far end, so a goodbye is not cut off mid-sentence.
func (m *callSession) awaitPlayback() {
	deadline := time.NewTimer(finalPlaybackTimeout)
	defer deadline.Stop()
	frames := m.call.Frames()
	for {
		pos, changed := m.call.Played()
		if pos >= m.playEnd {
			return
		}
		select {
		case <-changed:
		case pcm, ok := <-frames:
			if !ok {
				return
			}
			m.rt.recorder.AddInbound(pcm)
		case <-deadline.C:
			return
		}
	}
}
