package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testEncoder(includeUsage bool) *chunkEncoder {
	return &chunkEncoder{id: "chatcmpl-x", created: 7, model: "live", includeUsage: includeUsage}
}

// encodeAll runs frames through e and decodes every payload but [DONE].
func encodeAll(t *testing.T, e *chunkEncoder, frames ...Frame) (chunks []map[string]any, raw []string) {
	t.Helper()
	for _, f := range frames {
		raw = append(raw, e.encode(f)...)
	}
	for _, p := range raw {
		if p == "[DONE]" {
			continue
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(p), &c); err != nil {
			t.Fatalf("payload %q: %v", p, err)
		}
		chunks = append(chunks, c)
	}
	return chunks, raw
}

func choice(t *testing.T, c map[string]any) map[string]any {
	t.Helper()
	cs, _ := c["choices"].([]any)
	if len(cs) != 1 {
		t.Fatalf("choices %v", c)
	}
	return cs[0].(map[string]any)
}

func TestChunkEncoderContent(t *testing.T) {
	chunks, raw := encodeAll(t, testEncoder(false),
		Frame{Delta: "Hi "}, Frame{Delta: "café <b>"},
		Frame{Done: true, Content: "Hi café <b>", FinishReason: "stop", Usage: Usage{1, 2, 3}})
	if raw[0] != `{"id":"chatcmpl-x","object":"chat.completion.chunk","created":7,"model":"live","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` {
		t.Fatalf("role chunk %s", raw[0])
	}
	if raw[len(raw)-1] != "[DONE]" || len(chunks) != 4 {
		t.Fatalf("payloads %q", raw)
	}
	var content string
	for _, c := range chunks[1:3] {
		ch := choice(t, c)
		if ch["finish_reason"] != nil {
			t.Fatalf("finish on a delta %v", c)
		}
		content += ch["delta"].(map[string]any)["content"].(string)
	}
	if content != "Hi café <b>" { // the Done frame's full content is not repeated
		t.Fatalf("content %q", content)
	}
	last := chunks[3]
	if ch := choice(t, last); ch["finish_reason"] != "stop" || len(ch["delta"].(map[string]any)) != 0 {
		t.Fatalf("final %v", last)
	}
	for _, c := range chunks {
		if _, ok := c["usage"]; ok {
			t.Fatalf("usage without include_usage: %v", c)
		}
		if c["id"] != "chatcmpl-x" || c["object"] != "chat.completion.chunk" || c["model"] != "live" {
			t.Fatalf("envelope %v", c)
		}
	}
}

func TestChunkEncoderUsage(t *testing.T) {
	chunks, raw := encodeAll(t, testEncoder(true),
		Frame{Delta: "a"}, Frame{Done: true, Content: "a", FinishReason: "length", Usage: Usage{9, 5, 14}})
	if raw[len(raw)-1] != "[DONE]" || len(chunks) != 4 {
		t.Fatalf("payloads %q", raw)
	}
	for _, c := range chunks[:3] {
		if u, ok := c["usage"]; !ok || u != nil {
			t.Fatalf("want usage:null before the usage chunk: %v", c)
		}
	}
	if choice(t, chunks[2])["finish_reason"] != "length" {
		t.Fatalf("finish %v", chunks[2])
	}
	u := chunks[3]
	if cs := u["choices"].([]any); len(cs) != 0 {
		t.Fatalf("usage chunk choices %v", u)
	}
	if um := u["usage"].(map[string]any); um["prompt_tokens"] != float64(9) || um["completion_tokens"] != float64(5) || um["total_tokens"] != float64(14) {
		t.Fatalf("usage %v", u)
	}
}

func TestChunkEncoderToolCalls(t *testing.T) {
	id, name := "call_9", "lights"
	chunks, raw := encodeAll(t, testEncoder(false), Frame{Done: true, FinishReason: "tool_calls", ToolCalls: []StreamToolCall{
		{ID: &id, Type: "function", Name: &name, Arguments: `{"on":true}`},
		{Arguments: "{}"}, // id, name and type missing upstream
	}})
	if raw[len(raw)-1] != "[DONE]" || len(chunks) != 4 {
		t.Fatalf("payloads %q", raw)
	}
	if d := choice(t, chunks[0])["delta"].(map[string]any); d["role"] != "assistant" {
		t.Fatalf("role chunk %v", chunks[0])
	}
	for i, c := range chunks[1:3] {
		calls := choice(t, c)["delta"].(map[string]any)["tool_calls"].([]any)
		tc := calls[0].(map[string]any)
		fn := tc["function"].(map[string]any)
		if len(calls) != 1 || tc["index"] != float64(i) || tc["type"] != "function" {
			t.Fatalf("call %d: %v", i, tc)
		}
		switch i {
		case 0:
			if tc["id"] != "call_9" || fn["name"] != "lights" || fn["arguments"] != `{"on":true}` {
				t.Fatalf("call 0: %v", tc)
			}
		case 1:
			if s, _ := tc["id"].(string); !strings.HasPrefix(s, "call_") || len(s) <= 5 || fn["name"] != "" || fn["arguments"] != "{}" {
				t.Fatalf("call 1: %v", tc)
			}
		}
	}
	if choice(t, chunks[3])["finish_reason"] != "tool_calls" {
		t.Fatalf("final %v", chunks[3])
	}
}

func TestChunkEncoderErrorMidStream(t *testing.T) {
	chunks, raw := encodeAll(t, testEncoder(true), Frame{Delta: "a"}, Frame{Err: "HTTP 500: boom"})
	if len(raw) != 3 || raw[2] == "[DONE]" {
		t.Fatalf("payloads %q", raw)
	}
	e := chunks[2]["error"].(map[string]any)
	if e["message"] != "HTTP 500: boom" || e["type"] != "server_error" {
		t.Fatalf("error %v", chunks[2])
	}
}

func TestChunkEncoderCancelled(t *testing.T) {
	chunks, raw := encodeAll(t, testEncoder(false), Frame{Delta: "a"}, Frame{Cancelled: true})
	if len(raw) != 3 || raw[2] == "[DONE]" {
		t.Fatalf("payloads %q", raw)
	}
	if e := chunks[2]["error"].(map[string]any); e["type"] != "cancelled" {
		t.Fatalf("cancel %v", chunks[2])
	}
}

// readChunks reads a standard SSE body: data lines only, the last one [DONE] when present.
func readChunks(t *testing.T, r io.Reader) (chunks []map[string]any, done bool) {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") || done {
			t.Fatalf("bad line %q (after [DONE]: %v)", line, done)
		}
		if line == "data: [DONE]" {
			done = true
			continue
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(line[6:]), &c); err != nil {
			t.Fatalf("chunk %q: %v", line, err)
		}
		chunks = append(chunks, c)
	}
	return chunks, done
}

func TestOpenAIRouteStream(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{Content: "One two café"})
	req, _ := http.NewRequest("POST", e.srv.URL+"/openai/v1/chat/completions", strings.NewReader(
		`{"model":"live","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"count"}]}`))
	for k, v := range appHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Request-Id", "rid-o")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") || res.Header.Get("X-Request-Id") != "rid-o" {
		t.Fatalf("%d %v", res.StatusCode, res.Header)
	}
	chunks, done := readChunks(t, res.Body)
	if !done || len(chunks) < 4 {
		t.Fatalf("done %v chunks %v", done, chunks)
	}
	if d := choice(t, chunks[0])["delta"].(map[string]any); d["role"] != "assistant" {
		t.Fatalf("first %v", chunks[0])
	}
	var content string
	for _, c := range chunks[1 : len(chunks)-2] {
		content += choice(t, c)["delta"].(map[string]any)["content"].(string)
	}
	if content != "One two café" || choice(t, chunks[len(chunks)-2])["finish_reason"] != "stop" {
		t.Fatalf("content %q, final %v", content, chunks[len(chunks)-2])
	}
	if u := chunks[len(chunks)-1]["usage"].(map[string]any); u["total_tokens"] != float64(14) {
		t.Fatalf("usage %v", chunks[len(chunks)-1])
	}
}

func TestOpenAIRouteToolCallsAndErrors(t *testing.T) {
	e := setup(t)
	e.engine.script(fakeReply{ToolCalls: []map[string]any{{"id": "call_9", "function": map[string]any{"name": "f", "arguments": "{}"}}}})
	r := e.post("/openai/v1/chat/completions", chat("x", map[string]any{"stream": true, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "f"}}}}))
	chunks, done := readChunks(t, bytes.NewReader(r.body))
	if !done || len(chunks) != 3 {
		t.Fatalf("chunks %v", chunks)
	}
	tc := choice(t, chunks[1])["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if tc["id"] != "call_9" || tc["function"].(map[string]any)["name"] != "f" || choice(t, chunks[2])["finish_reason"] != "tool_calls" {
		t.Fatalf("tool chunks %v", chunks)
	}
	for _, c := range chunks {
		if _, ok := c["usage"]; ok {
			t.Fatalf("usage without include_usage: %v", c)
		}
	}

	// An upstream failure after the 200 is an error chunk, with no [DONE].
	e.engine.script(fakeReply{Status: 500, Body: "boom"})
	r = e.post("/openai/v1/chat/completions", chat("x", map[string]any{"stream": true}))
	chunks, done = readChunks(t, bytes.NewReader(r.body))
	if r.status != 200 || done || len(chunks) != 1 || chunks[0]["error"].(map[string]any)["message"] != "HTTP 500: boom" {
		t.Fatalf("%d %v %v", r.status, done, chunks)
	}

	// Validation stays an HTTP error (app credentials: TestAppAuth).
	if r := e.post("/openai/v1/chat/completions", map[string]any{"stream": true}); r.status != 422 {
		t.Fatalf("validation %d %s", r.status, r.body)
	}

	// Non-stream is the ordinary chat completion.
	e.engine.script(fakeReply{Content: "ok"})
	if r := e.post("/openai/v1/chat/completions", chat("x", nil)); r.status != 200 || r.json(t)["object"] != "chat.completion" {
		t.Fatalf("non-stream %d %s", r.status, r.body)
	}
}

func TestOpenAIRouteCancel(t *testing.T) {
	e := setup(t)
	e.engine.streamDelay = 30 * time.Millisecond
	e.engine.streamText = strings.Repeat("tick ", 200)
	req, _ := http.NewRequest("POST", e.srv.URL+"/openai/v1/chat/completions", strings.NewReader(
		`{"model":"live","stream":true,"messages":[{"role":"user","content":"count"}]}`))
	for k, v := range appHeaders {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Request-Id", "cancel-o")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	br := bufio.NewReader(res.Body)
	if line, _ := br.ReadString('\n'); !strings.Contains(line, `"role":"assistant"`) {
		t.Fatalf("first line %q", line)
	}
	if r := e.post("/v1/chat/completions/cancel/cancel-o", nil); r.status != 200 {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	chunks, done := readChunks(t, br)
	last := chunks[len(chunks)-1]
	if done || last["error"].(map[string]any)["type"] != "cancelled" {
		t.Fatalf("done %v, last %v", done, last)
	}
}
