package cc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// A client tool with one parameter, and one that already declares failure_message itself.
const searchTools = `[
 {"type": "function", "function": {"name": "web_lookup", "description": "Look it up.",
  "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}},
 {"type": "function", "function": {"name": "own_failure", "description": "Has its own.",
  "parameters": {"type": "object", "properties": {"failure_message": {"type": "integer"}}}}}
]`

func toolsFrom(t *testing.T, s string) []prompts.Tool {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	var out []prompts.Tool
	for _, e := range v.([]any) {
		out = append(out, e.(*pyjson.Object))
	}
	return out
}

// toolByName finds a tool in a request's decoded "tools" list.
func toolByName(tools []any, name string) map[string]any {
	for _, x := range tools {
		f := x.(map[string]any)["function"].(map[string]any)
		if f["name"] == name {
			return f
		}
	}
	return nil
}

// Blueprint §6 (a): every native tool gets an optional failure_message argument; the cached
// tools and prompts.NativeTools (the G1 goldens) don't change, and the warmup sends the engine's
// bytes so the prefix cache holds.
func TestNativePayloadAddsFailureMessage(t *testing.T) {
	tools := toolsFrom(t, searchTools)
	before := prompts.CompactASCII(tools[0])
	nativeBefore := prompts.CompactASCII(prompts.NativeTools(tools)[0])

	out := nativePayload(tools)
	if len(out) != 2 {
		t.Fatalf("got %d tools", len(out))
	}
	want := `{"type":"function","function":{"name":"web_lookup","description":"Look it up.",` +
		`"parameters":{"type":"object","properties":{"query":{"type":"string"},` +
		`"failure_message":{"type":"string","description":"` + failureMessageDescription + `"}},"required":["query"]}}}`
	if got := string(out[0].Raw); got != want {
		t.Fatalf("payload:\n got %s\nwant %s", got, want)
	}
	if got, want := string(out[1].Raw), string(llmTools(prompts.NativeTools(tools))[1].Raw); got != want {
		t.Fatalf("a tool with its own failure_message changed:\n got %s\nwant %s", got, want)
	}
	if prompts.CompactASCII(tools[0]) != before || prompts.CompactASCII(prompts.NativeTools(tools)[0]) != nativeBefore {
		t.Fatal("nativePayload changed the cached tool or NativeTools")
	}
	// Not an object schema: passed through.
	odd := toolsFrom(t, `[{"type": "function", "function": {"name": "odd", "parameters": {"type": "string"}}}]`)
	if got, want := string(nativePayload(odd)[0].Raw), string(llmTools(prompts.NativeTools(odd))[0].Raw); got != want {
		t.Fatalf("non-object schema changed: %s", got)
	}

	// The warmup and the turn send the same tools.
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("np1", searchTools)
	ve.eng.say("Sure.")
	ve.turn("/api/v0/voice/command", "np1", "what time is it", nil).want(200)
	reqs := ve.eng.requests()
	if len(reqs) < 2 || reqs[0]["max_tokens"] != float64(1) {
		t.Fatalf("no warmup request first: %d requests", len(reqs))
	}
	warm, turn := jsonOf(reqs[0]["tools"]), jsonOf(reqs[len(reqs)-1]["tools"])
	if warm != turn {
		t.Fatalf("warmup and engine tools differ:\n%s\n%s", warm, turn)
	}
	f := toolByName(reqs[0]["tools"].([]any), "web_lookup")
	params := f["parameters"].(map[string]any)
	if _, ok := params["properties"].(map[string]any)["failure_message"]; !ok {
		t.Fatalf("no failure_message sent: %v", params)
	}
	if strings.Contains(jsonOf(params["required"]), "failure_message") {
		t.Fatal("failure_message is required")
	}
}

// The model's failure_message rides on the call (as on the text path) and never reaches the tool.
func TestNativeFailureMessageStripped(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("np2", searchTools)
	ve.eng.push(nativeCall("call_q1", "web_lookup", `{"query":"x","failure_message":"Couldn't reach it."}`))
	b := ve.turn("/api/v0/voice/command", "np2", "look up x", nil).want(200).json()
	calls, _ := b["tool_calls"].([]any)
	if b["stop_reason"] != "tool_calls" || len(calls) != 1 {
		t.Fatalf("%v", b)
	}
	c := calls[0].(map[string]any)
	if c["failure_message"] != "Couldn't reach it." {
		t.Fatalf("failure_message %v", c["failure_message"])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(c["function"].(map[string]any)["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args["query"] != "x" {
		t.Fatalf("tool got %v", args)
	}

	// Arguments without it, or that aren't an object, pass through untouched.
	for _, in := range []string{`{"query":"x"}`, `[1, 2]`, `not json`, `{"failure_message": 3}`} {
		if got, fm := takeFailureMessage(in); got != in || fm != "" {
			t.Fatalf("%s → %s, %q", in, got, fm)
		}
	}
	if got, _ := takeFailureMessage(`{"b": 1, "failure_message": "no", "a": 2}`); got != `{"b": 1, "a": 2}` {
		t.Fatalf("key order: %s", got)
	}
}

// Blueprint §6 (b): a call llama-server left in the content is rescued as on the text path (#56),
// but only for an offered tool.
func TestNativeRescue(t *testing.T) {
	cases := []struct {
		name, content, tool string
		call                bool
		prose               string
	}{
		{"tagged", `<tool_call>{"name": "web_lookup", "arguments": {"query": "x"}}</tool_call>`, "web_lookup", true, ""},
		{"bare", `Typing now. {"name": "web_lookup", "arguments": {"query": "x"}}`, "web_lookup", true, "Typing now."},
		{"not offered tagged", `<tool_call>{"name": "nope_tool", "arguments": {}}</tool_call>`, "", false, ""},
		{"not offered bare", `Typing now. {"name": "nope_tool", "arguments": {}}`, "", false, "Typing now."},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
			cid := "nr" + string(rune('a'+i))
			ve.start(cid, searchTools)
			ve.eng.say(c.content)
			ve.eng.say("Here you go.")
			b := ve.turn("/api/v0/voice/command", cid, "look up x", nil).want(200).json()
			calls, _ := b["tool_calls"].([]any)
			if !c.call {
				if len(calls) != 0 || b["stop_reason"] == "tool_calls" {
					t.Fatalf("unoffered tool called: %v", b)
				}
				if c.prose != "" && !strings.HasPrefix(b["assistant_message"].(string), c.prose) {
					t.Fatalf("prose lost: %v", b)
				}
				return
			}
			if b["stop_reason"] != "tool_calls" || len(calls) != 1 || !strings.Contains(jsonOf(calls[0]), c.tool) {
				t.Fatalf("not rescued: %v", b)
			}
			if msg, _ := b["assistant_message"].(string); msg != c.prose {
				t.Fatalf("prose lost: %v", b)
			}
		})
	}
}
