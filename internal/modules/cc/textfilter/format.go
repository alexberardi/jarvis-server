package textfilter

import (
	"regexp"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// Tool-result formatting: the text path's single formatting call
// (ConversationHandler._format_tool_result_text_mode, D23) and the continue-stream helpers
// (stream_continue_with_tool_results). Outputs are the node's tool_results[].output values,
// pyjson-decoded (a string, an *pyjson.Object, …).

// ToolOutputString is one output as the formatter sees it: a string as is, anything else
// json.dumps'd (", " / ": " separators, ensure_ascii).
func ToolOutputString(output any) string {
	if s, ok := output.(string); ok {
		return s
	}
	return pyjson.Dumps(output, true)
}

// ToolResultsContext is the "\n"-joined ToolOutputString of every output (tool_context).
func ToolResultsContext(outputs []any) string {
	parts := make([]string, len(outputs))
	for i, o := range outputs {
		parts[i] = ToolOutputString(o)
	}
	return strings.Join(parts, "\n")
}

// asObject is the formatter's output coercion: a JSON-string output is parsed (a parse
// failure becomes {} — or, in the bare-JSON fallback, stays the string); a dict is returned.
func asObject(output any, parseFailEmpty bool) *pyjson.Object {
	if s, ok := output.(string); ok {
		v, err := pyjson.Loads(s)
		if err != nil {
			if parseFailEmpty {
				return pyjson.NewObject()
			}
			return nil
		}
		output = v
	}
	o, _ := output.(*pyjson.Object)
	return o
}

// pyOr is Python's `a or b` over decoded values.
func pyOr(a, b any) any {
	if pyTruthy(a) {
		return a
	}
	return b
}

func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	case float64:
		return x != 0
	}
	return pyjson.Repr(v) != "0"
}

// FastPathMessage is the shared fast path: when EVERY output carries a non-blank string
// "message" (or, when message is falsy, "response"), their stripped values joined by a space;
// ok=false otherwise (an LLM call is needed). The text formatter uses it as is; the
// continue-stream path passes it through CleanForTTS and also requires !IsKnowledgeDelegation.
func FastPathMessage(outputs []any) (string, bool) {
	var parts []string
	for _, out := range outputs {
		o := asObject(out, true)
		if o == nil {
			continue
		}
		msg := pyOr(objGet(o, "message"), objGet(o, "response"))
		if s, ok := msg.(string); ok && parse.PyStrip(s) != "" {
			parts = append(parts, parse.PyStrip(s))
		}
	}
	if len(parts) == 0 || len(parts) != len(outputs) {
		return "", false
	}
	return strings.Join(parts, " "), true
}

// IsKnowledgeDelegation is _is_knowledge_delegation: the tool context parses as one JSON object
// whose keys are exactly {query} or {question}, directly or under "context" (answer_question
// echoes the query; the model answers from its own knowledge).
func IsKnowledgeDelegation(toolContext string) bool {
	v, err := pyjson.Loads(toolContext)
	if err != nil {
		return false
	}
	o, ok := v.(*pyjson.Object)
	if !ok {
		return false
	}
	if onlyKey(o) {
		return true
	}
	ctxv, has := o.Get("context")
	if !has {
		return false
	}
	ctx, ok := ctxv.(*pyjson.Object)
	return ok && onlyKey(ctx)
}

func onlyKey(o *pyjson.Object) bool {
	if o.Len() != 1 {
		return false
	}
	k := o.Keys()[0]
	return k == "query" || k == "question"
}

// knowledgeInstruction is the delegation instruction shared by both paths.
const knowledgeInstruction = "Answer the question from your own knowledge. Be brief and conversational."

// TextModeFormatPrompt is the user message the text-path formatting call appends (after
// dropping role=tool messages): `The user asked: "<q>"\n\n` (when voiceCommand is non-empty —
// the stripped content of the latest user message) + the knowledge or tool-results
// instruction + "\n/no_think". Send it with max_tokens 256, temperature 0.7.
func TextModeFormatPrompt(voiceCommand, toolContext string, knowledge bool) string {
	pre := ""
	if voiceCommand != "" {
		pre = `The user asked: "` + voiceCommand + `"` + "\n\n"
	}
	const suffix = "\n/no_think"
	if knowledge {
		return pre + "Answer the question from your own knowledge. Be brief " +
			"and conversational." + suffix
	}
	return pre + "Tool results below. Answer the user's question DIRECTLY " +
		"using only the fields relevant to what they asked — don't " +
		"list fields they didn't ask about. Use ACTUAL values, " +
		"never placeholders. Be brief and conversational.\n\n" +
		toolContext + suffix
}

var (
	toolCallTagRE  = regexp.MustCompile(`</?tool_call>`)
	thinkCaptureRE = regexp.MustCompile(`(?s)<think>(.*?)</think>`)
)

// StripToolCallTags removes <tool_call> / </tool_call> tags.
func StripToolCallTags(s string) string { return toolCallTagRE.ReplaceAllLiteralString(s, "") }

// FinishFormattedReply is the formatting call's post-processing: tool_call tags removed and
// stripped; <think> bodies collected as reasoning ("" when none); then sanitize (the
// provider's SanitizeText; nil = identity) and CleanForTTS; finally, when the result is a bare
// JSON object with a "name" key (a tool call instead of prose), the tool results' messages
// (or a success:false result's error) joined by a space, or "Done.".
func FinishFormattedReply(content string, sanitize func(string) string, outputs []any) (text, reasoning string) {
	content = parse.PyStrip(StripToolCallTags(content))
	var thinks []string
	for _, m := range thinkCaptureRE.FindAllStringSubmatch(content, -1) {
		if s := parse.PyStrip(m[1]); s != "" {
			thinks = append(thinks, s)
		}
	}
	reasoning = strings.Join(thinks, "\n\n")
	if sanitize != nil {
		content = sanitize(content)
	}
	content = CleanForTTS(content)
	if strings.HasPrefix(content, "{") && strings.HasSuffix(content, "}") {
		if v, err := pyjson.Loads(content); err == nil {
			if o, ok := v.(*pyjson.Object); ok {
				if _, has := o.Get("name"); has {
					var parts []string
					for _, out := range outputs {
						oo := asObject(out, false)
						if oo == nil {
							continue
						}
						if msg := pyOr(objGet(oo, "message"), objGet(oo, "response")); pyTruthy(msg) {
							parts = append(parts, pyjson.Str(msg))
						} else if b, ok := objGet(oo, "success").(bool); ok && !b {
							parts = append(parts, errorOr(oo))
						}
					}
					content = "Done."
					if len(parts) > 0 {
						content = strings.Join(parts, " ")
					}
				}
			}
		}
	}
	return content, reasoning
}

func errorOr(o *pyjson.Object) string {
	if v, has := o.Get("error"); has {
		return pyjson.Str(v)
	}
	return "The command failed."
}

// ContinueToolResultsMessage is the continue-stream text path's user message (role=tool
// messages dropped first): the knowledge instruction, or the tool-results instruction followed
// by the tool context.
func ContinueToolResultsMessage(toolContext string, knowledge bool) string {
	if knowledge {
		return knowledgeInstruction
	}
	return "Here are the tool results. Craft a natural response using the " +
		"ACTUAL values — never use placeholders. If the results are " +
		"empty or contain no items, say so plainly (e.g. \"You have " +
		"nothing on your calendar today\"). Never reply with a bare " +
		"acknowledgment like \"Task completed\", \"Done\", or \"Okay\" — " +
		"always state the actual result. Be brief and conversational.\n\n" +
		toolContext
}

// PlainTextOverride is the system message appended to the continue-stream text path's LLM
// copy only (never cached). Streamed with max_tokens 512, temperature 0.7.
const PlainTextOverride = "Respond naturally in plain text. Do not use JSON format " +
	"or call any tools. Use the tool results above to answer. " +
	"Never reply with a generic acknowledgment like \"Task " +
	"completed\" or \"Done\" — always state the actual result, " +
	"and if there is nothing to report, say so plainly."

// ContinueStreamFastSpoken is the continue-stream fast path: when the outputs are not a
// knowledge delegation and every one carries a message, CleanForTTS of the joined messages;
// "" means take the LLM path. The caller commits the native shape (role=tool kept) or the text
// shape (role=tool dropped) plus the spoken assistant message, then speaks each
// ExtractSentences-style piece of SplitSentenceBoundary(spoken).
func ContinueStreamFastSpoken(outputs []any) string {
	if len(outputs) == 0 || IsKnowledgeDelegation(ToolResultsContext(outputs)) {
		return ""
	}
	msg, ok := FastPathMessage(outputs)
	if !ok {
		return ""
	}
	return CleanForTTS(msg)
}

// LooksUnspeakable is _looks_unspeakable: empty, think residue, or raw JSON — never sent to TTS.
func LooksUnspeakable(text string) bool {
	t := parse.PyStrip(text)
	if t == "" {
		return true
	}
	if strings.Contains(parse.PyLower(t), "<think") {
		return true
	}
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// ToolResultFallback is _tool_result_fallback: spoken text derived from the tool results when
// the streamed reply had nothing usable — each output's message/response, or a success:false
// output's error ("The command failed."), joined, CleanForTTS'd and stripped.
func ToolResultFallback(outputs []any) string {
	var parts []string
	for _, out := range outputs {
		o := asObject(out, true)
		if o == nil {
			continue
		}
		if msg := pyOr(objGet(o, "message"), objGet(o, "response")); pyTruthy(msg) {
			parts = append(parts, pyjson.Str(msg))
		} else if b, ok := objGet(o, "success").(bool); ok && !b {
			parts = append(parts, errorOr(o))
		}
	}
	return parse.PyStrip(CleanForTTS(strings.Join(parts, " ")))
}

// ContinueStreamer is the continue-stream sentence pump: Push each LLM delta and speak the
// sentences it returns; Finish returns the trailing text to speak ("" = none) and the cleaned
// full response to commit ("" = commit nothing unless a fallback is substituted).
type ContinueStreamer struct {
	think  *ThinkStripper
	buf    string
	full   strings.Builder
	Spoken int // sentences handed out so far
}

// NewContinueStreamer uses the provider's think delimiters.
func NewContinueStreamer(thinkStart, thinkEnd string) *ContinueStreamer {
	return &ContinueStreamer{think: NewThinkStripper(thinkStart, thinkEnd)}
}

// Push adds a delta and returns the complete sentences now ready (cleaned for TTS).
func (c *ContinueStreamer) Push(delta string) []string {
	if delta == "" {
		return nil
	}
	c.buf += delta
	c.full.WriteString(delta)
	c.buf = c.think.StripCompleteBlocks(c.buf)
	if c.think.HasOpenBlock(c.buf) {
		return nil
	}
	parts := SplitSentenceBoundary(c.buf)
	if len(parts) < 2 {
		return nil
	}
	c.buf = parts[len(parts)-1]
	var out []string
	for _, p := range parts[:len(parts)-1] {
		if s := CleanForTTS(parse.PyStrip(p)); s != "" {
			c.Spoken++
			out = append(out, s)
		}
	}
	return out
}

// Finish flushes: tail is the trailing partial sentence to speak ("" when there is none or it
// looks unspeakable); commit is the full response with think blocks and tool_call tags removed,
// or "" when it looks unspeakable. When Spoken is still 0 after the tail, the caller
// substitutes ToolResultFallback (speaking and committing it).
func (c *ContinueStreamer) Finish() (tail, commit string) {
	rem := CleanForTTS(parse.PyStrip(StripToolCallTags(c.think.StripCompleteBlocks(c.buf))))
	if rem != "" && !LooksUnspeakable(rem) {
		c.Spoken++
		tail = rem
	}
	commit = parse.PyStrip(StripToolCallTags(c.think.StripCompleteBlocks(c.full.String())))
	if LooksUnspeakable(commit) {
		commit = ""
	}
	return tail, commit
}
