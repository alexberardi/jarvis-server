package textfilter

import (
	"regexp"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/dlclark/regexp2"
)

// core/tts_text.py: emoji and markdown presentation syntax stripped before TTS.

var emojiRE = regexp.MustCompile(`[` +
	`\x{1F300}-\x{1F5FF}` +
	`\x{1F600}-\x{1F64F}` +
	`\x{1F680}-\x{1F6FF}` +
	`\x{1F700}-\x{1F77F}` +
	`\x{1F780}-\x{1F7FF}` +
	`\x{1F800}-\x{1F8FF}` +
	`\x{1F900}-\x{1F9FF}` +
	`\x{1FA00}-\x{1FA6F}` +
	`\x{1FA70}-\x{1FAFF}` +
	`\x{2600}-\x{26FF}` +
	`\x{2700}-\x{27BF}` +
	`\x{2B00}-\x{2BFF}` +
	`\x{2300}-\x{23FF}` +
	`\x{1F1E6}-\x{1F1FF}` +
	`\x{FE0F}` +
	`\x{200D}` +
	`\x{20E3}` +
	`]+`)

type markdownSub struct {
	re   pyre
	repl string
}

// markdownSubs are _MARKDOWN_SUBS, applied in order.
var markdownSubs = []markdownSub{
	{compile("```[a-zA-Z0-9_-]*\\n?(.*?)```", regexp2.Singleline), "$1"},
	{mustPy("`([^`\\n]+)`"), "$1"},
	{mustPy(`!\[[^\]]*\]\([^)]+\)`), ""},
	{mustPy(`\[([^\]]+)\]\([^)]+\)`), "$1"},
	{mustPy(`\*\*\*([^\*\n]+?)\*\*\*`), "$1"},
	{mustPy(`\*\*([^\*\n]+?)\*\*`), "$1"},
	{mustPy(`\*([^\*\n]+?)\*`), "$1"},
	{mustPy(`__([^_\n]+?)__`), "$1"},
	{mustPy(`(?<![A-Za-z0-9])_([^_\n]+?)_(?![A-Za-z0-9])`), "$1"},
	{mustPy(`~~([^~\n]+?)~~`), "$1"},
	{compile(`^[ \t]*#{1,6}[ \t]+`, regexp2.Multiline), ""},
	{compile(`^[ \t]*>[ \t]?`, regexp2.Multiline), ""},
	{compile(`^[ \t]*[-\*\+][ \t]+`, regexp2.Multiline), ""},
	{compile(`^[ \t]*\d+\.[ \t]+`, regexp2.Multiline), ""},
	{compile(`^[ \t]*[-\*_]{3,}[ \t]*$`, regexp2.Multiline), ""},
}

var (
	orphanMarkdownRE = mustPy("(?<!\\w)[\\*~`]+|[\\*~`]+(?!\\w)")
	multiSpaceRE     = regexp.MustCompile(`[ \t]{2,}`)
	manyNewlinesRE   = regexp.MustCompile(`\n{3,}`)
)

// CleanForTTS is clean_for_tts: the <exchange_complete/> marker, emoji and markdown syntax
// removed, double spaces collapsed and the result stripped. Text needing no change is
// returned unchanged (including its surrounding whitespace).
func CleanForTTS(text string) string {
	if text == "" {
		return text
	}
	cleaned := text
	if parse.ContainsExchangeComplete(text) {
		cleaned = parse.StripExchangeComplete(text)
	}
	cleaned = emojiRE.ReplaceAllLiteralString(cleaned, "")
	for _, s := range markdownSubs {
		cleaned = s.re.sub(cleaned, s.repl)
	}
	cleaned = orphanMarkdownRE.sub(cleaned, "")
	if cleaned == text {
		return text
	}
	cleaned = multiSpaceRE.ReplaceAllLiteralString(cleaned, " ")
	cleaned = manyNewlinesRE.ReplaceAllLiteralString(cleaned, "\n\n")
	return parse.PyStrip(cleaned)
}

// ApplyExchangeComplete is exchange_complete.apply_to_result for one final result: unless the
// stop reason is not_for_me, a message carrying <exchange_complete/> is returned stripped with
// endOfExchange true; otherwise the message is returned unchanged.
func ApplyExchangeComplete(stopReason, message string) (out string, endOfExchange bool) {
	if stopReason == "not_for_me" || !parse.ContainsExchangeComplete(message) {
		return message, false
	}
	return parse.StripExchangeComplete(message), true
}
