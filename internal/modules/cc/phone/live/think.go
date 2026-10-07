package live

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// partialSuffixLen is the longest k < len(tag) such that text ends with tag[:k].
func partialSuffixLen(text, tag string) int {
	for k := min(len(tag)-1, len(text)); k > 0; k-- {
		if strings.HasSuffix(text, tag[:k]) {
			return k
		}
	}
	return 0
}

// ThinkStripper removes <think>...</think> blocks from a token stream (llm/think_strip.py).
// Tags may arrive split across deltas; an unclosed block never leaks.
type ThinkStripper struct {
	buf    string
	inside bool
}

// Feed consumes one delta and returns the speakable text so far.
func (s *ThinkStripper) Feed(delta string) string {
	s.buf += delta
	var out strings.Builder
	for {
		if s.inside {
			i := strings.Index(s.buf, thinkClose)
			if i == -1 {
				keep := partialSuffixLen(s.buf, thinkClose)
				s.buf = s.buf[len(s.buf)-keep:]
				break
			}
			s.buf = s.buf[i+len(thinkClose):]
			s.inside = false
		} else {
			i := strings.Index(s.buf, thinkOpen)
			if i == -1 {
				keep := partialSuffixLen(s.buf, thinkOpen)
				cut := len(s.buf) - keep
				out.WriteString(s.buf[:cut])
				s.buf = s.buf[cut:]
				break
			}
			out.WriteString(s.buf[:i])
			s.buf = s.buf[i+len(thinkOpen):]
			s.inside = true
		}
	}
	return out.String()
}

// Flush releases held text; inside an unclosed block nothing is released.
func (s *ThinkStripper) Flush() string {
	if s.inside {
		s.buf = ""
		return ""
	}
	out := s.buf
	s.buf = ""
	return out
}

var (
	thinkBlockRE    = regexp.MustCompile(`(?s)<think>.*?</think>`)
	thinkUnclosedRE = regexp.MustCompile(`(?s)<think>.*$`)
)

// StripThinkText strips think blocks from complete text (unclosed blocks to the end), then
// trims.
func StripThinkText(text string) string {
	text = thinkBlockRE.ReplaceAllString(text, "")
	text = thinkUnclosedRE.ReplaceAllString(text, "")
	return pyStrip(text)
}

// pyStrip is Python's str.strip() (Unicode whitespace).
func pyStrip(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }
