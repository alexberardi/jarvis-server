package sip

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

// The SIP trace (docs/cc/phone-sip.md §6; decision 2026-10-22-sip-support-tools.md): the last
// TraceCalls calls' SIP messages, and the last TraceOther registration and keepalive
// exchanges, kept in memory so a failed call can be exported from the admin and read by
// someone who cannot see the PBX. Never audio (RTP does not pass through here), never a
// password or a digest response.
//
// sipgo v1.5.0's tracer is process-wide (sip.SIPDebug and sip.SIPDebugTracer), so one tracer
// serves every UA: a message belongs to the UA whose local address it was sent or read on,
// or, over TCP (where our outgoing connection has an ephemeral port), whose registrar it was
// exchanged with. Messages of no UA's are dropped. With a tracer set, SIPDebug only calls it:
// sipgo logs nothing more.

// Ring sizes.
const (
	TraceCalls       = 20  // INVITE dialogs (and every other in-dialog method)
	TraceOther       = 30  // REGISTER and OPTIONS exchanges, one per Call-ID
	traceMessages    = 100 // per dialog; the oldest go first
	traceMessageSize = 16 << 10
)

// Trace kinds.
const (
	TraceCall         = "call"
	TraceRegistration = "registration"
)

// TraceMessage is one SIP message, redacted.
type TraceMessage struct {
	At        time.Time `json:"at"`
	Dir       string    `json:"dir"` // "sent" or "received"
	Transport string    `json:"transport"`
	Local     string    `json:"local"`
	Remote    string    `json:"remote"`
	FirstLine string    `json:"first_line"`
	Text      string    `json:"text"`
}

// TraceDialog is one Call-ID's messages, oldest first.
type TraceDialog struct {
	CallID   string         `json:"call_id"`
	Account  string         `json:"account"` // extension@server:port
	Kind     string         `json:"kind"`    // TraceCall or TraceRegistration
	Method   string         `json:"method"`  // the first message's CSeq method
	Started  time.Time      `json:"started"`
	Updated  time.Time      `json:"updated"`
	Messages []TraceMessage `json:"messages"`
	Dropped  int            `json:"dropped"` // older messages no longer kept
}

// traceAccount is one UA as the tracer knows it.
type traceAccount struct {
	label     string
	laddr     string // bind:port of the UA's socket
	registrar string // ip:port
	passwords []string
}

type tracer struct {
	calls, other, perDialog int

	mu       sync.Mutex
	accounts []*traceAccount
	dialogs  map[string]*TraceDialog // by Call-ID
	order    map[string][]string     // kind → Call-IDs, oldest first
}

func newTracer(calls, other, perDialog int) *tracer {
	return &tracer{calls: calls, other: other, perDialog: perDialog,
		dialogs: map[string]*TraceDialog{}, order: map[string][]string{}}
}

var processTracer = newTracer(TraceCalls, TraceOther, traceMessages)

func init() {
	// Set once before any socket exists: sipgo reads SIPDebug unlocked on every message.
	sip.SIPDebug = true
	sip.SIPDebugTracer(processTracer)
}

// Trace is every UA's recent SIP traffic, newest dialog first.
func Trace() []TraceDialog { return processTracer.snapshot() }

func (t *tracer) SIPTraceRead(transport, laddr, raddr string, msg []byte) {
	t.record(true, transport, laddr, raddr, msg)
}

func (t *tracer) SIPTraceWrite(transport, laddr, raddr string, msg []byte) {
	t.record(false, transport, laddr, raddr, msg)
}

func (t *tracer) add(a *traceAccount) {
	t.mu.Lock()
	t.accounts = append(t.accounts, a)
	t.mu.Unlock()
}

func (t *tracer) remove(a *traceAccount) {
	t.mu.Lock()
	for i, x := range t.accounts {
		if x == a {
			t.accounts = append(t.accounts[:i], t.accounts[i+1:]...)
			break
		}
	}
	t.mu.Unlock()
}

// account is the UA a message on laddr ↔ raddr belongs to (mu held).
func (t *tracer) account(laddr, raddr string) *traceAccount {
	for _, a := range t.accounts {
		if a.laddr == laddr {
			return a
		}
	}
	for _, a := range t.accounts {
		if a.registrar != "" && a.registrar == raddr {
			return a
		}
	}
	return nil
}

func (t *tracer) record(read bool, transport, laddr, raddr string, msg []byte) {
	if len(bytes.TrimSpace(msg)) == 0 {
		return // a TCP keepalive (CRLF) or an empty read
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.account(laddr, raddr)
	if a == nil {
		return
	}
	text := redact(msg, a.passwords)
	callID, method := headerValue(text, "call-id", "i"), cseqMethod(text)
	if callID == "" {
		callID = "(no Call-ID)"
	}
	dir := "sent"
	if read {
		dir = "received"
	}
	m := TraceMessage{At: now, Dir: dir, Transport: transport, Local: laddr, Remote: raddr,
		FirstLine: firstLine(text), Text: text}
	d := t.dialogs[callID]
	if d == nil {
		kind := TraceCall
		if method == "REGISTER" || method == "OPTIONS" {
			kind = TraceRegistration
		}
		d = &TraceDialog{CallID: callID, Account: a.label, Kind: kind, Method: method, Started: now}
		t.dialogs[callID] = d
		t.order[kind] = append(t.order[kind], callID)
		limit := t.calls
		if kind == TraceRegistration {
			limit = t.other
		}
		for len(t.order[kind]) > limit {
			delete(t.dialogs, t.order[kind][0])
			t.order[kind] = t.order[kind][1:]
		}
	}
	d.Updated = now
	d.Messages = append(d.Messages, m)
	if over := len(d.Messages) - t.perDialog; over > 0 {
		d.Messages = append([]TraceMessage(nil), d.Messages[over:]...)
		d.Dropped += over
	}
}

// snapshot copies every dialog, newest (by last message) first.
func (t *tracer) snapshot() []TraceDialog {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TraceDialog, 0, len(t.dialogs))
	for _, kind := range []string{TraceCall, TraceRegistration} {
		ids := t.order[kind]
		for i := len(ids) - 1; i >= 0; i-- {
			d := *t.dialogs[ids[i]]
			d.Messages = append([]TraceMessage(nil), d.Messages...)
			out = append(out, d)
		}
	}
	return out
}

// WriteTraceText writes dialogs as plain text for a support request: one block per dialog,
// each message with its time, direction and addresses.
func WriteTraceText(w io.Writer, dialogs []TraceDialog) {
	fmt.Fprintf(w, "Jarvis SIP trace, %s (%d dialogs; passwords and digest responses removed)\n",
		time.Now().UTC().Format(time.RFC3339), len(dialogs))
	for _, d := range dialogs {
		fmt.Fprintf(w, "\n######## %s %s  Call-ID %s  account %s  %s\n", d.Kind, d.Method, d.CallID, d.Account,
			d.Started.UTC().Format(time.RFC3339))
		if d.Dropped > 0 {
			fmt.Fprintf(w, "(%d earlier messages not kept)\n", d.Dropped)
		}
		for _, m := range d.Messages {
			arrow := "->"
			if m.Dir == "received" {
				arrow = "<-"
			}
			fmt.Fprintf(w, "\n=== %s %s %s %s %s %s ===\n%s", m.At.UTC().Format("15:04:05.000"), m.Dir, m.Transport,
				m.Local, arrow, m.Remote, strings.TrimRight(m.Text, "\r\n")+"\n")
		}
	}
}

// --- redaction ---

// authHeaders are the headers carrying a digest answer; their other parameters (response,
// nonce, cnonce, opaque, nc) would let someone test password guesses offline.
var authHeaders = map[string]bool{"authorization": true, "proxy-authorization": true}

// keptAuthParams are what a support reader needs of a digest answer.
var keptAuthParams = map[string]bool{"username": true, "realm": true, "uri": true, "algorithm": true, "qop": true}

var authParam = regexp.MustCompile(`([A-Za-z-]+)\s*=\s*("[^"]*"|[^,\s]*)`)

// redact is msg as text with every digest answer's secrets and every occurrence of the
// account's passwords replaced by <redacted>, folded header lines unfolded, a non-text body
// left out and the whole capped at traceMessageSize.
func redact(msg []byte, passwords []string) string {
	head, body, hasBody := splitMessage(string(msg))
	lines := unfold(strings.Split(head, "\n"))
	var b strings.Builder
	ctype := ""
	for _, l := range lines {
		name, value, ok := strings.Cut(l, ":")
		lname := strings.ToLower(strings.TrimSpace(name))
		if ok && (lname == "content-type" || lname == "c") {
			ctype = strings.ToLower(strings.TrimSpace(value))
		}
		if ok && authHeaders[lname] {
			l = name + ":" + redactAuth(value)
		}
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	if hasBody {
		b.WriteString("\r\n")
		switch {
		case body == "":
		case textBody(ctype, body):
			b.WriteString(body)
		default:
			fmt.Fprintf(&b, "<%d bytes of %s omitted>\r\n", len(body), orUnknown(ctype))
		}
	}
	out := b.String()
	for _, p := range passwords {
		if p != "" {
			out = strings.ReplaceAll(out, p, "<redacted>")
		}
	}
	if len(out) > traceMessageSize {
		out = out[:traceMessageSize] + "\r\n<truncated>\r\n"
	}
	return out
}

func redactAuth(value string) string {
	v := strings.TrimSpace(value)
	scheme, params, _ := strings.Cut(v, " ")
	var kept []string
	for _, m := range authParam.FindAllStringSubmatch(params, -1) {
		if keptAuthParams[strings.ToLower(m[1])] {
			kept = append(kept, m[1]+"="+m[2])
		} else {
			kept = append(kept, m[1]+`="<redacted>"`)
		}
	}
	return " " + scheme + " " + strings.Join(kept, ", ")
}

// splitMessage splits at the blank line; hasBody is whether there was one.
func splitMessage(s string) (head, body string, hasBody bool) {
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		return s[:i], s[i+4:], true
	}
	if i := strings.Index(s, "\n\n"); i >= 0 {
		return s[:i], s[i+2:], true
	}
	return strings.TrimRight(s, "\r\n"), "", false
}

// unfold joins continuation lines (starting with a space or tab) to the header before them.
func unfold(lines []string) []string {
	var out []string
	for _, l := range lines {
		l = strings.TrimRight(l, "\r")
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += " " + strings.TrimSpace(l)
			continue
		}
		out = append(out, l)
	}
	return out
}

func textBody(ctype, body string) bool {
	if ctype == "" || strings.HasPrefix(ctype, "application/sdp") || strings.HasPrefix(ctype, "text/") ||
		strings.Contains(ctype, "dtmf") || strings.Contains(ctype, "sipfrag") || strings.Contains(ctype, "xml") {
		for _, r := range body {
			if r == 0 || (r < 0x20 && r != '\r' && r != '\n' && r != '\t') {
				return false
			}
		}
		return true
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}

func firstLine(text string) string {
	l, _, _ := strings.Cut(text, "\r\n")
	return l
}

// headerValue is the first header named name (or its compact form) in text.
func headerValue(text, name, compact string) string {
	head, _, _ := splitMessage(text)
	for _, l := range strings.Split(head, "\n") {
		n, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		n = strings.ToLower(strings.TrimSpace(n))
		if n == name || (compact != "" && n == compact) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func cseqMethod(text string) string {
	f := strings.Fields(headerValue(text, "cseq", ""))
	if len(f) == 2 {
		return strings.ToUpper(f[1])
	}
	return ""
}

// traceLabel names the UA's account in the trace.
func (u *UA) traceLabel() string {
	return fmt.Sprintf("%s@%s:%d", u.cfg.Extension, u.cfg.Server, u.cfg.Port)
}

// traceAccount is the UA's entry in the process tracer.
func (u *UA) newTraceAccount() *traceAccount {
	a := &traceAccount{label: u.traceLabel(), laddr: net.JoinHostPort(u.bindHost, fmt.Sprint(u.cfg.LocalPort)),
		passwords: []string{u.cfg.Password}}
	if len(u.allowed) > 0 {
		a.registrar = net.JoinHostPort(u.allowed[0].String(), fmt.Sprint(u.cfg.Port))
	}
	return a
}
