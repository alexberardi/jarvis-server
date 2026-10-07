// Package textfilter holds the pure, prompt-byte-relevant text helpers of command-center's
// voice pipeline and tool loop, ported byte-exact from the legacy Python
// (jarvis-command-center/app):
//
//   - core/transcript_filter.py: the transcript SHAPE detectors (noise, device/music/action
//     commands, questions, reports, multi-speaker dashes, named-person addressing) and
//     response_claims_action, which reads the model's reply;
//   - core/tts_text.py clean_for_tts and core/exchange_complete.py apply_to_result;
//   - conversation_handler.py's terminal-filler guard and transient-block prefixes, and the
//     text-mode / continue-stream formatting prompts and fallbacks;
//   - core/utils/think_block_stripper.py and core/streaming_handler.py's sentence splitting
//     and chunk grouping;
//   - core/wake_verification.py's pure parts (wake-phrase fuzzy matching, the leading slice);
//   - services/acknowledgment_service.py (with the M5 word-boundary fix);
//   - core/param_validation.py and the tool-execution engine's dedupe key, keyword gate and
//     nag strings.
//
// Every string here is prompt bytes or spoken text; fixtures/golden/voice (generated from the
// real Python by tools/golden/export_cc_voice.py) is the gate. Python's re module semantics
// (Unicode \w \s \d \b, lookbehind) are reproduced with regexp2 where RE2 differs.
package textfilter

import (
	"strings"

	"github.com/dlclark/regexp2"
)

// Python's Unicode classes for str patterns, spelled out for regexp2.
const (
	pyW = `\p{L}\p{N}_`
	pyS = `\t\n\x0b\f\r\x1c-\x20\x85\xa0  -     　`
	pyD = `\p{Nd}`
)

// translate rewrites a Python pattern's \b, \w, \s and \d into Python's Unicode semantics.
func translate(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '\\' && i+1 < len(p) {
			n := p[i+1]
			i++
			switch {
			case n == 'b' && !inClass:
				b.WriteString(`(?:(?<=[` + pyW + `])(?![` + pyW + `])|(?<![` + pyW + `])(?=[` + pyW + `]))`)
			case n == 'w':
				if inClass {
					b.WriteString(pyW)
				} else {
					b.WriteString(`[` + pyW + `]`)
				}
			case n == 's':
				if inClass {
					b.WriteString(pyS)
				} else {
					b.WriteString(`[` + pyS + `]`)
				}
			case n == 'd':
				if inClass {
					b.WriteString(pyD)
				} else {
					b.WriteString(`[` + pyD + `]`)
				}
			default:
				b.WriteByte('\\')
				b.WriteByte(n)
			}
			continue
		}
		switch c {
		case '[':
			inClass = true
		case ']':
			inClass = false
		}
		b.WriteByte(c)
	}
	return b.String()
}

// pyre is a compiled Python-semantics pattern.
type pyre struct{ re *regexp2.Regexp }

func compile(p string, opts regexp2.RegexOptions) pyre {
	return pyre{regexp2.MustCompile(translate(p), opts)}
}

func mustPy(p string) pyre  { return compile(p, regexp2.None) }
func mustPyI(p string) pyre { return compile(p, regexp2.IgnoreCase) }

// search is re.search(...) is not None (re.match for ^-anchored patterns).
func (r pyre) search(s string) bool {
	ok, err := r.re.MatchString(s)
	return err == nil && ok
}

// findAll counts re.findall matches.
func (r pyre) count(s string) int {
	n := 0
	m, _ := r.re.FindStringMatch(s)
	for m != nil {
		n++
		m, _ = r.re.FindNextMatch(m)
	}
	return n
}

// sub is re.sub(pattern, repl, s) with a $-style replacement.
func (r pyre) sub(s, repl string) string {
	out, err := r.re.Replace(s, repl, -1, -1)
	if err != nil {
		return s
	}
	return out
}

// split is re.split(pattern, s) for a pattern without groups.
func (r pyre) split(s string) []string {
	rs := []rune(s)
	var out []string
	last := 0
	m, _ := r.re.FindRunesMatch(rs)
	for m != nil {
		out = append(out, string(rs[last:m.Index]))
		last = m.Index + m.Length
		m, _ = r.re.FindNextMatch(m)
	}
	return append(out, string(rs[last:]))
}
