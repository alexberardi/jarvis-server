package llm

import (
	"bytes"
	"encoding/json"
	"time"
)

// The standard OpenAI stream, served on /openai/v1/chat/completions (opt-in; the legacy
// route keeps the frozen Jarvis frames). Decision: docs/decisions/2026-10-16-opt-in-openai-standard-streaming.md.

type chunkJSON struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chunkChoiceJSON `json:"choices"`
	// Usage is null on every chunk but the trailing usage chunk, and absent unless
	// stream_options.include_usage was set (as OpenAI does).
	Usage json.RawMessage `json:"usage,omitempty"`
}

type chunkChoiceJSON struct {
	Index        int            `json:"index"`
	Delta        chunkDeltaJSON `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chunkDeltaJSON struct {
	Role      string              `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls []chunkToolCallJSON `json:"tool_calls,omitempty"`
}

type chunkToolCallJSON struct {
	Index    int                   `json:"index"`
	ID       string                `json:"id"`
	Type     string                `json:"type"`
	Function chunkToolFunctionJSON `json:"function"`
}

type chunkToolFunctionJSON struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chunkEncoder turns a stream's Frames into chat.completion.chunk payloads (each written as
// "data: <payload>\n\n"). It is stateful: the role chunk goes first, once.
type chunkEncoder struct {
	id           string
	created      int64
	model        string
	includeUsage bool
	started      bool
}

func newChunkEncoder(model string, includeUsage bool) *chunkEncoder {
	return &chunkEncoder{id: "chatcmpl-" + newHex(8), created: time.Now().Unix(), model: model, includeUsage: includeUsage}
}

// encode answers zero or more payloads for f. A finished stream ends with [DONE]; an error or
// a cancel ends with an {"error"} payload and no [DONE], so a client cannot mistake it for a
// complete reply.
func (e *chunkEncoder) encode(f Frame) []string {
	switch {
	case f.Cancelled:
		return []string{errorChunk("Request cancelled", "cancelled")}
	case f.Err != "":
		return []string{errorChunk(f.Err, "server_error")}
	}
	var out []string
	if !e.started {
		e.started = true
		empty := ""
		out = append(out, e.chunk(chunkDeltaJSON{Role: "assistant", Content: &empty}, nil))
	}
	if !f.Done {
		if f.Delta != "" {
			d := f.Delta
			out = append(out, e.chunk(chunkDeltaJSON{Content: &d}, nil))
		}
		return out
	}
	// Done.Content is every delta again: not repeated. Calls arrive merged, one chunk each.
	for i, tc := range f.ToolCalls {
		c := chunkToolCallJSON{Index: i, Type: tc.Type, Function: chunkToolFunctionJSON{Arguments: tc.Arguments}}
		if tc.ID != nil {
			c.ID = *tc.ID
		} else {
			c.ID = "call_" + newHex(12) // the openai SDKs need an id
		}
		if c.Type == "" {
			c.Type = "function"
		}
		if tc.Name != nil {
			c.Function.Name = *tc.Name
		}
		out = append(out, e.chunk(chunkDeltaJSON{ToolCalls: []chunkToolCallJSON{c}}, nil))
	}
	reason := f.FinishReason
	if reason == "" {
		reason = "stop"
		if len(f.ToolCalls) > 0 {
			reason = "tool_calls"
		}
	}
	out = append(out, e.chunk(chunkDeltaJSON{}, &reason))
	if e.includeUsage {
		u, _ := json.Marshal(f.Usage)
		out = append(out, marshalChunk(chunkJSON{ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model,
			Choices: []chunkChoiceJSON{}, Usage: u}))
	}
	return append(out, "[DONE]")
}

func (e *chunkEncoder) chunk(d chunkDeltaJSON, finish *string) string {
	c := chunkJSON{ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model,
		Choices: []chunkChoiceJSON{{Index: 0, Delta: d, FinishReason: finish}}}
	if e.includeUsage {
		c.Usage = json.RawMessage("null")
	}
	return marshalChunk(c)
}

func errorChunk(msg, typ string) string {
	return marshalChunk(map[string]any{"error": map[string]any{"message": msg, "type": typ}})
}

func marshalChunk(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return string(bytes.TrimRight(b.Bytes(), "\n"))
}
