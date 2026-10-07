package llm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

// The OpenAI-compatible engine client: llama-server and remote endpoints speak
// /v1/chat/completions and /v1/embeddings (01 §3.1, 04 §3). It replaces the legacy
// RestClient; payloads keep structured content and tool history (D8).

// chatPayload is the upstream request body.
type chatPayload struct {
	Model              string          `json:"model"`
	Messages           []Message       `json:"messages"`
	Temperature        float64         `json:"temperature"`
	Stream             bool            `json:"stream"`
	StreamOptions      *streamOptions  `json:"stream_options,omitempty"`
	MaxTokens          *int            `json:"max_tokens,omitempty"`
	TopP               *float64        `json:"top_p,omitempty"`
	Seed               *int64          `json:"seed,omitempty"`
	Tools              []Tool          `json:"tools,omitempty"`
	ToolChoice         json.RawMessage `json:"tool_choice,omitempty"`
	ChatTemplateKwargs map[string]bool `json:"chat_template_kwargs,omitempty"`
	ReasoningBudget    *int            `json:"reasoning_budget,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// applyThinking is _apply_reasoning: a nil budget leaves the server's launch flags in charge;
// otherwise the chat template's enable_thinking kwarg (the only control Qwen3.5 honours) is
// set, and reasoning_budget is forwarded for servers that read it. N>0 means "on, uncapped".
func (p *chatPayload) applyThinking(budget *int) {
	if budget == nil {
		return
	}
	p.ChatTemplateKwargs = map[string]bool{"enable_thinking": *budget != 0}
	b := *budget
	p.ReasoningBudget = &b
}

func endpointURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
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
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: c.Function.Name, Arguments: argumentsString(c.Function.Arguments)}}
}

func (s *Service) post(ctx context.Context, ep Endpoint, path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL(ep.BaseURL, path), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "jarvisd-llm")
	if ep.APIKey != "" {
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
		return nil, &APIError{Status: resp.StatusCode, Type: ErrorTypeForStatus(resp.StatusCode), Message: string(body)}
	}
	return resp, nil
}

// complete runs one non-stream completion.
func (s *Service) complete(ctx context.Context, ep Endpoint, p *chatPayload) (completion, error) {
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
		c.FinishReason = *ch.FinishReason
	}
	return c, nil
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
