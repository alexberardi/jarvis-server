package live

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// The call brain's text-token tool protocol (llm/tool_tokens.py): [HANGUP], [ESCALATE: q],
// [OUTCOME: facts] and [DTMF: digits] (reserved). Plain text tokens because the streaming
// path drops native tools.

// ToolEvent is one parsed token.
type ToolEvent interface{ toolEvent() }

// Hangup ends the call after the current reply.
type Hangup struct{}

// Escalate asks the user a question mid-call.
type Escalate struct{ Question string }

// Outcome records a confirmed result.
type Outcome struct{ Facts string }

// Dtmf is reserved (parsed, ignored).
type Dtmf struct{ Digits string }

func (Hangup) toolEvent()   {}
func (Escalate) toolEvent() {}
func (Outcome) toolEvent()  {}
func (Dtmf) toolEvent()     {}

// maxTokenLen bounds how long an unclosed candidate is buffered.
const maxTokenLen = 512

var tokenRE = regexp.MustCompile(`(?s)^\[(?:(HANGUP)|(ESCALATE|OUTCOME|DTMF):\s*(.*))\]$`)

var tokenPrefixes = []string{"HANGUP]", "ESCALATE:", "OUTCOME:", "DTMF:"}

// couldBecomeToken: candidate starts after '[' and has no ']' yet; is it worth holding?
func couldBecomeToken(candidate string) bool {
	if utf8.RuneCountInString(candidate) > maxTokenLen {
		return false
	}
	for _, p := range tokenPrefixes {
		if len(candidate) <= len(p) {
			if strings.HasPrefix(p, candidate) {
				return true
			}
		} else if strings.HasPrefix(candidate, p) {
			return true
		}
	}
	return false
}

func parseComplete(body string) ToolEvent {
	m := tokenRE.FindStringSubmatch("[" + body + "]")
	if m == nil {
		return nil
	}
	if m[1] != "" {
		return Hangup{}
	}
	arg := strings.TrimSpace(m[3])
	switch m[2] {
	case "ESCALATE":
		return Escalate{Question: arg}
	case "OUTCOME":
		return Outcome{Facts: arg}
	default:
		return Dtmf{Digits: arg}
	}
}

// TokenParser is stream-safe: feed deltas, receive speakable text and events. A '[' that could
// still grow into a token is held until ']' arrives or the prefix stops matching; anything
// that is not a token is released verbatim.
type TokenParser struct{ buf string }

// Feed consumes one delta.
func (p *TokenParser) Feed(delta string) (string, []ToolEvent) {
	p.buf += delta
	var text strings.Builder
	var events []ToolEvent
	for p.buf != "" {
		i := strings.IndexByte(p.buf, '[')
		if i == -1 {
			text.WriteString(p.buf)
			p.buf = ""
			break
		}
		text.WriteString(p.buf[:i])
		rest := p.buf[i+1:]
		j := strings.IndexByte(rest, ']')
		if j == -1 {
			if couldBecomeToken(rest) {
				p.buf = p.buf[i:]
				break
			}
			text.WriteByte('[')
			p.buf = rest
			continue
		}
		body := rest[:j]
		if ev := parseComplete(body); ev != nil {
			events = append(events, ev)
		} else {
			text.WriteString("[" + body + "]")
		}
		p.buf = rest[j+1:]
	}
	return text.String(), events
}

// Flush ends the stream: an unfinished candidate is plain text after all.
func (p *TokenParser) Flush() string {
	out := p.buf
	p.buf = ""
	return out
}
