package hints

import (
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// profile_match.py: restate the speaker-profile lines the utterance mentions, inside the user
// turn (the only position a quantized model reliably attends to).

var profileStopwords = setOf(
	"the", "and", "who", "what", "whats", "when", "where", "why", "how",
	"is", "are", "was", "were", "does", "did", "can", "could", "will",
	"would", "should", "you", "your", "yours", "know", "tell", "about",
	"for", "with", "that", "this", "these", "those", "have", "has", "had",
	"get", "his", "her", "hers", "their", "our", "one", "name", "named",
	"call", "called", "like", "likes", "any", "some", "all", "not",
	"please", "hey", "okay",
)

var askLeads = setOf(
	"who", "whos", "whose", "what", "whats", "which", "when", "where", "why",
	"how", "is", "are", "was", "were", "do", "does", "did", "can", "could",
	"will", "would", "should", "have", "has", "had", "am", "may",
	"tell", "describe", "list", "show", "remind", "explain",
)

// MaxMatchedLines caps the lines one hint restates.
const MaxMatchedLines = 3

var profileWordRE = regexp.MustCompile(`[a-z0-9]+`)

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func contentWords(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range profileWordRE.FindAllString(parse.PyLower(text), -1) {
		if len(w) >= 3 && !profileStopwords[w] {
			out[w] = true
		}
	}
	return out
}

func asksAboutProfile(utterance string) bool {
	text := parse.PyStrip(utterance)
	if strings.HasSuffix(text, "?") {
		return true
	}
	words := profileWordRE.FindAllString(parse.PyLower(text), -1)
	return len(words) > 0 && askLeads[words[0]]
}

// ProfileMatchHint is build_profile_match_hint: a [profile match: …] line restating up to
// MaxMatchedLines "- " lines of speakerBlock (the per-turn speaker system message) that share
// a content word with the utterance; "" when nothing matches. A question gets "answer from it
// directly"; a statement gets "still call whatever tool the request needs".
func ProfileMatchHint(utterance, speakerBlock string) string {
	if utterance == "" || speakerBlock == "" {
		return ""
	}
	query := contentWords(utterance)
	if len(query) == 0 {
		return ""
	}
	var matched []string
	for _, raw := range pySplitLines(speakerBlock) {
		line := parse.PyStrip(raw)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		item := parse.PyStrip(line[2:])
		for w := range contentWords(item) {
			if query[w] {
				matched = append(matched, item)
				break
			}
		}
		if len(matched) >= MaxMatchedLines {
			break
		}
	}
	if len(matched) == 0 {
		return ""
	}
	facts := strings.Join(matched, "; ")
	if asksAboutProfile(utterance) {
		return "[profile match: " + facts + " — this is stored about this speaker; " +
			"answer from it directly]"
	}
	return "[profile match: " + facts + " — this is stored about this speaker; use it " +
		"as context, but still call whatever tool the request needs — don't " +
		"just say you did it]"
}

// pySplitLines is str.splitlines(): \n, \r, \r\n, \v, \f, \x1c-\x1e, \x85,  ,  .
func pySplitLines(s string) []string {
	var out []string
	rs := []rune(s)
	start := 0
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			out = append(out, string(rs[start:i]))
			start = i + 1
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
		}
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}
