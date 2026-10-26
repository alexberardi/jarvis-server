package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The OpenAI-compatible engine client: llama-server and remote endpoints speak
// /v1/chat/completions and /v1/embeddings (01 §3.1, 04 §3). It replaces the legacy
// RestClient; payloads keep structured content and tool history (D8). A remote with
// remote_api=anthropic is translated to the Messages API instead (anthropic.go).

// chatPayload is the upstream request body.
type chatPayload struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Temperature   *float64       `json:"temperature,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	MaxTokens     *int           `json:"max_tokens,omitempty"`
	// MaxCompletionTokens replaces max_tokens for a remote: OpenAI's newer models refuse
	// max_tokens.
	MaxCompletionTokens *int            `json:"max_completion_tokens,omitempty"`
	TopP                *float64        `json:"top_p,omitempty"`
	Seed                *int64          `json:"seed,omitempty"`
	Tools               []Tool          `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ChatTemplateKwargs  map[string]bool `json:"chat_template_kwargs,omitempty"`
	ReasoningBudget     *int            `json:"reasoning_budget,omitempty"`
	// ReasoningBudgetTokens is llama-server's own name for a capped budget (N>0); it ignores
	// reasoning_budget.
	ReasoningBudgetTokens *int `json:"reasoning_budget_tokens,omitempty"`
	// ReasoningEffort is OpenAI's thinking control for its reasoning models (fixedSampling).
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// applyThinking is _apply_reasoning: a nil budget leaves the server's launch flags in charge;
// otherwise the chat template's enable_thinking kwarg (the only control Qwen3.5 honours) is
// set, and reasoning_budget is forwarded for servers that read it (not to one that refuses it,
// ep.NoReasoningBudget). N>0 is also sent as reasoning_budget_tokens, which llama-server
// enforces by closing the thinking block after N tokens. A remote gets none of these: they are
// llama-server's, and OpenAI answers 400 to an unknown parameter.
func (p *chatPayload) applyThinking(budget *int, ep Endpoint) {
	if ep.Remote {
		if ep.API != APIAnthropic { // anthropicBody reads MaxTokens
			p.MaxCompletionTokens, p.MaxTokens = p.MaxTokens, nil
		}
		if fixedSampling(p.Model) {
			// The cap counts hidden reasoning too: a chat turn's 256 went on thinking and the
			// answer came back empty.
			p.Temperature, p.TopP, p.MaxCompletionTokens = nil, nil, nil
			if budget != nil && *budget == 0 {
				p.ReasoningEffort = "low" // the lowest every family (o-series, gpt-5.x) accepts
			}
		}
		if gemini(p.Model) {
			// Gemini's cap counts thinking too, and 2.5 Pro and 3.x can't turn thinking off.
			p.MaxCompletionTokens = nil
			if budget != nil && *budget == 0 {
				p.ReasoningEffort = "low" // minimal is a 400 on some (3 Pro); low works on every family
			}
			p.Messages = withThoughtSignatures(p.Messages)
		}
		return
	}
	if budget == nil {
		return
	}
	p.ChatTemplateKwargs = map[string]bool{"enable_thinking": *budget != 0}
	if ep.NoReasoningBudget {
		return
	}
	b := *budget
	p.ReasoningBudget = &b
	if b > 0 {
		p.ReasoningBudgetTokens = &b
	}
}

// fixedSampling: OpenAI's reasoning models answer 400 to any temperature or top_p but the
// default. shortcut: matched by name (also behind a router's "openai/" prefix); add new families here.
func fixedSampling(model string) bool {
	m := strings.ToLower(model)
	m = m[strings.LastIndex(m, "/")+1:]
	return slices.ContainsFunc([]string{"gpt-5", "o1", "o3", "o4"}, func(p string) bool { return strings.HasPrefix(m, p) })
}

// skipThoughtSignature is Google's documented stand-in for a tool call whose signature is
// unknown (history from another model, or cc's own, which keeps no signatures).
// shortcut: real signatures don't survive cc's history (parse.ToolCall), so Gemini 3 may reason
// a little worse across tool rounds; carry ExtraContent through cc if that shows.
var skipThoughtSignature = json.RawMessage(`{"google":{"thought_signature":"skip_thought_signature_validator"}}`)

// withThoughtSignatures gives every history tool call without one the stand-in signature
// (Gemini 3 answers 400 "Function call is missing a thought_signature"). Copies on write: the
// caller's messages are not changed.
func withThoughtSignatures(msgs []Message) []Message {
	out := msgs
	for i, m := range msgs {
		if !slices.ContainsFunc(m.ToolCalls, func(tc ToolCall) bool { return len(tc.ExtraContent) == 0 }) {
			continue
		}
		if len(out) > 0 && &out[0] == &msgs[0] { // first change: copy
			out = slices.Clone(msgs)
		}
		calls := slices.Clone(m.ToolCalls)
		for j := range calls {
			if len(calls[j].ExtraContent) == 0 {
				calls[j].ExtraContent = skipThoughtSignature
			}
		}
		out[i].ToolCalls = calls
	}
	return out
}

// gemini is a Google model name (models/gemini-2.5-flash, gemini-3-flash).
func gemini(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m[strings.LastIndex(m, "/")+1:], "gemini")
}

func endpointURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") && !strings.HasSuffix(base, "/openai") { // Google's is /v1beta/openai
		base += "/v1"
	}
	return base + path
}

func newHex(n int) string {
	b := make([]byte, (n+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// completion is a parsed non-stream answer.
type completion struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
}

type wireToolCall struct {
	ID       *string `json:"id"`
	Type     string  `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *wireUsage) usage() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{u.PromptTokens, u.CompletionTokens, u.TotalTokens}
}

// argumentsString keeps a JSON-string arguments value, and re-encodes an object some servers
// send instead.
func argumentsString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func (c *wireToolCall) toolCall() ToolCall {
	id := ""
	if c.ID != nil {
		id = *c.ID
	}
	if id == "" {
		id = "call_" + newHex(12)
	}
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: c.Function.Name, Arguments: argumentsString(c.Function.Arguments)},
		ExtraContent: c.ExtraContent}
}

// remoteRetryDelays are the waits before retrying a remote's transient refusal (429, or a 5xx
// such as Gemini's 503 "This model is currently experiencing high demand"). A var for tests.
var remoteRetryDelays = []time.Duration{time.Second, 3 * time.Second}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusInternalServerError || code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable || code == http.StatusGatewayTimeout
}

func (s *Service) post(ctx context.Context, ep Endpoint, path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		resp, err := s.postOnce(ctx, ep, path, raw)
		var apiErr *APIError
		if !ep.Remote || attempt >= len(remoteRetryDelays) || !errors.As(err, &apiErr) || !retryableStatus(apiErr.Status) {
			return resp, err
		}
		s.log.Info("llm: remote refused, retrying", "status", apiErr.Status, "attempt", attempt+1)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(remoteRetryDelays[attempt]):
		}
	}
}

func (s *Service) postOnce(ctx context.Context, ep Endpoint, path string, raw []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(ep.BaseURL, path), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "jarvisd-llm")
	switch {
	case ep.API == APIAnthropic:
		// The Messages API authenticates with x-api-key, never a bearer token.
		req.Header.Set("anthropic-version", anthropicVersion)
		if ep.APIKey != "" {
			req.Header.Set("x-api-key", ep.APIKey)
		}
	case ep.APIKey != "":
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		// The engine's own status and body, unwrapped: a context overflow is the caller's
		// 400 with n_prompt_tokens in the message, not a server fault (02 §3.9).
		msg := string(body)
		if ep.API == APIAnthropic {
			msg = anthropicErrorMessage(body)
		}
		return nil, &APIError{Status: resp.StatusCode, Type: ErrorTypeForStatus(resp.StatusCode), Message: msg}
	}
	return resp, nil
}

// complete runs one non-stream completion.
func (s *Service) complete(ctx context.Context, ep Endpoint, p *chatPayload) (completion, error) {
	if ep.API == APIAnthropic {
		return s.completeAnthropic(ctx, ep, p)
	}
	resp, err := s.post(ctx, ep, "/chat/completions", p)
	if err != nil {
		return completion{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content   *string        `json:"content"`
				ToolCalls []wireToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *wireUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return completion{}, fmt.Errorf("Invalid JSON response: %w", err)
	}
	var c completion
	c.Usage = out.Usage.usage()
	if len(out.Choices) == 0 {
		return c, nil
	}
	ch := out.Choices[0]
	if ch.Message.Content != nil {
		c.Content = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		c.ToolCalls = append(c.ToolCalls, tc.toolCall())
	}
	if ch.FinishReason != nil {
		c.FinishReason = toolCallsFinish(*ch.FinishReason, len(c.ToolCalls))
	}
	return c, nil
}

// toolCallsFinish is "tool_calls" for a reply with calls that says "stop", as Gemini's
// OpenAI layer does.
func toolCallsFinish(reason string, calls int) string {
	if reason == "stop" && calls > 0 {
		return "tool_calls"
	}
	return reason
}

// streamResult is what the stream parser gathered for the done frame.
type streamResult struct {
	Content      string
	Usage        Usage
	ToolCalls    []StreamToolCall
	FinishReason string
}

// streamDelta is one parsed upstream chunk's relevant parts.
type streamChunk struct {
	Usage   *wireUsage `json:"usage"`
	Choices []struct {
		Delta struct {
			Content   *string `json:"content"`
			ToolCalls []struct {
				Index    *int    `json:"index"`
				ID       *string `json:"id"`
				Type     *string `json:"type"`
				Function *struct {
					Name      *string `json:"name"`
					Arguments *string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// parseStream reads upstream OpenAI SSE and calls onDelta per non-empty content piece. It is
// generate_text_chat_stream's loop: keep-alives, event lines and [DONE] are skipped, a
// malformed frame is skipped, tool-call fragments are merged per index.
func parseStream(r io.Reader, onDelta func(string) bool) (streamResult, error) {
	res := streamResult{FinishReason: "stop"}
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	sc.Split(scanLines)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "[DONE]" {
			continue
		}
		var ck streamChunk
		if err := json.Unmarshal([]byte(data), &ck); err != nil {
			continue
		}
		if ck.Usage != nil && *ck.Usage != (wireUsage{}) {
			res.Usage = ck.Usage.usage()
		}
		if len(ck.Choices) == 0 {
			continue
		}
		ch := ck.Choices[0]
		if p := ch.Delta.Content; p != nil && *p != "" {
			sb.WriteString(*p)
			if !onDelta(*p) {
				return res, context.Canceled
			}
		}
		for _, d := range ch.Delta.ToolCalls {
			idx := 0
			if d.Index != nil {
				idx = *d.Index
			}
			if idx < 0 || idx > 1024 {
				continue
			}
			for len(res.ToolCalls) <= idx {
				res.ToolCalls = append(res.ToolCalls, StreamToolCall{Type: "function"})
			}
			slot := &res.ToolCalls[idx]
			if d.ID != nil && *d.ID != "" {
				id := *d.ID
				slot.ID = &id
			}
			if d.Type != nil && *d.Type != "" {
				slot.Type = *d.Type
			}
			if d.Function != nil {
				if d.Function.Name != nil && *d.Function.Name != "" {
					n := *d.Function.Name
					slot.Name = &n
				}
				if d.Function.Arguments != nil {
					slot.Arguments += *d.Function.Arguments
				}
			}
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			res.FinishReason = *ch.FinishReason
		}
	}
	res.Content = sb.String()
	res.FinishReason = toolCallsFinish(res.FinishReason, len(res.ToolCalls))
	return res, sc.Err()
}

// scanLines splits on \n, \r\n and \r, like httpx's aiter_lines.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		switch b {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil // need more to tell \r from \r\n
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// embed calls /v1/embeddings and returns L2-normalised vectors in input order.
func (s *Service) embed(ctx context.Context, ep Endpoint, texts []string) ([][]float64, error) {
	resp, err := s.post(ctx, ep, "/embeddings", map[string]any{"model": ep.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
			Index     *int      `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("Invalid JSON response: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float64, len(texts))
	for i, d := range out.Data {
		idx := i
		if d.Index != nil {
			idx = *d.Index
		}
		if idx < 0 || idx >= len(vecs) || vecs[idx] != nil {
			return nil, fmt.Errorf("embeddings: bad index %d", idx)
		}
		vecs[idx] = normalize(d.Embedding)
	}
	return vecs, nil
}

func normalize(v []float64) []float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	if sum == 0 {
		return v
	}
	n := math.Sqrt(sum)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}
