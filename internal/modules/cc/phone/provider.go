package phone

import (
	"context"
	"errors"
)

// Provider is the telephony seam (gateway telephony/provider.py, PRD decision 7,
// docs/cc/phone-sip.md §1): place a call and hand the live loop a Call that carries its audio
// both ways, and look up a number's line type. The live loop (VAD, STT, model, TTS,
// escalation, recorder) only ever talks to a Call, so Twilio (Media Streams over our
// WebSocket, twilio.go and twilio_media.go) and a SIP/RTP adapter sit behind the same
// interface. Tests use fakes; only Twilio talks to a real vendor today.
type Provider interface {
	// Ready reports whether a call can be placed now, checked before anything is spent on the
	// call (the disclosure prewarm). Twilio: a public URL for the media stream is configured.
	// The error's text is the session's failure reason.
	Ready() error
	// Dial places the outbound call. It returns once the provider has accepted it, not when
	// it is answered (Call.Started); the error's text is the session's failure reason.
	Dial(ctx context.Context, req DialRequest) (Call, error)
	// LineType is "mobile" | "landline" | "voip" | "unknown"; any failure is "unknown".
	LineType(ctx context.Context, e164 string) string
}

// DialRequest is one outbound call.
type DialRequest struct {
	SessionID   string
	HouseholdID string
	To          string // E.164
}

// Call is one placed call: its audio, its playback and its end. Audio is 8 kHz mono PCM16
// both ways. A Call is used by one goroutine at a time (the call loop), except Close, Done
// and Hangup, which the dialer's supervisor may call from its own.
type Call interface {
	// ID is the provider's call id (Twilio's call SID), stored on the session.
	ID() string
	// Started is closed once the call is answered and its media flows (Twilio: a validated
	// stream-start).
	Started() <-chan struct{}
	// Frames yields the far end's audio in 20 ms frames (160 samples) from Started on,
	// silence included, so the inbound stream is the call's clock. It is closed when the
	// media ends. A transport that cannot deliver blocks rather than drops: a turn that runs
	// long holds the far end's audio back, it does not lose it.
	Frames() <-chan []int16
	// Play queues pcm behind anything already queued; the transport paces it out in real
	// time. It returns the playback position, in samples since Started, at which pcm ends.
	// cue names that end in the transport's own playback reports (Twilio's mark name);
	// a transport that tracks playback itself ignores it.
	Play(pcm []int16, cue string) (end int64, err error)
	// Played is how far playback has got, in samples since Started, and a channel closed
	// at its next change.
	Played() (pos int64, changed <-chan struct{})
	// Flush drops queued playback that has not been heard yet (barge-in). Played then
	// reaches the end of everything queued so far (Twilio: its marks are echoed at once), so a
	// later Play's end stays on the same scale; a SIP pacer drops its queue and advances its
	// position to the queue's end.
	Flush() error
	// SendDTMF sends digits in band or out of band; ErrUnsupported when the transport can't.
	SendDTMF(digits string) error
	// Done is closed when the call's media has ended; EndReason says why.
	Done() <-chan struct{}
	EndReason() EndReason
	// Hangup ends the call at the provider (Twilio: the REST call's status → completed).
	Hangup(ctx context.Context) error
	// Close releases the media and anything the call holds (Twilio: the stream token and
	// the WebSocket, which ends a <Connect><Stream> call). Safe to call more than once.
	Close()
}

// EndReason is why a call's media ended.
type EndReason string

const (
	// EndNone: the media has not ended.
	EndNone EndReason = ""
	// EndRemote: the far end or the provider ended it (Twilio: the stream stop event).
	EndRemote EndReason = "remote"
	// EndClosed: we closed it (Close).
	EndClosed EndReason = "closed"
	// EndRejected: the media failed validation (Twilio: stream-start bound to another call).
	EndRejected EndReason = "rejected"
	// EndTransport: the media connection was lost (SIP: or the PBX never answered the INVITE).
	EndTransport EndReason = "transport"
	// Before an answer (SIP's final responses, docs/cc/phone-sip.md §3.3):
	// EndBusy: the line was busy (486/600).
	EndBusy EndReason = "busy"
	// EndNoAnswer: nobody answered (480/408, or FreePBX's 183 announcement then 503).
	EndNoAnswer EndReason = "no_answer"
	// EndNotFound: the number could not be reached as dialed (404/484/604).
	EndNotFound EndReason = "not_found"
	// EndFailed: the provider could not place the call (any other refusal).
	EndFailed EndReason = "failed"
	// EndNoMedia: the far end's audio stopped (SIP's no-RTP watchdog).
	EndNoMedia EndReason = "no_media"
)

// endDetailer is a Call that can say more about its end than its EndReason (SIP: the response,
// "486 Busy Here").
type endDetailer interface{ EndDetail() string }

// endMessage is the session's failure reason for a call that ended before it was answered.
func endMessage(c Call) string {
	var msg string
	switch c.EndReason() {
	case EndBusy:
		msg = "the line was busy"
	case EndNoAnswer:
		msg = "no answer"
	case EndNotFound:
		msg = "the number could not be reached as dialed (check the PBX's dial format, phone.sip.dial_format)"
	case EndFailed:
		msg = "the phone system could not place the call"
	case EndNoMedia:
		msg = "no audio from the far end"
	case EndTransport:
		msg = "the phone system did not respond"
	case EndRejected:
		msg = "the call's media was rejected"
	default:
		msg = "the call ended before it was answered"
	}
	if d, ok := c.(endDetailer); ok && d.EndDetail() != "" {
		msg += " (" + d.EndDetail() + ")"
	}
	return msg
}

// ErrUnsupported is a Call operation its transport does not offer.
var ErrUnsupported = errors.New("phone: not supported by this provider")
