package phone

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone/live"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Twilio's Media Streams WebSocket (gateway main.py /media/{token} and
// services/media_stream.py), the Twilio provider's transport. Four gates before the upgrade,
// in order (PRD security requirement 2): the single-use path token (claim pops it, so a replay
// or a duplicate stream dies before the handshake), a live call for the token's session, the
// provider signature (X-Twilio-Signature with the https↔wss scheme fix, checked with the
// call's account's auth token, AD6; no signing key rejects everything), and the household
// still having phone on. After the upgrade the stream-start event must bind to the claimed
// session (session_id parameter + callSid) or the socket closes.

const closeBadBinding = 4403

var upgrader = websocket.Upgrader{
	// The provider is not a browser: there is no Origin to check. The token and signature
	// are the authentication.
	CheckOrigin:     func(*http.Request) bool { return true },
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// twilioMedia is the media endpoint every Twilio call shares: the token registry and the
// calls waiting for, or carrying, a stream. It reads the public URL, the log and the phone
// gate from the Service.
type twilioMedia struct {
	s      *Service
	tokens *live.TokenRegistry

	mu    sync.Mutex
	calls map[string]*twilioCall // by session id
}

func newTwilioMedia(s *Service) *twilioMedia {
	return &twilioMedia{s: s, tokens: &live.TokenRegistry{}, calls: map[string]*twilioCall{}}
}

// register issues c's stream token and makes c the session's live call.
func (h *twilioMedia) register(c *twilioCall) string {
	c.token = h.tokens.Issue(c.req.SessionID)
	h.mu.Lock()
	h.calls[c.req.SessionID] = c
	h.mu.Unlock()
	return c.token
}

// unregister revokes c's token and drops it, unless a newer call took the session.
func (h *twilioMedia) unregister(c *twilioCall) {
	h.tokens.Revoke(c.token)
	h.mu.Lock()
	if h.calls[c.req.SessionID] == c {
		delete(h.calls, c.req.SessionID)
	}
	h.mu.Unlock()
}

func (h *twilioMedia) call(sessionID string) *twilioCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[sessionID]
}

func (h *twilioMedia) wssBase() string {
	o := h.s.Options
	if o.PublicWSSURL != "" {
		return strings.TrimRight(o.PublicWSSURL, "/")
	}
	u := strings.TrimRight(o.PublicURL, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		return "wss://" + u[len("https://"):]
	case strings.HasPrefix(u, "http://"):
		return "ws://" + u[len("http://"):]
	}
	return u
}

// signatureURL is the URL Twilio requested and signed: the public base the stream URL was
// built from (on a single-port server it ends in /cc), else this host plus the mount prefix
// the request came in under.
func (h *twilioMedia) signatureURL(r *http.Request) string {
	if h.s.Options.PublicURL != "" {
		return strings.TrimRight(h.s.Options.PublicURL, "/") + r.URL.Path
	}
	return "https://" + r.Host + httpx.MountPrefix(r) + r.URL.Path
}

func (h *twilioMedia) handleWS(w http.ResponseWriter, r *http.Request) {
	log := h.s.log()
	token := r.PathValue("token")
	pending, ok := h.tokens.Claim(token)
	if !ok {
		log.Warn("phone: rejected media stream with unknown/replayed token")
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	// The token maps to the session, the session's call carries its account's signing key
	// (AD6): a stream signed with another household's token fails here.
	c := h.call(pending.SessionID)
	if c == nil {
		log.Warn("phone: rejected media stream: no live call", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	key := c.p.signingKey
	if key == "" {
		log.Error("phone: rejected media stream: no provider signing key configured", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !live.ValidateWSSignature(key, h.signatureURL(r), r.Header.Get("X-Twilio-Signature")) {
		log.Warn("phone: rejected media stream: bad signature", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !h.s.Enabled(r.Context(), c.req.HouseholdID) {
		log.Warn("phone: rejected media stream: no live call", "session", pending.SessionID)
		detail(w, http.StatusForbidden, "Forbidden")
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.serve(conn, *pending)
}

// twilioCall is a Twilio call: the REST call SID, and the media stream once it connects.
// serve (the WebSocket's handler goroutine) reads; the call loop writes (Play, Flush).
type twilioCall struct {
	p     *twilioProvider
	req   DialRequest
	token string

	frames  chan []int16
	started chan struct{}
	done    chan struct{}
	closed  chan struct{}

	startOnce sync.Once
	closeOnce sync.Once
	doneOnce  sync.Once

	mu        sync.Mutex
	sid       string
	conn      *websocket.Conn
	streamSID string
	reason    EndReason
	// Playback: queued is where the last queued buffer ends, cues the marks Twilio has yet to
	// echo (in order), played the end of the last echoed one.
	queued  int64
	cues    []twilioCue
	played  int64
	changed chan struct{}

	wmu sync.Mutex // one writer on the socket at a time
}

type twilioCue struct {
	name string
	end  int64
}

func newTwilioCall(p *twilioProvider, req DialRequest) *twilioCall {
	return &twilioCall{p: p, req: req, frames: make(chan []int16), started: make(chan struct{}),
		done: make(chan struct{}), closed: make(chan struct{}), changed: make(chan struct{})}
}

func (c *twilioCall) ID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sid
}

func (c *twilioCall) Started() <-chan struct{} { return c.started }
func (c *twilioCall) Frames() <-chan []int16   { return c.frames }
func (c *twilioCall) Done() <-chan struct{}    { return c.done }

func (c *twilioCall) EndReason() EndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

// attach makes conn the call's stream; false once the call is closed.
func (c *twilioCall) attach(conn *websocket.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return false
	default:
	}
	c.conn = conn
	return true
}

// serve reads the stream until it stops, fails its binding or is closed. Half-duplex by
// back-pressure: while the call loop runs a turn it does not read Frames, so this blocks and
// the far end's audio waits in the socket, as it did when the turn ran on this goroutine.
func (c *twilioCall) serve(conn *websocket.Conn, pending live.PendingSession) {
	log := c.p.media.s.log()
	if !c.attach(conn) {
		_ = conn.Close()
		return
	}
	reason := EndTransport
	defer func() { c.finish(reason) }()
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch ev := live.ParseWSMessage(raw).(type) {
		case live.StreamStart:
			if !live.ValidateStreamStart(ev, pending) {
				log.Warn("phone: stream-start binding failed — closing", "session", c.req.SessionID)
				c.closeWith(closeBadBinding, "bad binding")
				reason = EndRejected
				return
			}
			c.mu.Lock()
			c.streamSID = ev.StreamSID
			c.mu.Unlock()
			log.Info("phone: media stream started", "session", c.req.SessionID)
			c.startOnce.Do(func() { close(c.started) })
		case live.InboundAudio:
			if c.stream() == "" {
				continue
			}
			select {
			case c.frames <- ev.PCM:
			case <-c.closed:
				return
			}
		case live.MarkReceived:
			c.marked(ev.Name)
		case live.StreamStop:
			log.Info("phone: media stream stopped", "session", c.req.SessionID)
			reason = EndRemote
			return
		}
	}
}

// finish ends the media once: the socket closes, Frames closes, Done fires.
func (c *twilioCall) finish(reason EndReason) {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		conn := c.conn
		select {
		case <-c.closed:
			reason = EndClosed
		default:
		}
		c.reason = reason
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		close(c.frames)
		close(c.done)
	})
}

func (c *twilioCall) stream() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streamSID
}

func (c *twilioCall) send(v any) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errNotStreaming
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(v)
}

func (c *twilioCall) closeWith(code int, text string) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	_ = conn.Close()
}

var errNotStreaming = errors.New("phone: no media stream")

// Play sends pcm as 20 ms media frames (Twilio paces playback), then a mark named cue that
// Twilio echoes once it has played.
func (c *twilioCall) Play(pcm []int16, cue string) (int64, error) {
	sid := c.stream()
	if sid == "" {
		return 0, errNotStreaming
	}
	for _, msg := range live.MediaMessages(sid, pcm) {
		if err := c.send(msg); err != nil {
			return 0, err
		}
	}
	// The cue is queued before the mark goes out, so its echo always finds it.
	c.mu.Lock()
	c.queued += int64(len(pcm))
	end := c.queued
	c.cues = append(c.cues, twilioCue{name: cue, end: end})
	c.mu.Unlock()
	_ = c.send(live.MarkMessage(sid, cue))
	return end, nil
}

// marked advances playback to the oldest unechoed cue of that name (Twilio echoes marks in
// order); an unknown name changes nothing.
func (c *twilioCall) marked(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, q := range c.cues {
		if q.name != name {
			continue
		}
		c.played, c.cues = q.end, c.cues[i+1:]
		close(c.changed)
		c.changed = make(chan struct{})
		return
	}
}

func (c *twilioCall) Played() (int64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.played, c.changed
}

// Flush is Twilio's clear: queued audio is dropped and its marks are echoed at once.
func (c *twilioCall) Flush() error {
	sid := c.stream()
	if sid == "" {
		return errNotStreaming
	}
	return c.send(live.ClearMessage(sid))
}

// SendDTMF: Media Streams carries no DTMF out (the gateway never sent any either).
func (c *twilioCall) SendDTMF(string) error { return ErrUnsupported }

// Hangup sets the REST call's status to completed; Twilio then stops the stream.
func (c *twilioCall) Hangup(ctx context.Context) error {
	sid := c.ID()
	if sid == "" {
		return nil
	}
	return c.p.api.EndCall(ctx, sid)
}

// Close revokes the stream token and closes the stream (a normal close, "call ended"), which
// ends the call's <Connect><Stream>. A call whose stream never connected ends here.
func (c *twilioCall) Close() {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		close(c.closed)
		attached := c.conn != nil
		c.mu.Unlock()
		if c.p != nil && c.p.media != nil {
			c.p.media.unregister(c)
		}
		if attached {
			// serve sees the read fail and finishes.
			c.closeWith(websocket.CloseNormalClosure, "call ended")
			return
		}
		c.finish(EndClosed)
	})
}
