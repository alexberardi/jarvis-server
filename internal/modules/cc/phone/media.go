package phone

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
)

// The provider's Media Streams WebSocket (gateway main.py /media/{token} and
// services/media_stream.py). Three gates before the upgrade, in order (PRD security
// requirement 2): the single-use path token (claim pops it, so a replay or a duplicate stream
// dies before the handshake), a live call runtime for the token's session, the provider
// signature (X-Twilio-Signature with the https↔wss scheme fix, checked with that session's
// household's auth token, AD6; no signing key rejects everything), and the household still
// having phone on. After the upgrade the stream-start event must bind to the
// claimed session (session_id parameter + callSid) or the socket closes.

const (
	closeBadBinding       = 4403
	finalPlaybackTimeout  = 10 * time.Second
	idleHangupAfterDefers = 2500 * time.Millisecond
)

var upgrader = websocket.Upgrader{
	// The provider is not a browser: there is no Origin to check. The token and signature
	// are the authentication.
	CheckOrigin:     func(*http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

func (s *Service) handleMediaWS(w http.ResponseWriter, r *http.Request) {
	s.init()
	token := r.PathValue("token")
	pending, ok := s.tokens.Claim(token)
	if !ok {
		s.log().Warn("phone: rejected media stream with unknown/replayed token")
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	// The token maps to the session, the session's runtime carries its household's signing
	// key (AD6): a stream signed with another household's token fails here.
	rt := s.runtime(pending.SessionID)
	if rt == nil {
		s.log().Warn("phone: rejected media stream: no live call", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	key := rt.tel.SigningKey
	if key == "" {
		s.log().Error("phone: rejected media stream: no provider signing key configured", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	reqURL := strings.TrimRight(s.Options.PublicURL, "/") + r.URL.Path
	if s.Options.PublicURL == "" {
		reqURL = "https://" + r.Host + r.URL.Path
	}
	if !live.ValidateWSSignature(key, reqURL, r.Header.Get("X-Twilio-Signature")) {
		s.log().Warn("phone: rejected media stream: bad signature", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !s.Enabled(r.Context(), rt.householdID) {
		s.log().Warn("phone: rejected media stream: no live call", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ms := &mediaSession{s: s, conn: conn, rt: rt, pending: *pending, vad: live.NewVAD(live.DefaultVADConfig())}
	defer rt.markDone()
	defer conn.Close()
	// A cancelled call closes the socket, which ends the read loop.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-rt.ctx.Done():
			ms.closeWith(websocket.CloseNormalClosure, "call ended")
		case <-stop:
		}
	}()
	ms.run()
}

// mediaSession is one stream: frames in → VAD → turn pipeline → frames out. Half-duplex: a
// turn runs on the read goroutine, so inbound audio is not processed while the agent speaks.
type mediaSession struct {
	s       *Service
	conn    *websocket.Conn
	rt      *callRuntime
	pending live.PendingSession
	vad     *live.VAD

	wmu          sync.Mutex
	streamSID    string
	speaking     bool
	hangup       bool
	turnNo       int
	marks        map[string]bool
	pendingMark  string
	idleDeadline time.Time
}

func (m *mediaSession) closeWith(code int, text string) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	_ = m.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	_ = m.conn.Close()
}

func (m *mediaSession) send(v any) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	_ = m.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return m.conn.WriteJSON(v)
}

func (m *mediaSession) requestHangup() { m.hangup = true }

// armIdleHangup ends the call if the other party stays silent after a deferred hangup.
func (m *mediaSession) armIdleHangup() { m.idleDeadline = time.Now().Add(idleHangupAfterDefers) }

func (m *mediaSession) run() {
	m.marks = map[string]bool{}
	for {
		_, raw, err := m.conn.ReadMessage()
		if err != nil {
			return
		}
		switch ev := live.ParseWSMessage(raw).(type) {
		case live.StreamStart:
			if !live.ValidateStreamStart(ev, m.pending) {
				m.s.log().Warn("phone: stream-start binding failed — closing", "session", m.rt.sessionID)
				m.closeWith(closeBadBinding, "bad binding")
				return
			}
			m.streamSID = ev.StreamSID
			m.s.log().Info("phone: media stream started", "session", m.rt.sessionID)
			// Validated stream-start: speak the disclosure, then the call is in_call.
			m.rt.markStarted()
			m.speak(m.rt.disclosure)
			if m.hangup {
				m.awaitPlayback()
				return
			}
		case live.InboundAudio:
			if m.streamSID == "" {
				continue
			}
			m.rt.recorder.AddInbound(ev.PCM)
			if !m.idleDeadline.IsZero() && time.Now().After(m.idleDeadline) && !m.speaking {
				m.s.log().Warn("phone: idle after deferred hangup — ending the call", "session", m.rt.sessionID)
				m.hangup = true
				return
			}
			if utt := m.vad.Feed(ev.PCM, m.speaking); utt != nil {
				m.idleDeadline = time.Time{}
				m.runTurn(utt)
				if m.hangup {
					m.awaitPlayback()
					return
				}
			}
		case live.MarkReceived:
			m.marks[ev.Name] = true
		case live.StreamStop:
			m.s.log().Info("phone: media stream stopped", "session", m.rt.sessionID)
			return
		}
	}
}

func (m *mediaSession) runTurn(utt []int16) {
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

// speak sends one PCM buffer to the call, marked so the hang-up can wait for playback.
func (m *mediaSession) speak(pcm []int16) {
	if m.streamSID == "" || len(pcm) == 0 {
		return
	}
	m.speaking = true
	defer func() { m.speaking = false }()
	m.rt.recorder.AddOutbound(pcm)
	for _, msg := range live.MediaMessages(m.streamSID, pcm) {
		if err := m.send(msg); err != nil {
			return
		}
	}
	mark := fmt.Sprintf("t%d", m.turnNo)
	m.pendingMark = mark
	_ = m.send(live.MarkMessage(m.streamSID, mark))
}

// awaitPlayback drains events until the provider confirms the last buffer played (or 10 s),
// so a goodbye is not cut off mid-sentence.
func (m *mediaSession) awaitPlayback() {
	mark := m.pendingMark
	if mark == "" || m.marks[mark] {
		return
	}
	deadline := time.Now().Add(finalPlaybackTimeout)
	_ = m.conn.SetReadDeadline(deadline)
	defer func() { _ = m.conn.SetReadDeadline(time.Time{}) }()
	for {
		_, raw, err := m.conn.ReadMessage()
		if err != nil {
			return
		}
		switch ev := live.ParseWSMessage(raw).(type) {
		case live.MarkReceived:
			m.marks[ev.Name] = true
			if ev.Name == mark {
				return
			}
		case live.InboundAudio:
			m.rt.recorder.AddInbound(ev.PCM)
		case live.StreamStop:
			return
		}
	}
}
