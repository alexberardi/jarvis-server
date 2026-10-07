package llm

import "strings"

// Strict chat templates (ID12, A10 F10). The Qwen 3.5 and 3.8 GGUF templates raise on a
// system message anywhere but first ("System message must be at the beginning") and Qwen
// 3.5's on a conversation without a user query ("No user query found in messages"). CC puts
// per-turn blocks (speaker, ambient, recently shown), retry nags and the continue override in
// later system messages, and its warmup is the system prompt alone, so on those models every
// turn was a 500. For an endpoint flagged FoldSystemMessages (the catalog flags such models;
// llm.<label>.fold_system_messages overrides), the request is reshaped here, right before it
// goes to the engine. The caller's history is never changed, and nothing changes for
// unflagged endpoints.
//
// The rules:
//   - messages[0] stays as is when it is a system message (the byte-exact system prompt, so the
//     engine's prefix cache holds);
//   - every later run of consecutive system messages becomes one context block, merged into the
//     user message right after the run; when none follows directly, appended to the user
//     message right before it; when neither touches the run (after an assistant or tool
//     message), sent as a user message of its own. Order is kept either way;
//   - a request left without a user message (the CC warmup, which only primes the prefix cache)
//     gets PrimeUserMessage as its user turn.
//
// The block is the system text wrapped in <system>…</system> tags on their own lines, several
// messages separated by a blank line, and a blank line between it and the user's own text:
//
//	<system>
//	You are speaking with Alex.
//	</system>
//
//	what time is it
//
// XML-style tags are the delimiter these models are trained on (their templates wrap tools,
// tool results and thinking in <tools>, <tool_response>, <think>) and that the prompts already
// use (<ambient_context>), so the model reads the block as structure rather than as something
// the user said. Naming it "system" tells it the text carries the operator's authority, as the
// system message did. The block can't be mistaken for a tool result: the template treats a
// user message as a query unless it is wrapped in <tool_response> tags.

// PrimeUserMessage is the user turn added to a strict-template request that has none.
const PrimeUserMessage = "Hi."

const (
	foldOpen  = "<system>\n"
	foldClose = "\n</system>"
	foldSep   = "\n\n"
)

// FoldSystemMessages returns msgs reshaped for a strict chat template (see above). msgs and
// the messages it points to are not modified.
func FoldSystemMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs)+1)
	start := 0
	if len(msgs) > 0 && msgs[0].Role == "system" {
		out = append(out, msgs[0])
		start = 1
	}
	var run []string
	flush := func() {
		if len(run) == 0 {
			return
		}
		block := foldOpen + strings.Join(run, foldSep) + foldClose
		run = nil
		if n := len(out); n > 0 && out[n-1].Role == "user" {
			out[n-1] = withText(out[n-1], foldSep+block, false)
			return
		}
		out = append(out, Message{Role: "user", Content: TextContent(block)})
	}
	for _, m := range msgs[start:] {
		if m.Role == "system" {
			run = append(run, contentText(m.Content))
			continue
		}
		if m.Role == "user" && len(run) > 0 {
			block := foldOpen + strings.Join(run, foldSep) + foldClose
			run = nil
			out = append(out, withText(m, block+foldSep, true))
			continue
		}
		flush()
		out = append(out, m)
	}
	flush()
	for _, m := range out {
		if m.Role == "user" {
			return out
		}
	}
	return append(out, Message{Role: "user", Content: TextContent(PrimeUserMessage)})
}

// contentText is a message's text: string content, or its text parts joined (templates reject
// images in system messages anyway).
func contentText(c *Content) string {
	switch {
	case c == nil:
		return ""
	case c.Text != nil:
		return *c.Text
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// withText returns a copy of m with s added before (prepend) or after its content. Structured
// content gets a text part, so images stay where they were.
func withText(m Message, s string, prepend bool) Message {
	c := m.Content
	switch {
	case c == nil:
		m.Content = TextContent(strings.TrimSpace(s))
	case c.Text != nil:
		if prepend {
			m.Content = TextContent(s + *c.Text)
		} else {
			m.Content = TextContent(*c.Text + s)
		}
	default:
		parts := make([]Part, 0, len(c.Parts)+1)
		if prepend {
			parts = append(append(parts, Part{Type: "text", Text: s}), c.Parts...)
		} else {
			parts = append(append(parts, c.Parts...), Part{Type: "text", Text: s})
		}
		m.Content = &Content{Parts: parts}
	}
	return m
}
