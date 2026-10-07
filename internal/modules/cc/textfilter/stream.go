package textfilter

import (
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
)

// ThinkStripper is core/utils/think_block_stripper.ThinkBlockStripper for one delimiter pair:
// it strips complete chain-of-thought spans before TTS and detects an unclosed one.
type ThinkStripper struct {
	start, end string
	block      *regexp.Regexp
	unclosed   *regexp.Regexp
}

// NewThinkStripper builds a stripper for (start, end), e.g. prompts.Provider.ThinkDelimiters().
func NewThinkStripper(start, end string) *ThinkStripper {
	return &ThinkStripper{
		start:    start,
		end:      end,
		block:    regexp.MustCompile(`(?s)` + regexp.QuoteMeta(start) + `.*?` + regexp.QuoteMeta(end) + `[` + pySpaceRE2 + `]*`),
		unclosed: regexp.MustCompile(`(?s)` + regexp.QuoteMeta(start) + `.*`),
	}
}

// pySpaceRE2 is Python's \s as an RE2 class body.
const pySpaceRE2 = `\t\n\x{0b}\f\r\x{1c}-\x{20}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// StripCompleteBlocks removes every balanced start…end span (and the whitespace after it).
func (t *ThinkStripper) StripCompleteBlocks(text string) string {
	return t.block.ReplaceAllLiteralString(text, "")
}

// HasOpenBlock reports a start marker with no end marker anywhere in text.
func (t *ThinkStripper) HasOpenBlock(text string) bool {
	return strings.Contains(text, t.start) && !strings.Contains(text, t.end)
}

// StripAll removes complete blocks, then any unclosed span to end of text.
func (t *ThinkStripper) StripAll(text string) string {
	return t.unclosed.ReplaceAllLiteralString(t.block.ReplaceAllLiteralString(text, ""), "")
}

// sentenceBoundaryRE is r"(?<=[.!?])\s+".
var sentenceBoundaryRE = mustPy(`(?<=[.!?])\s+`)

// SplitSentenceBoundary is re.split(r"(?<=[.!?])\s+", text): pieces are not stripped and the
// last one may be a partial sentence (the streaming paths keep it buffered).
func SplitSentenceBoundary(text string) []string { return sentenceBoundaryRE.split(text) }

// ExtractSentences is streaming_handler.extract_sentences: the split pieces, stripped, empty
// ones dropped.
func ExtractSentences(text string) []string {
	var out []string
	for _, p := range SplitSentenceBoundary(text) {
		if s := parse.PyStrip(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sentencesPerChunk is _SENTENCES_PER_CHUNK.
const sentencesPerChunk = 4

// SpeechGroups is stream_text_as_audio's grouping: each text is one TTS synthesis, in order.
// Up to two sentences are spoken one per synthesis; longer text is grouped into chunks of
// four sentences joined by a space (synthesis of chunk N+1 may be prefetched while N plays).
// Empty for text with no sentences.
func SpeechGroups(text string) []string {
	sentences := ExtractSentences(text)
	if len(sentences) <= 2 {
		return sentences
	}
	var out []string
	for i := 0; i < len(sentences); i += sentencesPerChunk {
		end := min(i+sentencesPerChunk, len(sentences))
		out = append(out, strings.Join(sentences[i:end], " "))
	}
	return out
}
