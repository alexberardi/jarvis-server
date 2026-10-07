package llm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func sys(s string) Message  { return Message{Role: "system", Content: TextContent(s)} }
func usr(s string) Message  { return Message{Role: "user", Content: TextContent(s)} }
func asst(s string) Message { return Message{Role: "assistant", Content: TextContent(s)} }

// strictTemplateOK applies the two rules the Qwen 3.5 template raises on: a system message
// anywhere but first, and no user query (a user message not wrapped in <tool_response>).
func strictTemplateOK(t *testing.T, msgs []Message) {
	t.Helper()
	query := false
	for i, m := range msgs {
		if m.Role == "system" && i > 0 {
			t.Fatalf("system message at %d: %s", i, dump(msgs))
		}
		if m.Role == "user" {
			c := strings.TrimSpace(contentText(m.Content))
			if !(strings.HasPrefix(c, "<tool_response>") && strings.HasSuffix(c, "</tool_response>")) {
				query = true
			}
		}
	}
	if !query {
		t.Fatalf("no user query: %s", dump(msgs))
	}
}

func dump(msgs []Message) string {
	b, _ := json.Marshal(msgs)
	return string(b)
}

func roles(msgs []Message) string {
	var r []string
	for _, m := range msgs {
		r = append(r, m.Role)
	}
	return strings.Join(r, ",")
}

func TestFoldSystemMessages(t *testing.T) {
	call := Message{Role: "assistant", Content: TextContent(""),
		ToolCalls: []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "get_weather", Arguments: "{}"}}}}
	tool := Message{Role: "tool", ToolCallID: "c1", Content: TextContent(`{"temp": 20}`)}
	cases := []struct {
		name string
		in   []Message
		want []Message
	}{
		{
			// CC's turn layout: per-turn blocks between the history and the utterance.
			name: "turn blocks fold into the next user message, in order",
			in:   []Message{sys("PROMPT"), usr("hi"), asst("hello"), sys("You are speaking with Alex."), sys("<ambient_context>\nsunny\n</ambient_context>"), usr("what time is it")},
			want: []Message{sys("PROMPT"), usr("hi"), asst("hello"),
				usr("<system>\nYou are speaking with Alex.\n\n<ambient_context>\nsunny\n</ambient_context>\n</system>\n\nwhat time is it")},
		},
		{
			name: "a retry nag after an assistant message becomes a user message",
			in:   []Message{sys("PROMPT"), usr("turn on the lights"), asst("Sure!"), sys("You MUST call a tool.")},
			want: []Message{sys("PROMPT"), usr("turn on the lights"), asst("Sure!"), usr("<system>\nYou MUST call a tool.\n</system>")},
		},
		{
			name: "a nag after tool results becomes a user message; tool messages are untouched",
			in:   []Message{sys("PROMPT"), usr("weather?"), call, tool, sys("Don't call get_weather again.")},
			want: []Message{sys("PROMPT"), usr("weather?"), call, tool, usr("<system>\nDon't call get_weather again.\n</system>")},
		},
		{
			// The continue-stream override follows the tool-results user message.
			name: "a block right after the last user message is appended to it",
			in:   []Message{sys("PROMPT"), usr("Tool results: 20C"), sys("Answer in plain text.")},
			want: []Message{sys("PROMPT"), usr("Tool results: 20C\n\n<system>\nAnswer in plain text.\n</system>")},
		},
		{
			name: "the following user message wins over the preceding one",
			in:   []Message{sys("PROMPT"), usr("a"), sys("X"), usr("b")},
			want: []Message{sys("PROMPT"), usr("a"), usr("<system>\nX\n</system>\n\nb")},
		},
		{
			name: "several runs fold separately",
			in:   []Message{sys("PROMPT"), usr("a"), asst("b"), sys("N1"), asst("c"), sys("S1"), sys("S2"), usr("d")},
			want: []Message{sys("PROMPT"), usr("a"), asst("b"), usr("<system>\nN1\n</system>"), asst("c"), usr("<system>\nS1\n\nS2\n</system>\n\nd")},
		},
		{
			name: "a second leading system message is folded too",
			in:   []Message{sys("PROMPT"), sys("JSON only."), usr("q")},
			want: []Message{sys("PROMPT"), usr("<system>\nJSON only.\n</system>\n\nq")},
		},
		{
			name: "a system message after a leading user message",
			in:   []Message{usr("q"), sys("X")},
			want: []Message{usr("q\n\n<system>\nX\n</system>")},
		},
		{
			// CC's warmup: the system prompt alone, to prime the prefix cache.
			name: "warmup gets a user turn",
			in:   []Message{sys("PROMPT")},
			want: []Message{sys("PROMPT"), usr(PrimeUserMessage)},
		},
		{
			name: "an assistant-only tail gets a user turn",
			in:   []Message{sys("PROMPT"), asst("x")},
			want: []Message{sys("PROMPT"), asst("x"), usr(PrimeUserMessage)},
		},
		{
			name: "a compliant list is unchanged",
			in:   []Message{sys("PROMPT"), usr("a"), call, tool, asst("20C"), usr("b")},
			want: []Message{sys("PROMPT"), usr("a"), call, tool, asst("20C"), usr("b")},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := dump(c.in)
			got := FoldSystemMessages(c.in)
			if dump(got) != dump(c.want) {
				t.Fatalf("got  %s\nwant %s", dump(got), dump(c.want))
			}
			if dump(c.in) != before {
				t.Fatal("input modified")
			}
			strictTemplateOK(t, got)
		})
	}
}

func TestFoldKeepsSystemPromptBytes(t *testing.T) {
	prompt := "  PROMPT with trailing space and\nnewlines\n\n"
	got := FoldSystemMessages([]Message{sys(prompt), sys("X"), usr("q")})
	if got[0].Content.Text == nil || *got[0].Content.Text != prompt {
		t.Fatalf("system prompt changed: %q", contentText(got[0].Content))
	}
}

func TestFoldStructuredContent(t *testing.T) {
	img := Part{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AAAA"}}
	user := Message{Role: "user", Content: &Content{Parts: []Part{img, {Type: "text", Text: "what is this?"}}}}
	sysParts := Message{Role: "system", Content: &Content{Parts: []Part{{Type: "text", Text: "Be "}, {Type: "text", Text: "brief."}}}}
	got := FoldSystemMessages([]Message{sys("PROMPT"), sysParts, user})
	if roles(got) != "system,user" {
		t.Fatalf("roles %s", roles(got))
	}
	parts := got[1].Content.Parts
	if len(parts) != 3 || parts[0].Text != "<system>\nBe brief.\n</system>\n\n" || !reflect.DeepEqual(parts[1], img) || parts[2].Text != "what is this?" {
		t.Fatalf("parts %s", dump(got))
	}
	if len(user.Content.Parts) != 2 {
		t.Fatal("input parts modified")
	}

	// Appended to a preceding structured user message.
	got = FoldSystemMessages([]Message{sys("PROMPT"), user, sys("X")})
	parts = got[1].Content.Parts
	if len(parts) != 3 || parts[2].Text != "\n\n<system>\nX\n</system>" {
		t.Fatalf("parts %s", dump(got))
	}
}

func TestFoldOnlyForFlaggedEndpoints(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "PROMPT"},
		map[string]any{"role": "system", "content": "You are speaking with Alex."},
		map[string]any{"role": "user", "content": "hi"},
	}
	e := setup(t)
	if r := e.post("/v1/chat/completions", map[string]any{"model": "live", "messages": msgs}); r.status != 200 {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	if got := roleList(e.engine.last()); got != "system,system,user" {
		t.Fatalf("unflagged endpoint: roles %s", got)
	}

	e.res.set(LabelLive, Endpoint{BaseURL: e.engine.srv.URL, Model: "qwen-live.gguf", FoldSystemMessages: true}, nil)
	if r := e.post("/v1/chat/completions", map[string]any{"model": "live", "messages": msgs}); r.status != 200 {
		t.Fatalf("status %d %s", r.status, r.body)
	}
	last := e.engine.last()
	if got := roleList(last); got != "system,user" {
		t.Fatalf("flagged endpoint: roles %s", got)
	}
	m := last["messages"].([]any)
	if c := m[1].(map[string]any)["content"]; c != "<system>\nYou are speaking with Alex.\n</system>\n\nhi" {
		t.Fatalf("folded content %q", c)
	}

	// Streams fold too, and a system-only request (CC's warmup) gets its user turn.
	frames, err := e.m.Service().Stream(e.ctx, ChatRequest{Label: LabelLive, Messages: []Message{sys("PROMPT")}})
	if err != nil {
		t.Fatal(err)
	}
	for range frames {
	}
	if got := roleList(e.engine.last()); got != "system,user" {
		t.Fatalf("stream: roles %s", got)
	}
}

func roleList(req map[string]any) string {
	var r []string
	for _, m := range req["messages"].([]any) {
		r = append(r, m.(map[string]any)["role"].(string))
	}
	return strings.Join(r, ",")
}
