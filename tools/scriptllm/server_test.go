package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

const kitchen = `{
  "fallback": "I have no line for that.",
  "rules": [
    {"match": "(?i)turn (on|off) the kitchen lights",
     "tool_calls": [{"name": "control_device", "arguments": {"device_name": "Kitchen Lights", "action": "turn_on"}}]},
    {"after_tool": "control_device", "match": "\"success\":\\s*true", "reply": "The kitchen lights are on."},
    {"match": "(?i)hello", "reply": "Hello there, friend."}
  ]
}`

func newServer(t *testing.T, script string) (*Server, *httptest.Server, *bytes.Buffer) {
	t.Helper()
	sc, err := ParseScript("kitchen.json", []byte(script))
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	s := &Server{Dir: t.TempDir(), Log: &log, Context: 32768}
	s.Use(sc, "")
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	return s, hs, &log
}

type resolver struct{ ep llm.Endpoint }

func (r resolver) Resolve(context.Context, string) (llm.Endpoint, error) { return r.ep, nil }

// client is jarvisd's own LLM client pointed at the server, as llm.live engine remote with
// remote_api openai is.
func client(hs *httptest.Server) *llm.Service {
	ep := llm.Endpoint{BaseURL: hs.URL + "/v1", Model: Model, Remote: true, API: llm.APIOpenAI, ContextLength: 32768}
	return llm.NewService(llm.ServiceConfig{Resolver: resolver{ep}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

var tools = []llm.Tool{{Type: "function", Function: llm.ToolFunction{Name: "control_device", Parameters: json.RawMessage(`{"type":"object"}`)}}}

func user(s string) llm.Message { return llm.Message{Role: "user", Content: llm.TextContent(s)} }

func TestParseScriptRejectsBadRules(t *testing.T) {
	for name, src := range map[string]string{
		"bad regexp":      `{"rules":[{"match":"(", "reply":"x"}]}`,
		"no condition":    `{"rules":[{"reply":"x"}]}`,
		"says nothing":    `{"rules":[{"match":"x"}]}`,
		"nameless call":   `{"rules":[{"match":"x","tool_calls":[{"arguments":{}}]}]}`,
		"unknown field":   `{"rules":[{"match":"x","reply":"y","replly":"z"}]}`,
		"not json at all": `rules:`,
	} {
		if _, err := ParseScript(name, []byte(src)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	sc, err := ParseScript("ok", []byte(`{"rules":[{"match":"x","reply":"y"}]}`))
	if err != nil || sc.Fallback != DefaultFallback {
		t.Fatalf("got %+v, %v", sc, err)
	}
}

func TestToolRoundTripThroughTheRealClient(t *testing.T) {
	_, hs, log := newServer(t, kitchen)
	svc := client(hs)
	ctx := context.Background()
	msgs := []llm.Message{{Role: "system", Content: llm.TextContent("You are Jarvis.")}, user("Please turn on the kitchen lights")}

	res, err := svc.Chat(ctx, llm.ChatRequest{Label: llm.LabelLive, Messages: msgs, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if res.FinishReason != "tool_calls" || len(res.ToolCalls) != 1 {
		t.Fatalf("want one tool call, got %+v", res)
	}
	tc := res.ToolCalls[0]
	var args map[string]string
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatal(err)
	}
	if tc.Function.Name != "control_device" || tc.ID == "" || args["device_name"] != "Kitchen Lights" || args["action"] != "turn_on" {
		t.Fatalf("tool call %+v", tc)
	}

	// The tool's result comes back as a tool message answering the call's id: the after_tool
	// rule ends the round trip with a reply.
	msgs = append(msgs, llm.Message{Role: "assistant", ToolCalls: res.ToolCalls},
		llm.Message{Role: "tool", ToolCallID: tc.ID, Content: llm.TextContent(`{"success": true}`)})
	res, err = svc.Chat(ctx, llm.ChatRequest{Label: llm.LabelLive, Messages: msgs, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "The kitchen lights are on." || len(res.ToolCalls) != 0 || res.FinishReason != "stop" {
		t.Fatalf("after the tool: %+v", res)
	}
	if log.Len() != 0 {
		t.Fatalf("matched turns were logged: %s", log)
	}
}

func TestStreamThroughTheRealClient(t *testing.T) {
	_, hs, _ := newServer(t, kitchen)
	svc := client(hs)
	frames := func(msg string) (deltas []string, done llm.Frame) {
		t.Helper()
		ch, err := svc.Stream(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{user(msg)}, Tools: tools})
		if err != nil {
			t.Fatal(err)
		}
		for f := range ch {
			switch {
			case f.Err != "":
				t.Fatalf("stream error %s", f.Err)
			case f.Done:
				done = f
			case f.Delta != "":
				deltas = append(deltas, f.Delta)
			}
		}
		return deltas, done
	}

	deltas, done := frames("hello jarvis")
	if strings.Join(deltas, "") != "Hello there, friend." || len(deltas) != 3 || done.Content != "Hello there, friend." || done.FinishReason != "stop" {
		t.Fatalf("content stream: %q, done %+v", deltas, done)
	}
	if done.Usage.TotalTokens == 0 {
		t.Errorf("no usage in the done frame: %+v", done.Usage)
	}

	_, done = frames("turn off the kitchen lights")
	if done.FinishReason != "tool_calls" || len(done.ToolCalls) != 1 {
		t.Fatalf("tool stream: %+v", done)
	}
	tc := done.ToolCalls[0]
	if tc.ID == nil || *tc.ID == "" || tc.Name == nil || *tc.Name != "control_device" ||
		tc.Arguments != `{"device_name": "Kitchen Lights", "action": "turn_on"}` {
		t.Fatalf("merged tool call: id %v name %v args %s", tc.ID, tc.Name, tc.Arguments)
	}
}

func TestUnmatchedGetsTheFallbackAndIsRecorded(t *testing.T) {
	_, hs, log := newServer(t, kitchen)
	svc := client(hs)
	res, err := svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{user("what's the weather")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "I have no line for that." || len(res.ToolCalls) != 0 {
		t.Fatalf("fallback: %+v", res)
	}
	// A tool result no after_tool rule takes is a fallback too, so cc never loops on a call.
	msgs := []llm.Message{user("turn on the kitchen lights"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "control_device", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: llm.TextContent(`{"success": false}`)}}
	if res, err = svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: msgs, Tools: tools}); err != nil || res.Content != "I have no line for that." {
		t.Fatalf("unmatched tool result: %+v, %v", res, err)
	}

	var got []Unmatched
	getJSON(t, http.MethodGet, hs.URL+"/control/unmatched", nil, &got)
	if len(got) != 2 || got[0].Text != "what's the weather" || got[0].Role != "user" || got[0].Script != "kitchen.json" ||
		got[1].Role != "tool" || got[1].Tool != "control_device" {
		t.Fatalf("unmatched: %+v", got)
	}
	if lines := strings.Split(strings.TrimSpace(log.String()), "\n"); len(lines) != 2 || !strings.Contains(lines[0], `"text":"what's the weather"`) {
		t.Fatalf("log: %s", log)
	}
	getJSON(t, http.MethodDelete, hs.URL+"/control/unmatched", nil, &got)
	getJSON(t, http.MethodGet, hs.URL+"/control/unmatched", nil, &got)
	if len(got) != 0 {
		t.Fatalf("after DELETE: %+v", got)
	}
}

// cc adds system notes after a tool result and context after the user's words, and warms the
// prompt cache with system messages only; none of that changes which rule answers.
func TestSystemMessagesNeverHideTheTurn(t *testing.T) {
	_, hs, log := newServer(t, kitchen)
	svc := client(hs)
	sys := func(s string) llm.Message { return llm.Message{Role: "system", Content: llm.TextContent(s)} }
	msgs := []llm.Message{sys("You are Jarvis."), user("turn on the kitchen lights"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: "control_device", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: llm.TextContent(`{"success": true}`)},
		sys("[TOOL_DEDUPE] answer from the results above")}
	res, err := svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: msgs, Tools: tools})
	if err != nil || res.Content != "The kitchen lights are on." || len(res.ToolCalls) != 0 {
		t.Fatalf("after a trailing system note: %+v, %v", res, err)
	}
	res, err = svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{user("hello"), sys("<calendar>")}})
	if err != nil || res.Content != "Hello there, friend." {
		t.Fatalf("user then system: %+v, %v", res, err)
	}
	res, err = svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{sys("You are Jarvis.")}})
	if err != nil || res.Content != "" {
		t.Fatalf("warm-up: %+v, %v", res, err)
	}
	if log.Len() != 0 {
		t.Fatalf("logged as unmatched: %s", log)
	}
}

func TestControlSwitchesScripts(t *testing.T) {
	s, hs, _ := newServer(t, kitchen)
	svc := client(hs)
	say := func(msg string) string {
		t.Helper()
		res, err := svc.Chat(context.Background(), llm.ChatRequest{Label: llm.LabelLive, Messages: []llm.Message{user(msg)}})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content
	}
	if got := say("hello"); got != "Hello there, friend." {
		t.Fatalf("before: %q", got)
	}

	// By path, relative to Dir.
	if err := os.MkdirAll(filepath.Join(s.Dir, "e2e/llm"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(s.Dir, "e2e/llm/other.json")
	if err := os.WriteFile(file, []byte(`{"rules":[{"match":"hello","reply":"Other script."}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Script string `json:"script"`
		Rules  int    `json:"rules"`
	}
	if code := getJSON(t, http.MethodPost, hs.URL+"/control/script", map[string]any{"path": "e2e/llm/other.json"}, &st); code != 200 || st.Script != file || st.Rules != 1 {
		t.Fatalf("switch by path: %d %+v", code, st)
	}
	if got := say("hello"); got != "Other script." {
		t.Fatalf("after switch: %q", got)
	}

	// An empty body reloads the file.
	if err := os.WriteFile(file, []byte(`{"rules":[{"match":"hello","reply":"Edited."}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := getJSON(t, http.MethodPost, hs.URL+"/control/script", nil, &st); code != 200 {
		t.Fatalf("reload: %d", code)
	}
	if got := say("hello"); got != "Edited." {
		t.Fatalf("after reload: %q", got)
	}

	// Inline; a broken script is refused and the active one stays.
	if code := getJSON(t, http.MethodPost, hs.URL+"/control/script", map[string]any{"script": json.RawMessage(`{"rules":[{"match":"hello","reply":"Inline."}]}`)}, &st); code != 200 || st.Script != "inline" {
		t.Fatalf("inline: %d %+v", code, st)
	}
	if code := getJSON(t, http.MethodPost, hs.URL+"/control/script", map[string]any{"script": json.RawMessage(`{"rules":[{"match":"("}]}`)}, nil); code != 400 {
		t.Fatalf("broken script: %d", code)
	}
	if code := getJSON(t, http.MethodPost, hs.URL+"/control/script", map[string]any{"path": "e2e/llm/missing.json"}, nil); code != 400 {
		t.Fatalf("missing file: %d", code)
	}
	if got := say("hello"); got != "Inline." {
		t.Fatalf("after refused switches: %q", got)
	}
	if code := getJSON(t, http.MethodGet, hs.URL+"/control/script", nil, &st); code != 200 || st.Script != "inline" {
		t.Fatalf("GET: %d %+v", code, st)
	}
}

func TestModelsListsTheScriptedModel(t *testing.T) {
	_, hs, _ := newServer(t, kitchen)
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			Context int    `json:"context_length"`
		} `json:"data"`
	}
	if code := getJSON(t, http.MethodGet, hs.URL+"/v1/models", nil, &out); code != 200 || len(out.Data) != 1 || out.Data[0].ID != Model || out.Data[0].Context != 32768 {
		t.Fatalf("%d %+v", code, out)
	}
}

func TestDefaultScriptsParse(t *testing.T) {
	files, err := filepath.Glob("../../e2e/llm/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no scripts: %v", err)
	}
	for _, f := range files {
		if _, err := LoadScript(f); err != nil {
			t.Error(err)
		}
	}
}

func getJSON(t *testing.T, method, url string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}
