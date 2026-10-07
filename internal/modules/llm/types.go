package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Labels (LD1): the two chat labels, and the embeddings engine.
const (
	LabelLive       = "live"
	LabelBackground = "background"
	LabelEmbeddings = "embeddings"
)

// Endpoint is an OpenAI-compatible server for a label: a supervised llama-server or a remote
// (LD2). BaseURL is the server root; "/v1" on the end is accepted.
type Endpoint struct {
	BaseURL string
	APIKey  string
	// Model is sent as the request's "model" and listed by /v1/models.
	Model string
	// Vision: the engine has a projector (LD4). Embeddings: it serves /v1/embeddings.
	Vision, Embeddings bool
	// ContextLength is the per-request window (-c / parallel), 0 when unknown.
	ContextLength int
	// Remote marks a remote endpoint (for /v1/engine).
	Remote bool
}

// Resolver maps a label to its engine (track A's model manager implements it). Resolve
// returns a *NotReadyError when the label has no usable engine; the Endpoint may still carry
// what is known (Model, Vision) so /v1/models and /health can describe it.
type Resolver interface {
	Resolve(ctx context.Context, label string) (Endpoint, error)
}

// Label states reported through NotReadyError.
const (
	StateLoading       = "loading"
	StateFailed        = "failed"
	StateNotConfigured = "not_configured"
)

// NotReadyError says a label's engine can't serve yet.
type NotReadyError struct {
	State  string // StateLoading, StateFailed or StateNotConfigured
	Reason string
}

func (e *NotReadyError) Error() string {
	if e.Reason == "" {
		return e.State
	}
	return e.State + ": " + e.Reason
}

// ImageURL is an image_url content part's payload.
type ImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// Part is one structured content part: {"type":"text","text"} or {"type":"image_url",...}.
type Part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// MarshalJSON keeps an empty text part's "text" key.
func (p Part) MarshalJSON() ([]byte, error) {
	if p.Type == "text" {
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{p.Type, p.Text})
	}
	type plain Part
	return json.Marshal(plain(p))
}

// Content is a message's content: null, a string, or a list of parts.
type Content struct {
	Text  *string
	Parts []Part
}

// TextContent is string content.
func TextContent(s string) *Content { return &Content{Text: &s} }

func (c *Content) MarshalJSON() ([]byte, error) {
	switch {
	case c == nil:
		return []byte("null"), nil
	case c.Text != nil:
		return json.Marshal(*c.Text)
	case c.Parts != nil:
		return json.Marshal(c.Parts)
	}
	return []byte("null"), nil
}

func (c *Content) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		c.Text = &s
		return nil
	}
	return json.Unmarshal(b, &c.Parts)
}

// FunctionCall is a tool call's function; Arguments is a JSON string.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCall is an OpenAI tool call.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// Message is a chat message. Tool history (ToolCalls on assistant turns, ToolCallID on tool
// turns) is forwarded to the engine as is (04 §8.3, D8: the legacy REST path dropped it).
type Message struct {
	Role       string     `json:"role"`
	Content    *Content   `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Tool is a tool definition. Raw, when set, is forwarded byte for byte (extra keys such as
// "strict" survive, as pydantic's extra="allow" kept them); otherwise Function is encoded.
type Tool struct {
	Raw      json.RawMessage `json:"-"`
	Type     string          `json:"type"`
	Function ToolFunction    `json:"function"`
}

// ToolFunction is a tool's function definition; Parameters is a JSON schema.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (t Tool) MarshalJSON() ([]byte, error) {
	if len(t.Raw) > 0 {
		return t.Raw, nil
	}
	type plain Tool
	p := plain(t)
	if p.Type == "" {
		p.Type = "function"
	}
	return json.Marshal(p)
}

func (t *Tool) UnmarshalJSON(b []byte) error {
	type plain Tool
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*t = Tool(p)
	t.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// ResponseFormat is response_format. JSONSchema is the json_schema object (key order kept).
type ResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}

// ChatRequest is one chat completion, for in-process callers and the HTTP API alike.
type ChatRequest struct {
	// Label is "live" or "background"; anything else is treated as live.
	Label       string    `json:"label"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"` // nil: 0.7 (an explicit 0 is honoured, D8)
	TopP        *float64  `json:"top_p,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Seed        *int64    `json:"seed,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	// ToolChoice is "auto"/"none"/"required" or an object, as raw JSON.
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	// ReasoningBudget: 0 thinking off, -1 unrestricted, N on; nil uses the label's setting.
	ReasoningBudget *int `json:"reasoning_budget,omitempty"`
	// WantDateKeys fills ChatResponse.DateKeys (the external include_date_context; in-process
	// CC calls dates.Extract on the raw transcript instead, D40).
	WantDateKeys bool `json:"want_date_keys,omitempty"`
	// RequestID is the stream's cancel handle (X-Request-Id).
	RequestID string `json:"request_id,omitempty"`
}

// Usage is token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is a finished completion.
type ChatResponse struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	// DateKeys is set (possibly empty) when the request asked for them.
	DateKeys []string
}

// Frame is one stream event. Exactly one of Delta, Done, Err or Cancelled is set.
type Frame struct {
	Delta string

	Done         bool
	Content      string
	Usage        Usage
	ToolCalls    []StreamToolCall
	FinishReason string

	Err       string
	Cancelled bool
}

// StreamToolCall is a tool call merged from stream fragments; ID or Name may be missing.
type StreamToolCall struct {
	ID        *string
	Type      string
	Name      *string
	Arguments string
}

// APIError is an error with the legacy OpenAI-style shape
// {"detail":{"error":{"type","message","code"}}} and an HTTP status.
type APIError struct {
	Status  int
	Type    string
	Message string
	Code    any
	// Extra keys inside "error" (model_not_loaded carries slot and state).
	Extra map[string]any
}

func (e *APIError) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Type, e.Message) }

// ErrorTypeForStatus is error_type_for_status.
func ErrorTypeForStatus(status int) string {
	switch {
	case status >= 500:
		return "internal_server_error"
	case status == 404:
		return "not_found_error"
	case status == 429:
		return "rate_limit_error"
	}
	return "invalid_request_error"
}

func apiErr(status int, typ, msg string) *APIError {
	return &APIError{Status: status, Type: typ, Message: msg}
}

// asAPIError converts any error to the shape the HTTP layer writes.
func asAPIError(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return apiErr(500, "internal_server_error", "Internal error: "+err.Error())
}

// NormalizeLabel maps a requested model name to its label: live or background (case-
// insensitive); anything else is live.
func NormalizeLabel(model string) string {
	if strings.EqualFold(model, LabelBackground) {
		return LabelBackground
	}
	return LabelLive
}
