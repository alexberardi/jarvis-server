package live

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
)

// Twilio Media Streams wire format and security checks (telephony/twilio_provider.py).
//
// Bidirectional streams need TwiML <Connect><Stream> given to calls.create; frames are
// headerless base64 mu-law, 8 kHz, 20 ms (160 bytes). The WS upgrade is signed with
// X-Twilio-Signature over the wss:// URL Twilio was handed, while server-side reconstruction
// yields https://, so validation tries both forms. On a static URL the signature is a replayable
// constant, so every session gets a single-use token in the path.

// FrameBytes is 20 ms of 8 kHz mu-law.
const FrameBytes = 160

// ComputeSignature is Twilio's request signature: base64(HMAC-SHA1(url + sorted k+v pairs)).
func ComputeSignature(authToken, url string, params map[string]string) string {
	payload := url
	for _, k := range sortedKeys(params) {
		payload += k + params[k]
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// ValidateWSSignature validates X-Twilio-Signature on a media-stream upgrade, trying the URL as
// given and its ws(s)/http(s) counterpart. A missing signature never validates.
func ValidateWSSignature(authToken, requestURL, signature string) bool {
	if signature == "" {
		return false
	}
	candidates := []string{requestURL}
	for _, p := range [][2]string{{"https://", "wss://"}, {"http://", "ws://"}} {
		if strings.HasPrefix(requestURL, p[0]) {
			candidates = append(candidates, p[1]+requestURL[len(p[0]):])
		} else if strings.HasPrefix(requestURL, p[1]) {
			candidates = append(candidates, p[0]+requestURL[len(p[1]):])
		}
	}
	ok := false
	for _, u := range candidates {
		if subtle.ConstantTimeCompare([]byte(ComputeSignature(authToken, u, nil)), []byte(signature)) == 1 {
			ok = true
		}
	}
	return ok
}

// quoteAttr is xml.sax.saxutils.quoteattr.
func quoteAttr(s string) string {
	s = strings.NewReplacer("&", "&amp;", ">", "&gt;", "<", "&lt;", "\n", "&#10;", "\r", "&#13;", "\t", "&#9;").Replace(s)
	if strings.Contains(s, `"`) {
		if strings.Contains(s, "'") {
			return `"` + strings.ReplaceAll(s, `"`, "&quot;") + `"`
		}
		return "'" + s + "'"
	}
	return `"` + s + `"`
}

// StreamTwiML points the call's media at wssURL; params become <Parameter> entries Twilio echoes
// in the stream-start event (the session binding).
func StreamTwiML(wssURL string, params [][2]string) string {
	var p strings.Builder
	for _, kv := range params {
		p.WriteString("<Parameter name=" + quoteAttr(kv[0]) + " value=" + quoteAttr(kv[1]) + "/>")
	}
	return "<Response><Connect>" +
		"<Stream url=" + quoteAttr(wssURL) + ">" + p.String() + "</Stream>" +
		"</Connect></Response>"
}

// StreamStart is the first stream event: the binding material.
type StreamStart struct {
	StreamSID  string
	CallSID    string
	CallSIDSet bool
	Params     map[string]string
}

// InboundAudio is one decoded media frame (8 kHz PCM).
type InboundAudio struct{ PCM []int16 }

// MarkReceived: Twilio played audio up to a named mark.
type MarkReceived struct{ Name string }

// StreamStop: Twilio ended the stream.
type StreamStop struct{}

type wsMessage struct {
	Event string `json:"event"`
	Start *struct {
		StreamSID        string         `json:"streamSid"`
		CallSID          *string        `json:"callSid"`
		CustomParameters map[string]any `json:"customParameters"`
	} `json:"start"`
	Media *struct {
		Payload string `json:"payload"`
	} `json:"media"`
	Mark *struct {
		Name string `json:"name"`
	} `json:"mark"`
}

// ParseWSMessage decodes one inbound message into StreamStart, InboundAudio, MarkReceived or
// StreamStop; nil for ignorable or unparseable messages.
func ParseWSMessage(raw []byte) any {
	var m wsMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	switch m.Event {
	case "start":
		s := StreamStart{Params: map[string]string{}}
		if m.Start != nil {
			s.StreamSID = m.Start.StreamSID
			if m.Start.CallSID != nil {
				s.CallSID, s.CallSIDSet = *m.Start.CallSID, true
			}
			for k, v := range m.Start.CustomParameters {
				if str, ok := v.(string); ok {
					s.Params[k] = str
				}
			}
		}
		return s
	case "media":
		payload := ""
		if m.Media != nil {
			payload = m.Media.Payload
		}
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil
		}
		return InboundAudio{PCM: MulawDecode(b)}
	case "mark":
		name := ""
		if m.Mark != nil {
			name = m.Mark.Name
		}
		return MarkReceived{Name: name}
	case "stop":
		return StreamStop{}
	}
	return nil
}

// MediaMessages are the outbound media messages for one PCM buffer, split into 160-byte frames.
func MediaMessages(streamSID string, pcm8k []int16) []map[string]any {
	mu := MulawEncode(pcm8k)
	var out []map[string]any
	for i := 0; i < len(mu); i += FrameBytes {
		end := min(i+FrameBytes, len(mu))
		out = append(out, map[string]any{
			"event": "media", "streamSid": streamSID,
			"media": map[string]any{"payload": base64.StdEncoding.EncodeToString(mu[i:end])},
		})
	}
	return out
}

// MarkMessage asks Twilio to echo name once the audio before it has played.
func MarkMessage(streamSID, name string) map[string]any {
	return map[string]any{"event": "mark", "streamSid": streamSID, "mark": map[string]any{"name": name}}
}

// ClearMessage flushes queued playback (barge-in).
func ClearMessage(streamSID string) map[string]any {
	return map[string]any{"event": "clear", "streamSid": streamSID}
}

// PendingSession is what the registry knows between TwiML and stream-start. CallSID "" means
// not bound yet.
type PendingSession struct {
	SessionID string
	CallSID   string
}

// TokenRegistry issues single-use wss-path tokens at TwiML time and claims them at the upgrade.
// Claiming pops, so a replay or duplicate stream is rejected. Safe for concurrent use.
type TokenRegistry struct {
	mu      sync.Mutex
	pending map[string]*PendingSession
}

// Issue mints a token (secrets.token_urlsafe(32)) for a session.
func (r *TokenRegistry) Issue(sessionID string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	r.mu.Lock()
	if r.pending == nil {
		r.pending = map[string]*PendingSession{}
	}
	r.pending[tok] = &PendingSession{SessionID: sessionID}
	r.mu.Unlock()
	return tok
}

// BindCallSID records the call id calls.create returned.
func (r *TokenRegistry) BindCallSID(token, callSID string) {
	r.mu.Lock()
	if p, ok := r.pending[token]; ok {
		p.CallSID = callSID
	}
	r.mu.Unlock()
}

// Claim pops a token.
func (r *TokenRegistry) Claim(token string) (*PendingSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.pending[token]
	if ok {
		delete(r.pending, token)
	}
	return p, ok
}

// Revoke drops a token (no-op once claimed).
func (r *TokenRegistry) Revoke(token string) {
	r.mu.Lock()
	delete(r.pending, token)
	r.mu.Unlock()
}

// ValidateStreamStart binds the stream-start event to the claimed session: the session_id
// parameter must match, and the callSid too once one is bound.
func ValidateStreamStart(s StreamStart, p PendingSession) bool {
	if sid, ok := s.Params["session_id"]; !ok || sid != p.SessionID {
		return false
	}
	if p.CallSID != "" && (!s.CallSIDSet || s.CallSID != p.CallSID) {
		return false
	}
	return true
}
