package admin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

// chatLLM is a fake OpenAI-compatible engine: the warmup's 1-token call gets nothing, then
// each chat call takes the next scripted answer.
type chatLLM struct {
	mu     sync.Mutex
	script []string
}

func (f *chatLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	content := ""
	if mt, _ := body["max_tokens"].(float64); mt != 1 {
		f.mu.Lock()
		if len(f.script) > 0 {
			content, f.script = f.script[0], f.script[1:]
		} else {
			content = "ok"
		}
		f.mu.Unlock()
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content}}}})
		e, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", b, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion",
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}})
}

type chatResolver struct{ url string }

func (r chatResolver) Resolve(context.Context, string) (llm.Endpoint, error) {
	return llm.Endpoint{BaseURL: r.url, Model: "fake.gguf", ContextLength: 8192}, nil
}

// fakeNode stands in for a node behind MQTT: cc publishes report_tools and tool_call to it,
// and it answers the way a real node does, over HTTP with its node key.
type fakeNode struct {
	mu      sync.Mutex
	ccURL   string
	id, key string
	got     []string
}

const weatherToolJSON = `[{"type": "function", "function": {"name": "get_weather", "description": "Current weather",
	"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}}}]`

func (n *fakeNode) Publish(topic string, payload []byte, _ byte, _ bool) error {
	var msgs []map[string]any
	if err := json.Unmarshal(payload, &msgs); err != nil || len(msgs) == 0 {
		return nil
	}
	cmd, _ := msgs[0]["command"].(string)
	d, _ := msgs[0]["details"].(map[string]any)
	rid, _ := d["reply_request_id"].(string)
	n.mu.Lock()
	n.got = append(n.got, cmd)
	n.mu.Unlock()
	var body, path string
	switch cmd {
	case "report_tools":
		body, path = `{"client_tools": `+weatherToolJSON+`, "available_commands": [], "installed_packages": []}`, "/api/v0/mobile/node-tool-reports/"
	case "tool_call":
		body, path = `{"output": {"success": true, "message": "Sunny, 21 C in Boston."}}`, "/api/v0/device-control-results/"
	default:
		return nil
	}
	go func() {
		req, _ := http.NewRequest("POST", n.ccURL+path+rid, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", n.id+":"+n.key)
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	return nil
}

func (n *fakeNode) Request(context.Context, string, []byte, string) ([]byte, error) { return nil, nil }

func (n *fakeNode) commands() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.got...)
}

// TestChatThroughGatewayEndToEnd is the web app's chat smoke (webapp 01 P3): a household
// member logs in on the admin port, lists the household's nodes, fetches the node's tools,
// warms up and chats, all through the gateway's member routes, with a fake LLM and a fake
// node answering a tool call; the stream ends in a `done` carrying the answer.
func TestChatThroughGatewayEndToEnd(t *testing.T) {
	eng := &chatLLM{script: []string{
		`<tool_call>{"name": "get_weather", "arguments": {"city": "Boston"}}</tool_call>`,
	}}
	srv := httptest.NewServer(eng)
	t.Cleanup(srv.Close)
	node := &fakeNode{}
	base, authURL, ccURL, _, home := memberStackWith(t, func(c *ccmod.Module) {
		c.LLM = llm.NewService(llm.ServiceConfig{Resolver: chatResolver{srv.URL}})
		c.Endpoints = chatResolver{srv.URL}.Resolve
		c.DefaultPromptProvider = func(context.Context) string { return prompts.Qwen3_8B }
		// The 8B's mode: a local model whose template has text tools and the thinking switch.
		c.LiveModelTraits = func(context.Context) llm.ModelTraits {
			return llm.ModelTraits{ToolCalling: "text", ToolCallingFrom: "template", Thinking: true, Local: true}
		}
		c.Publisher = node
	})
	node.ccURL = ccURL

	tok, err := os.ReadFile(authmod.SetupTokenPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, "POST", base+"/api/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		authmod.SetupTokenHeader, strings.TrimSpace(string(tok))); code != 201 {
		t.Fatalf("setup: %d %v", code, out)
	}
	root, _ := login(t, base, "root@example.com")
	code, out := call(t, "POST", authURL+"/households", map[string]any{"name": "Home"}, bearer(root)...)
	if code != 201 {
		t.Fatalf("household: %d %v", code, out)
	}
	hh := obj(t, out)["id"].(string)
	code, out = call(t, "POST", authURL+"/auth/register", map[string]any{"email": "m@example.com", "password": "password1"})
	if code != 201 && code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	uid := obj(t, obj(t, out)["user"])["id"]
	if code, out := call(t, "POST", authURL+"/households/"+hh+"/members", map[string]any{"user_id": uid, "role": "member"}, bearer(root)...); code != 201 && code != 200 {
		t.Fatalf("member: %d %v", code, out)
	}

	// The node, heard from once over HTTP (that makes it reachable for round trips).
	code, out = call(t, "POST", ccURL+"/api/v0/admin/nodes", map[string]any{"node_id": "kitchen", "household_id": hh, "room": "kitchen"},
		"X-API-Key", tierCCAdminKey)
	if code != 201 && code != 200 {
		t.Fatalf("node: %d %v", code, out)
	}
	node.id, node.key = "kitchen", obj(t, out)["node_key"].(string)
	if code, out := call(t, "POST", ccURL+"/api/v0/admin/nodes/heartbeat", map[string]any{}, "X-API-Key", node.id+":"+node.key); code != 200 {
		t.Fatalf("heartbeat: %d %v", code, out)
	}

	// Everything below goes through the admin port as the member, as the web app does.
	member, _ := login(t, base, "m@example.com")
	code, out = call(t, "GET", base+"/api/cc/api/v0/admin/nodes?household_id="+hh, nil, bearer(member)...)
	if l, ok := out.([]any); code != 200 || !ok || len(l) != 1 || obj(t, l[0])["online"] != true {
		t.Fatalf("nodes: %d %v", code, out)
	}
	code, out = call(t, "GET", base+"/api/cc/api/v0/mobile/nodes/kitchen/tools", nil, bearer(member)...)
	tools, _ := obj(t, out)["client_tools"].([]any)
	if code != 200 || len(tools) != 1 {
		t.Fatalf("tools: %d %v", code, out)
	}
	code, out = call(t, "POST", base+"/api/cc/api/v0/mobile/chat/warmup",
		map[string]any{"node_id": "kitchen", "household_id": hh, "timezone": "UTC", "client_tools": tools}, bearer(member)...)
	if code != 200 {
		t.Fatalf("warmup: %d %v", code, out)
	}
	cid := obj(t, out)["conversation_id"].(string)

	b, _ := json.Marshal(map[string]any{"message": "weather in boston", "node_id": "kitchen", "household_id": hh,
		"conversation_id": cid, "timezone": "UTC", "include_reasoning": true})
	req, _ := http.NewRequest("POST", base+"/api/cc/api/v0/mobile/chat", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+member)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("chat: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var types []string
	var done map[string]any
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		types = append(types, ev["type"].(string))
		if ev["type"] == "done" || ev["type"] == "error" {
			done = ev
		}
	}
	if done == nil || done["type"] != "done" {
		t.Fatalf("no done event: %v (last %v)", types, done)
	}
	// cc answers with the node tool's own message when it is complete (no second LLM call).
	if done["full_text"] != "Sunny, 21 C in Boston." || done["conversation_id"] != cid {
		t.Fatalf("done %v", done)
	}
	if types[0] != "acknowledgment" || !contains(types, "status") || !contains(types, "delta") {
		t.Fatalf("event types %v", types)
	}
	// The node ran the tool the model asked for.
	if got := strings.Join(node.commands(), ","); !strings.Contains(got, "report_tools") || !strings.Contains(got, "tool_call") {
		t.Fatalf("node commands %v", got)
	}
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}
