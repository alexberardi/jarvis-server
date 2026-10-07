// Package parse is the text-path output contract of the command-center tool loop
// (docs/cc/02 §3.2 step f, §9; docs/cc/03 §3.2): the Qwen think/<message> stripping, the
// Qwen2.5 provider parse_response chain, ToolCallParser (envelope, singular tool_call, bare
// call recovery, OpenAI function form, # comments, balanced JSON extraction), and the
// <not_for_me/> / <exchange_complete/> sentinels matched outside <think> blocks.
//
// Everything here is pure and golden-tested against fixtures/golden/prompts/_parse.json (G5)
// and _toolparse.json. JSON goes through llm/pyjson so decode errors, key order and the
// json.dumps bytes (", " / ": " separators, ensure_ascii) are CPython's.
package parse

import (
	"strings"
	"unicode"
)

// IsPySpace is Python's str.isspace / the re module's \s for str patterns: Go's
// unicode.IsSpace plus the four information separators \x1c-\x1f.
func IsPySpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// PyStrip is str.strip() with no arguments.
func PyStrip(s string) string { return strings.TrimFunc(s, IsPySpace) }

// PyRStrip is str.rstrip() with no arguments.
func PyRStrip(s string) string { return strings.TrimRightFunc(s, IsPySpace) }

// PyLower is str.lower(): Unicode lowercase, with the one full-mapping special case that
// matters for ASCII-ish keys (U+0130 İ lowers to "i̇", two code points, in Python).
func PyLower(s string) string {
	if !strings.ContainsRune(s, 'İ') {
		return strings.ToLower(s)
	}
	var b strings.Builder
	for _, r := range s {
		if r == 'İ' {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// pySpaceClass is \s of a Python str pattern, spelled out for RE2 (whose \s is ASCII only
// and lacks \v).
const pySpaceClass = `\t\n\x{0b}\f\r\x{1c}-\x{1f} \x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
