// Package sip is the SIP phone provider's transport (docs/cc/phone-sip.md §1-§3): jarvisd joins
// a PBX (FreePBX, Asterisk, FreeSWITCH, 3CX) as an ordinary extension, keeps that registration
// alive, places outbound calls through the PBX's own routes and answers the calls it routes
// in. It wraps emiago/diago and emiago/sipgo (pinned in go.mod,
// docs/decisions/2026-10-22-sip-library-pin.md) so neither leaks past this package: package
// phone sees a UA and a Call carrying 8 kHz PCM.
//
// What it adds over the libraries, from the gotchas in phone-sip.md §3:
//   - a registration supervisor: exponential backoff, OPTIONS keepalive to the registrar, a
//     stable local port and Contact, Expires: 0 on a clean shutdown, and a hard STOP after 3
//     authentication failures, since a PBX firewall bans a host that keeps retrying;
//   - requests are accepted only from the registrar's address; OPTIONS is answered 200;
//   - an inbound call is offered to Config.Inbound with its caller (P-Asserted-Identity, then
//     From: a hint, never authentication), refused as it says (480, or 486 busy), or answered
//     after its delay with 180 Ringing meanwhile; a CANCEL while it rings leaves it unanswered;
//   - an outbound call is answered on 200 only (183 never is), and its final code maps to an
//     outcome; Started waits for the first RTP packet after the 200, so the disclosure plays
//     to audio that is flowing;
//   - a 20 ms pacer that always sends (silence included: Asterisk's rtptimeout hangs up a
//     silent leg), a no-RTP watchdog, re-INVITE/UPDATE answered with the same SDP, the DTMF
//     payload type taken from the answer SDP, media-level c= honoured, and recover() around
//     every handler (remote SDP has panicked diago before).
package sip

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Config is one SIP account: the PBX it registers to and the extension it registers as.
type Config struct {
	// Server is the registrar's host (IP or name) and Port its SIP port (default 5060).
	Server string
	Port   int
	// Transport is "udp" (default) or "tcp".
	Transport string
	// Extension is the extension (and digest user) jarvisd registers as; Password its secret.
	Extension string
	Password  string
	// LocalPort is jarvisd's own SIP port. It stays the same across restarts, so the Contact
	// does too: FreePBX 17 defaults to Remove Existing = yes (a new Contact replaces the
	// old), but an older install or an admin's max_contacts=1 with remove_existing=no refuses
	// a second Contact until the first expires, and each refusal feeds its firewall. Default
	// 5062, not 5060, in case the PBX runs on the same host.
	LocalPort int
	// BindHost is the local address to bind; "" is the address the route to Server uses.
	BindHost string
	// UserAgent is the User-Agent header; default "Jarvis".
	UserAgent string
	Log       *slog.Logger
	// Inbound decides each call the PBX routes to the extension (inbound.go); nil refuses
	// every one 480, which sends the caller on to the PBX's next destination.
	Inbound func(ctx context.Context, in Incoming) Admission

	// Timings and limits; zero takes the default.
	RegisterExpiry  time.Duration // asked for in REGISTER (300 s)
	RequestTimeout  time.Duration // one REGISTER or OPTIONS round trip (10 s)
	KeepAlive       time.Duration // OPTIONS to the registrar while registered (30 s)
	BackoffMin      time.Duration // first retry after a failure (2 s), doubling
	BackoffMax      time.Duration // (5 min)
	NoRTPTimeout    time.Duration // a call with no RTP this long is over (30 s)
	TransferWait    time.Duration // a transfer's wait for the PBX's final NOTIFY (30 s)
	MaxAuthFailures int           // authentication failures before registration stops (3)
}

// Defaults.
const (
	DefaultPort      = 5060
	DefaultLocalPort = 5062
	defaultExpiry    = 300 * time.Second
	defaultTimeout   = 10 * time.Second
	defaultKeepAlive = 30 * time.Second
	defaultBackoff   = 2 * time.Second
	defaultMaxWait   = 5 * time.Minute
	defaultNoRTP     = 30 * time.Second
	defaultTransfer  = 30 * time.Second
	defaultAuthFails = 3
)

func (c *Config) defaults() {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Transport == "" {
		c.Transport = "udp"
	}
	if c.LocalPort == 0 {
		c.LocalPort = DefaultLocalPort
	}
	if c.UserAgent == "" {
		c.UserAgent = "Jarvis"
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.RegisterExpiry, defaultExpiry)
	def(&c.RequestTimeout, defaultTimeout)
	def(&c.KeepAlive, defaultKeepAlive)
	def(&c.BackoffMin, defaultBackoff)
	def(&c.BackoffMax, defaultMaxWait)
	def(&c.NoRTPTimeout, defaultNoRTP)
	def(&c.TransferWait, defaultTransfer)
	if c.MaxAuthFailures <= 0 {
		c.MaxAuthFailures = defaultAuthFails
	}
}

func (c Config) validate() error {
	var missing []string
	if strings.TrimSpace(c.Server) == "" {
		missing = append(missing, "server")
	}
	if strings.TrimSpace(c.Extension) == "" {
		missing = append(missing, "extension")
	}
	if c.Password == "" {
		missing = append(missing, "password")
	}
	if len(missing) > 0 {
		return fmt.Errorf("sip: missing %s", strings.Join(missing, ", "))
	}
	if c.Transport != "udp" && c.Transport != "tcp" {
		return fmt.Errorf("sip: transport %q (udp or tcp)", c.Transport)
	}
	return nil
}

// String never prints the password.
func (c Config) String() string {
	return fmt.Sprintf("sip.Config{%s@%s:%d/%s, local port %d, password_set=%t}",
		c.Extension, c.Server, c.Port, c.Transport, c.LocalPort, c.Password != "")
}

// GoString keeps %#v as safe as %v.
func (c Config) GoString() string { return c.String() }

// State is where a UA's registration is.
type State string

const (
	// StateRegistering: the first REGISTER is under way.
	StateRegistering State = "registering"
	// StateRegistered: the PBX accepted the registration and answers OPTIONS.
	StateRegistered State = "registered"
	// StateRetrying: the last attempt failed; the next one waits out a backoff.
	StateRetrying State = "retrying"
	// StateStopped: registration gave up (authentication failures) and will not retry until
	// the account's settings change. Hint says what to do.
	StateStopped State = "stopped"
	// StateClosed: the UA was shut down.
	StateClosed State = "closed"
)

// Status is a UA's registration state for status pages and the provider's Ready.
type Status struct {
	State        State
	Since        time.Time
	LastError    string // the last failure, "" after a success
	Hint         string // what the user can do about it (plain words)
	AuthFailures int    // consecutive, since the last success
	Attempts     int    // REGISTER attempts (each may be two requests: challenge and answer)

	// LastRegister is when the last REGISTER attempt ended and RegisterRTT how long it took
	// (the challenge and the answer); LastOptions and OptionsRTT the same for the last
	// keepalive OPTIONS that was answered.
	LastRegister time.Time
	RegisterRTT  time.Duration
	LastOptions  time.Time
	OptionsRTT   time.Duration
	// ActiveCalls is the calls under way (ringing or answered).
	ActiveCalls int
	// LastInbound is the last inbound INVITE the PBX sent and what we answered it; its At is
	// zero when none came.
	LastInbound InboundInvite
}

// InboundInvite is what an inbound INVITE said: who calls and where the PBX would send audio
// from. The inbound watch self-test shows it.
type InboundInvite struct {
	At         time.Time
	Caller     string   // the number: P-Asserted-Identity's user, else From's
	CallerName string   // the display name, if any
	To         string   // the dialed user (our extension, or the DID the PBX passes on)
	Media      string   // the SDP's audio address (ip:port), "" without SDP
	Codecs     []string // the audio codecs offered, in order
	Response   int      // what we answered
}

// HintBlocked is what a stopped registration says: the PBX refused our password, or its
// firewall has already banned this host and drops or refuses everything from it.
const HintBlocked = "Check the extension's password. After repeated failures the PBX may have " +
	"blocked this host: unblock it in the PBX's firewall (FreePBX: Admin → Firewall → Status), " +
	"then correct the password in Jarvis's SIP settings, or restart Jarvis, to try again."

// ErrNotRegistered is a call placed while the UA is not registered.
var ErrNotRegistered = errors.New("sip: not registered with the PBX")

// End is why a call ended.
type End string

const (
	EndNone End = ""
	// EndRemote: the far end or the PBX hung up after the answer.
	EndRemote End = "remote"
	// EndClosed: we hung up or closed the call.
	EndClosed End = "closed"
	// EndBusy: 486 / 600 before the answer.
	EndBusy End = "busy"
	// EndNoAnswer: nobody answered (480, 408, 487, or 183 then 503: FreePBX's no answer).
	EndNoAnswer End = "no_answer"
	// EndNotFound: the number could not be reached as dialed (404, 484, 604, 410).
	EndNotFound End = "not_found"
	// EndFailed: any other final response, or the call could not be set up.
	EndFailed End = "failed"
	// EndNoMedia: no RTP from the far end for NoRTPTimeout.
	EndNoMedia End = "no_media"
	// EndTransferred: the PBX put the call through to a transfer target (Call.Transfer) and
	// we hung up our leg.
	EndTransferred End = "transferred"
	// EndTransport: the PBX did not answer the INVITE at all, or the media socket failed.
	EndTransport End = "transport"
)
