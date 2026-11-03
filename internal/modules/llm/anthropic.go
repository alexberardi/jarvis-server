package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// The Anthropic Messages API dialect (remote_api=anthropic): a remote label pointed at
// https://api.anthropic.com is spoken to natively at POST /v1/messages. Everything around
// the client stays OpenAI-shaped (chatPayload in, completion / streamResult out); this file
// is the seam. The rules follow the translation in jarvis-llm-proxy-api#75.
//
// Outbound: system messages become the top-level "system", tool calls and tool results
// become tool_use / tool_result blocks, consecutive same-role messages merge (parallel tool
// results must share one user message), max_tokens is always sent, and the sampling knobs
// are not (current Claude models reject temperature/top_p).
// Inbound: text blocks concatenate, tool_use blocks become tool calls with JSON-string
// arguments, stop_reason maps onto finish_reason, and cache tokens count as prompt tokens.

// anthropicVersion is the anthropic-version header every request carries.
const anthropicVersion = "2023-06-01"

// anthropicDefaultMaxTokens is max_tokens when the request sets none: the Messages API
// requires it and the module has no deployment-wide default.
const anthropicDefaultMaxTokens = 4096

// alwaysThinkingModels: thinking is always on for these and {"type":"disabled"} is a 400, so
// the key is omitted. Their tool round trips would need thinking-block replay, which the
// OpenAI-shaped history can't carry: not supported.
var alwaysThinkingModels = []string{"fable", "mythos"}

// anthropicRequest is the POST /v1/messages body.
type anthropicRequest struct {
	Model      string             `json:"model"`
	System     string             `json:"system,omitempty"`
	Messages   []anthropicMessage `json:"messages"`
	MaxTokens  int                `json:"max_tokens"`
	Stream     bool               `json:"stream"`
	Thinking   *anthropicThinking `json:"thinking,omitempty"`
	Tools      []anthropicTool    `json:"tools,omitempty"`
	ToolChoice json.RawMessage    `json:"tool_choice,omitempty"`
}

type anthropicThinking struct {
	Type string `json:"type"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicBlock is one request content block: text, image, tool_use or tool_result.
type anthropicBlock struct {
	Type      string           `json:"type"`
	Text      string           `json:"text,omitempty"`
	Source    *anthropicSource `json:"source,omitempty"`
	ID        string           `json:"id,omitempty"`
	Name      string           `json:"name,omitempty"`
	Input     json.RawMessage  `json:"input,omitempty"`
	ToolUseID string           `json:"tool_use_id,omitempty"`
	// Content is a tool_result's content: a string, or text and image blocks.
	Content any `json:"content,omitempty"`
}

type anthropicSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// anthropicBody translates an OpenAI-shaped payload into a Messages body. A tool message
// without tool_call_id, or nothing left to send, is the caller's 400.
func anthropicBody(p *chatPayload) (*anthropicRequest, error) {
	system, msgs, err := anthropicMessages(p.Messages)
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, apiErr(400, "invalid_request_error", "Anthropic request needs at least one user or assistant message")
	}
	r := &anthropicRequest{Model: p.Model, System: system, Messages: msgs, MaxTokens: anthropicDefaultMaxTokens, Stream: p.Stream}
	if p.MaxTokens != nil && *p.MaxTokens > 0 {
		r.MaxTokens = *p.MaxTokens
	}
	if !alwaysThinks(p.Model) {
		// Omitted, Claude 5 models think adaptively, and thinking blocks would then have to be
		// replayed on the tool-result round trip.
		r.Thinking = &anthropicThinking{Type: "disabled"}
	}
	if len(p.Tools) > 0 {
		r.Tools = anthropicTools(p.Tools)
		r.ToolChoice = anthropicToolChoice(p.ToolChoice)
	}
	return r, nil
}

func alwaysThinks(model string) bool {
	m := strings.ToLower(model)
	return slices.ContainsFunc(alwaysThinkingModels, func(s string) bool { return strings.Contains(m, s) })
}

// anthropicMessages splits OpenAI messages into the system text and Messages turns.
func anthropicMessages(in []Message) (string, []anthropicMessage, error) {
	var system []string
	var out []anthropicMessage
	add := func(role string, blocks []anthropicBlock) {
		switch {
		case len(blocks) == 0:
			// An empty message is a 400.
		case len(out) == 0 && role == "assistant":
			// The first message must be the user's; a replayed greeting is dropped.
		case len(out) > 0 && out[len(out)-1].Role == role:
			out[len(out)-1].Content = append(out[len(out)-1].Content, blocks...)
		default:
			out = append(out, anthropicMessage{Role: role, Content: blocks})
		}
	}
	for _, m := range in {
		switch m.Role {
		case "system":
			if t := contentText(m.Content); strings.TrimSpace(t) != "" {
				system = append(system, t)
			}
		case "tool":
			if m.ToolCallID == "" {
				return "", nil, apiErr(400, "invalid_request_error",
					"Anthropic tool_result requires tool_call_id; a tool message arrived without one")
			}
			add("user", []anthropicBlock{{Type: "tool_result", ToolUseID: m.ToolCallID, Content: toolResultContent(m.Content)}})
		case "assistant":
			blocks := anthropicBlocks(m.Content, false)
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					// An empty id is a 400; keep the turn rather than fail it.
					id = fmt.Sprintf("toolu_generated_%d_%d", len(out), len(blocks))
				}
				blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: id, Name: tc.Function.Name,
					Input: toolInput(tc.Function.Arguments)})
			}
			add("assistant", blocks)
		default:
			add("user", anthropicBlocks(m.Content, true))
		}
	}
	return strings.Join(system, "\n\n"), out, nil
}

// anthropicBlocks turns content into text blocks (and image blocks when images is set).
// Empty text yields no block: an empty text block is a 400. Images are data URLs by now
// (checkMessages refused anything else).
func anthropicBlocks(c *Content, images bool) []anthropicBlock {
	if c == nil {
		return nil
	}
	if c.Text != nil {
		if *c.Text == "" {
			return nil
		}
		return []anthropicBlock{{Type: "text", Text: *c.Text}}
	}
	var out []anthropicBlock
	for _, p := range c.Parts {
		switch {
		case p.Type == "text" && p.Text != "":
			out = append(out, anthropicBlock{Type: "text", Text: p.Text})
		case p.Type == "image_url" && p.ImageURL != nil && images:
			sub := dataURL.FindStringSubmatch(strings.TrimSuffix(p.ImageURL.URL, "\n"))
			if sub == nil {
				continue
			}
			out = append(out, anthropicBlock{Type: "image",
				Source: &anthropicSource{Type: "base64", MediaType: sub[1], Data: stripB64(sub[2])}})
		}
	}
	return out
}

// toolResultContent is a tool message's content as a tool_result's: a string, or blocks.
func toolResultContent(c *Content) any {
	if c == nil {
		return ""
	}
	if c.Text != nil {
		return *c.Text
	}
	if b := anthropicBlocks(c, true); len(b) > 0 {
		return b
	}
	return ""
}

// toolInput decodes OpenAI arguments for a tool_use block's input. Models send "" for a
// no-argument call and sometimes truncated JSON; both degrade to {}.
func toolInput(args string) json.RawMessage {
	if obj := jsonObject([]byte(args)); obj != nil {
		return obj
	}
	return json.RawMessage(`{}`)
}

// jsonObject returns b compacted when it is a JSON object, else nil.
func jsonObject(b []byte) json.RawMessage {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' || !json.Valid(b) {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return nil
	}
	return buf.Bytes()
}

// anthropicTools maps {type:function,function:{name,description,parameters}} to
// {name,description,input_schema}. A tool already in Anthropic's shape passes through; one
// without a name is dropped.
func anthropicTools(tools []Tool) []anthropicTool {
	var out []anthropicTool
	for _, t := range tools {
		fn := t.Function
		schema := jsonObject(fn.Parameters)
		if fn.Name == "" && len(t.Raw) > 0 {
			var bare struct {
				Function    ToolFunction    `json:"function"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				InputSchema json.RawMessage `json:"input_schema"`
			}
			switch {
			case json.Unmarshal(t.Raw, &bare) != nil:
			case bare.Function.Name != "": // OpenAI-shaped Raw with Function unset (cc's warmup)
				fn, schema = bare.Function, jsonObject(bare.Function.Parameters)
			default:
				fn = ToolFunction{Name: bare.Name, Description: bare.Description}
				if schema = jsonObject(bare.Parameters); schema == nil {
					schema = jsonObject(bare.InputSchema)
				}
			}
		}
		if fn.Name == "" {
			continue
		}
		if schema == nil {
			schema = emptySchema
		}
		out = append(out, anthropicTool{Name: fn.Name, Description: fn.Description, InputSchema: schema})
	}
	return out
}

// anthropicToolChoice maps tool_choice: auto/none as is, required → any, a named function →
// {type:tool,name}. Absent stays absent (the model's default, auto).
func anthropicToolChoice(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	choice := func(typ string) json.RawMessage { return json.RawMessage(`{"type":"` + typ + `"}`) }
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(s) {
		case "required", "any":
			return choice("any")
		case "none":
			return choice("none")
		}
		return choice("auto")
	}
	var o struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return choice("auto")
	}
	named := func(name string) json.RawMessage {
		b, _ := json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{"tool", name})
		return b
	}
	switch o.Type {
	case "function":
		if o.Function.Name != "" {
			return named(o.Function.Name)
		}
		return choice("any")
	case "tool":
		if o.Name != "" {
			return named(o.Name)
		}
		return choice("any") // "call some tool" is the nearest legal intent
	case "auto", "any", "none":
		return choice(o.Type)
	}
	return choice("auto")
}

// --- responses ---

// anthropicUsage is a Messages usage object. Cache reads and writes are billed input
// reported apart from input_tokens.
type anthropicUsage struct {
	InputTokens   *int `json:"input_tokens"`
	OutputTokens  *int `json:"output_tokens"`
	CacheRead     *int `json:"cache_read_input_tokens"`
	CacheCreation *int `json:"cache_creation_input_tokens"`
}

// usageTally keeps the four counters apart, so a later event can update one without
// losing the others.
type usageTally struct{ input, output, cacheRead, cacheCreation int }

// merge takes each counter the event carries. The counts are cumulative, so the larger
// value wins: a message_delta repeating input_tokens without the cache fields keeps the
// cache counts message_start reported.
func (t *usageTally) merge(u *anthropicUsage) {
	if u == nil {
		return
	}
	take := func(dst *int, v *int) {
		if v != nil && *v > *dst {
			*dst = *v
		}
	}
	take(&t.input, u.InputTokens)
	take(&t.output, u.OutputTokens)
	take(&t.cacheRead, u.CacheRead)
	take(&t.cacheCreation, u.CacheCreation)
}

func (t usageTally) usage() Usage {
	prompt := t.input + t.cacheRead + t.cacheCreation
	return Usage{PromptTokens: prompt, CompletionTokens: t.output, TotalTokens: prompt + t.output}
}

// finishReason maps stop_reason. end_turn, stop_sequence, pause_turn and none read as stop.
func finishReason(stop string) string {
	switch stop {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "content_filter"
	}
	return "stop"
}

// logStop warns on the stop reasons that mean the turn didn't really finish.
func logStop(log *slog.Logger, stop string, details json.RawMessage) {
	switch stop {
	case "refusal":
		log.Warn("llm: anthropic stop_reason=refusal (the model declined to continue)", "stop_details", string(details))
	case "pause_turn":
		log.Warn("llm: anthropic stop_reason=pause_turn (the turn was paused and would need continuing); treated as stop")
	}
}

// toolArguments is a tool_use input as an OpenAI arguments string; a non-object is {}.
func toolArguments(input json.RawMessage) string {
	if obj := jsonObject(input); obj != nil {
		return string(obj)
	}
	return "{}"
}

func toolUseID(id string) string {
	if id == "" {
		return "call_" + newHex(12)
	}
	return id
}

type anthropicResponseBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// completeAnthropic runs one non-stream completion against the Messages API.
func (s *Service) completeAnthropic(ctx context.Context, ep Endpoint, p *chatPayload) (completion, error) {
	body, err := anthropicBody(p)
	if err != nil {
		return completion{}, err
	}
	resp, err := s.post(ctx, ep, "/messages", body)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	return parseAnthropicMessage(resp.Body, s.log)
}

// parseAnthropicMessage reads a non-stream Messages response: text blocks concatenate in
// order, every tool_use becomes a tool call, thinking blocks are skipped.
func parseAnthropicMessage(r io.Reader, log *slog.Logger) (completion, error) {
	var out struct {
		Content     []anthropicResponseBlock `json:"content"`
		StopReason  string                   `json:"stop_reason"`
		StopDetails json.RawMessage          `json:"stop_details"`
		Usage       *anthropicUsage          `json:"usage"`
	}
	if err := json.NewDecoder(r).Decode(&out); err != nil {
		return completion{}, fmt.Errorf("Invalid JSON response: %w", err)
	}
	var c completion
	var sb strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			sb.WriteString(b.Text)
		case "tool_use":
			c.ToolCalls = append(c.ToolCalls, ToolCall{ID: toolUseID(b.ID), Type: "function",
				Function: FunctionCall{Name: b.Name, Arguments: toolArguments(b.Input)}})
		}
	}
	c.Content = sb.String()
	var t usageTally
	t.merge(out.Usage)
	c.Usage = t.usage()
	c.FinishReason = finishReason(out.StopReason)
	logStop(log, out.StopReason, out.StopDetails)
	return c, nil
}

// anthropicEvent is one streamed event; which fields are set depends on Type.
type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *anthropicResponseBlock `json:"content_block"`
	Delta        *struct {
		Type        string          `json:"type"`
		Text        string          `json:"text"`
		PartialJSON string          `json:"partial_json"`
		StopReason  string          `json:"stop_reason"`
		StopDetails json.RawMessage `json:"stop_details"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// streamBlock gathers one content block, by index.
type streamBlock struct {
	typ, id, name string
	input         json.RawMessage // content_block_start's input
	text          strings.Builder
	json          strings.Builder
}

// errNoMessageStop: the stream ended without message_stop (a cut connection, not an end).
var errNoMessageStop = errors.New("Anthropic stream ended before message_stop")

// parseAnthropicStream folds the Messages SSE events into a streamResult, calling onDelta
// per text delta. Blocks are keyed by index, a tool call's input_json_delta fragments are
// only JSON once joined, ping may come anywhere, there is no [DONE], and an error event
// fails the stream.
func parseAnthropicStream(r io.Reader, onDelta func(string) bool, log *slog.Logger) (streamResult, error) {
	res := streamResult{FinishReason: "stop"}
	blocks := map[int]*streamBlock{}
	var tally usageTally
	var stop string
	var stopDetails json.RawMessage
	stopped := false
	block := func(i int) *streamBlock {
		b := blocks[i]
		if b == nil {
			b = &streamBlock{typ: "text"}
			blocks[i] = b
		}
		return b
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	sc.Split(scanLines)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue // event: lines repeat the JSON's type
		}
		var ev anthropicEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &ev); err != nil {
			continue
		}
		if ev.Index < 0 || ev.Index > 1024 {
			continue
		}
		switch ev.Type {
		case "error":
			typ, msg := "error", "unknown error"
			if ev.Error != nil {
				if ev.Error.Type != "" {
					typ = ev.Error.Type
				}
				if ev.Error.Message != "" {
					msg = ev.Error.Message
				}
			}
			return res, fmt.Errorf("Anthropic stream error (%s): %s", typ, msg)
		case "message_start":
			if ev.Message != nil {
				tally.merge(ev.Message.Usage)
			}
		case "content_block_start":
			b := &streamBlock{typ: "text"}
			if cb := ev.ContentBlock; cb != nil {
				b.typ, b.id, b.name, b.input = cb.Type, cb.ID, cb.Name, cb.Input
				if cb.Type == "text" && cb.Text != "" {
					b.text.WriteString(cb.Text)
					if !onDelta(cb.Text) {
						return res, context.Canceled
					}
				}
			}
			blocks[ev.Index] = b
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			b := block(ev.Index)
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text == "" || b.typ != "text" {
					continue
				}
				b.text.WriteString(ev.Delta.Text)
				if !onDelta(ev.Delta.Text) {
					return res, context.Canceled
				}
			case "input_json_delta":
				b.json.WriteString(ev.Delta.PartialJSON)
			}
			// thinking_delta, signature_delta, …: not content.
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stop, stopDetails = ev.Delta.StopReason, ev.Delta.StopDetails
			}
			tally.merge(ev.Usage)
		case "message_stop":
			stopped = true
		}
		// content_block_stop, ping and unknown events carry nothing to keep.
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	idx := make([]int, 0, len(blocks))
	for i := range blocks {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	var sb strings.Builder
	for _, i := range idx {
		b := blocks[i]
		switch b.typ {
		case "text":
			sb.WriteString(b.text.String())
		case "tool_use":
			args := b.json.String()
			if strings.TrimSpace(args) == "" {
				args = string(b.input)
			}
			id, name := toolUseID(b.id), b.name
			res.ToolCalls = append(res.ToolCalls, StreamToolCall{ID: &id, Type: "function", Name: &name,
				Arguments: toolArguments(json.RawMessage(args))})
		}
	}
	res.Content = sb.String()
	res.Usage = tally.usage()
	res.FinishReason = finishReason(stop)
	if !stopped {
		return res, errNoMessageStop
	}
	logStop(log, stop, stopDetails)
	return res, nil
}

// anthropicErrorMessage makes an error body readable: {"type":"error","error":{"type",
// "message"}} becomes "type: message"; anything else is returned as is.
func anthropicErrorMessage(body []byte) string {
	var e struct {
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil || e.Error == nil || e.Error.Message == "" {
		return string(body)
	}
	if e.Error.Type == "" {
		return e.Error.Message
	}
	return e.Error.Type + ": " + e.Error.Message
}
