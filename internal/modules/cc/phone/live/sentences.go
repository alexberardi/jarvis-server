package live

import (
	"unicode"
	"unicode/utf8"
)

// splitSentences is re.split(r"(?<=[.!?…])\s+", s): split at each whitespace run that follows
// sentence-ending punctuation.
func splitSentences(s string) []string {
	var parts []string
	start := 0
	var prev rune
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) && (prev == '.' || prev == '!' || prev == '?' || prev == '…') {
			end := i
			j := i
			for j < len(s) {
				r2, sz := utf8.DecodeRuneInString(s[j:])
				if !unicode.IsSpace(r2) {
					break
				}
				j += sz
			}
			parts = append(parts, s[start:end])
			start = j
			i = j
			prev = ' '
			continue
		}
		prev = r
		i += size
	}
	return append(parts, s[start:])
}

// SentenceSplitter regroups a delta stream into complete sentences (llm/client.py
// sentences()).
type SentenceSplitter struct{ buf string }

// Feed consumes a delta and returns the sentences it completed (trimmed, non-empty).
func (s *SentenceSplitter) Feed(delta string) []string {
	s.buf += delta
	parts := splitSentences(s.buf)
	if len(parts) <= 1 {
		return nil
	}
	var out []string
	for _, p := range parts[:len(parts)-1] {
		if t := pyStrip(p); t != "" {
			out = append(out, t)
		}
	}
	s.buf = parts[len(parts)-1]
	return out
}

// Flush returns the trailing sentence, if any.
func (s *SentenceSplitter) Flush() []string {
	t := pyStrip(s.buf)
	s.buf = ""
	if t == "" {
		return nil
	}
	return []string{t}
}
