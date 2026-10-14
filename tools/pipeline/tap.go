package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The LLM tap: an OpenAI-compatible pass-through between a sandbox's llm.live remote endpoint
// and the real engine. It forwards every request untouched, streams the answer back as it
// arrives, and appends one TapEntry per call to a JSONL log: the full request (messages,
// tools) and the reply assembled from either a JSON body or SSE frames. The pipeline runner
// reads the entries a turn produced to show what the model saw and did, server tools included.

// TapEntry is one logged model call.
type TapEntry struct {
	Seq        int             `json:"seq"`
	Time       time.Time       `json:"time"`
	Path       string          `json:"path"`
	Status     int             `json:"status"`
	DurationMS float64         `json:"duration_ms"`
	Request    json.RawMessage `json:"request,omitempty"`
	Reply      TapReply        `json:"reply"`
	Error      string          `json:"error,omitempty"`
}

// TapReply is the model's answer, whichever way it came back.
type TapReply struct {
	Content   string     `json:"content,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Finish    string     `json:"finish_reason,omitempty"`
	Raw       string     `json:"raw,omitempty"` // only when the body could not be parsed
}

// Tap is the proxy. Upstream returns the base URL (scheme://host:port) per request. Header is
// set on every forwarded request (the installed jarvisd's app-to-app credentials).
type Tap struct {
	Upstream func() (string, error)
	Header   http.Header
	Log      io.Writer
	Client   *http.Client

	mu  sync.Mutex
	seq int
}

func (t *Tap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entry := TapEntry{Time: start.UTC(), Path: r.URL.Path}
	if json.Valid(body) {
		entry.Request = body
	}
	base, err := t.Upstream()
	if err != nil {
		entry.Status, entry.Error = http.StatusBadGateway, err.Error()
		t.record(entry, start)
		http.Error(w, "tap: "+err.Error(), http.StatusBadGateway)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(base, "/")+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for k, vs := range r.Header {
		if k != "Content-Length" && k != "Authorization" {
			req.Header[k] = vs
		}
	}
	for k, vs := range t.Header {
		req.Header[k] = vs
	}
	client := t.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		entry.Status, entry.Error = http.StatusBadGateway, err.Error()
		t.record(entry, start)
		http.Error(w, "tap: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		w.Header()[k] = vs
	}
	w.WriteHeader(resp.StatusCode)
	entry.Status = resp.StatusCode

	// Stream through, keeping a copy.
	var copyBuf bytes.Buffer
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			copyBuf.Write(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				entry.Error = "client went away: " + werr.Error()
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			entry.Error = rerr.Error()
			break
		}
	}
	entry.Reply = parseReply(resp.Header.Get("Content-Type"), copyBuf.Bytes())
	t.record(entry, start)
}

func (t *Tap) record(e TapEntry, start time.Time) {
	e.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	e.Seq = t.seq
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	t.Log.Write(append(b, '\n'))
}

// openAIChoice covers both a full message and a stream delta.
type openAIChunk struct {
	Choices []struct {
		Message *openAIMsg `json:"message"`
		Delta   *openAIMsg `json:"delta"`
		Finish  *string    `json:"finish_reason"`
	} `json:"choices"`
}

type openAIMsg struct {
	Content   *string `json:"content"`
	Reasoning *string `json:"reasoning_content"`
	ToolCalls []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// parseReply assembles the reply from a JSON body or from SSE "data:" frames.
func parseReply(contentType string, body []byte) TapReply {
	if strings.Contains(contentType, "text/event-stream") || bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		return parseSSE(body)
	}
	var c openAIChunk
	if err := json.Unmarshal(body, &c); err != nil || len(c.Choices) == 0 {
		return TapReply{Raw: truncate(string(body), 4000)}
	}
	var r TapReply
	ch := c.Choices[0]
	if ch.Finish != nil {
		r.Finish = *ch.Finish
	}
	if m := ch.Message; m != nil {
		if m.Content != nil {
			r.Content = *m.Content
		}
		if m.Reasoning != nil {
			r.Reasoning = *m.Reasoning
		}
		for _, tc := range m.ToolCalls {
			r.ToolCalls = append(r.ToolCalls, ToolCall{ID: tc.ID, Type: "function", Function: FunctionCall{Name: tc.Function.Name, Arguments: tc.Function.Arguments}})
		}
	}
	return r
}

func parseSSE(body []byte) TapReply {
	var r TapReply
	var content, reasoning strings.Builder
	calls := map[int]*ToolCall{}
	var order []int
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var c openAIChunk
		if json.Unmarshal([]byte(data), &c) != nil || len(c.Choices) == 0 {
			continue
		}
		ch := c.Choices[0]
		if ch.Finish != nil && *ch.Finish != "" {
			r.Finish = *ch.Finish
		}
		d := ch.Delta
		if d == nil {
			d = ch.Message
		}
		if d == nil {
			continue
		}
		if d.Content != nil {
			content.WriteString(*d.Content)
		}
		if d.Reasoning != nil {
			reasoning.WriteString(*d.Reasoning)
		}
		for i, tc := range d.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			c, ok := calls[idx]
			if !ok {
				c = &ToolCall{Type: "function"}
				calls[idx] = c
				order = append(order, idx)
			}
			if tc.ID != "" {
				c.ID = tc.ID
			}
			c.Function.Name += tc.Function.Name
			c.Function.Arguments += tc.Function.Arguments
		}
	}
	r.Content, r.Reasoning = content.String(), reasoning.String()
	for _, i := range order {
		r.ToolCalls = append(r.ToolCalls, *calls[i])
	}
	return r
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
