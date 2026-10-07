package tts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// splitSentences ports the legacy Kokoro provider's split pattern, r"(?<=[.!?…])\s+": text
// splits at every whitespace run that follows sentence-ending punctuation, and the punctuation
// stays with its sentence. Pieces are trimmed and empty ones dropped. Like the legacy splitter,
// an abbreviation ("Dr. Smith") splits early; that buys constant first-audio latency.
func splitSentences(text string) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	start := 0
	var prev rune
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if unicode.IsSpace(r) && isSentenceEnd(prev) {
			j := i
			for j < len(text) {
				r2, s2 := utf8.DecodeRuneInString(text[j:])
				if !unicode.IsSpace(r2) {
					break
				}
				j += s2
			}
			add(text[start:i])
			start, i, prev = j, j, ' '
			continue
		}
		prev = r
		i += size
	}
	add(text[start:])
	return out
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == '…'
}
