package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// chatNodePub is a node behind the broker: it answers report_tools and tool_call publishes
// the way a node POSTs its results, and records every command.
type chatNodePub struct {
	mu        sync.Mutex
	m         *Module
	cmds      []map[string]any
	tools     string // report_tools body ("" = never answers)
	toolReply string // tool_call body ("" = never answers)
}

func (p *chatNodePub) Publish(topic string, payload []byte, _ byte, _ bool) error {
	var msgs []map[string]any
	if err := json.Unmarshal(payload, &msgs); err != nil || len(msgs) == 0 {
		return nil
	}
	p.mu.Lock()
	p.cmds = append(p.cmds, msgs[0])
	tools, reply := p.tools, p.toolReply
	p.mu.Unlock()
	node := strings.Split(topic, "/")[2]
	d := msgs[0]["details"].(map[string]any)
	rid, _ := d["reply_request_id"].(string)
	body := ""
	switch msgs[0]["command"] {
	case "report_tools":
		body = tools
	case "tool_call":
		body = reply
	}
	if body != "" {
		go func() {
			time.Sleep(5 * time.Millisecond)
			p.m.bus.Deliver(rid, node, json.RawMessage(body))
		}()
	}
	return nil
}

func (p *chatNodePub) Request(context.Context, string, []byte, string) ([]byte, error) {
	return nil, nil
}

func (p *chatNodePub) commands(verb string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, c := range p.cmds {
		if c["command"] == verb {
			out = append(out, c["details"].(map[string]any))
		}
	}
	return out
}

type chatEnv struct {
	*voiceEnv
	pub *chatNodePub
}

func newChatEnv(t *testing.T, provider string) *chatEnv {
	t.Helper()
	pub := &chatNodePub{tools: `{"client_tools": ` + weatherTool + `, "available_commands": [], "installed_packages": []}`}
	ve := newVoiceEnv(t, provider, func(m *Module) { m.Publisher = pub; pub.m = m })
	ce := &chatEnv{voiceEnv: ve, pub: pub}
	ce.setOnline(true)
	old := chatWordPause
	chatWordPause = time.Millisecond
	t.Cleanup(func() { chatWordPause = old })
	return ce
}

func (ce *chatEnv) setOnline(online bool) {
	ce.t.Helper()
	seen := ce.clock()
	if !online {
		seen = seen.Add(-time.Hour)
	}
	if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ? WHERE node_id = ?`, dbTime(seen), ce.node.id); err != nil {
		ce.t.Fatal(err)
	}
}

func (ce *chatEnv) warm(tok string) string {
	ce.t.Helper()
	r := ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": ce.node.id, "household_id": voiceHH},
		bearer(tok)).want(200).json()
	return r["conversation_id"].(string)
}

// chat posts one message and returns the SSE frames, each checked to be one Python-framed
// `data: <json>` line.
func (ce *chatEnv) chat(tok string, body map[string]any) []string {
	ce.t.Helper()
	b := map[string]any{"node_id": ce.node.id, "household_id": voiceHH}
	for k, v := range body {
		b[k] = v
	}
	r := ce.do("POST", "/api/v0/mobile/chat", b, bearer(tok)).want(200)
	if ct := r.header.Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		ce.t.Fatalf("content type %q", ct)
	}
	if r.header.Get("Cache-Control") != "no-cache" || r.header.Get("X-Accel-Buffering") != "no" {
		ce.t.Fatalf("headers %v", r.header)
	}
	return sseFrames(ce.t, string(r.body))
}

func sseFrames(t *testing.T, body string) []string {
	t.Helper()
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("stream does not end with a blank line: %q", body)
	}
	var out []string
	for _, f := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if !strings.HasPrefix(f, "data: ") || strings.Contains(f, "\n") {
			t.Fatalf("bad SSE frame %q", f)
		}
		js := strings.TrimPrefix(f, "data: ")
		v, err := pyjson.Loads(js)
		if err != nil {
			t.Fatalf("frame JSON %q: %v", js, err)
		}
		// Python's json.dumps framing: re-encoding gives the same bytes.
		if again := pyjson.Dumps(v, true); again != js {
			t.Fatalf("frame is not json.dumps-shaped:\n got %s\nwant %s", js, again)
		}
		out = append(out, js)
	}
	return out
}

var traceRE = regexp.MustCompile(`"trace_summary": \{"total_duration_ms": [0-9.]+, "span_count": \d+, "status": "(ok|error)", "service_hops": \[.*\]\}`)

// normalize replaces the timing-dependent trace summary.
func normalize(frames []string) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = traceRE.ReplaceAllString(f, `"trace_summary": "<$1>"`)
	}
	return out
}

func event(t *testing.T, frame string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(frame), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func wantFrames(t *testing.T, got []string, want ...string) {
	t.Helper()
	n := normalize(got)
	if strings.Join(n, "\n") != strings.Join(want, "\n") {
		t.Fatalf("SSE frames:\n%s\nwant:\n%s", strings.Join(n, "\n"), strings.Join(want, "\n"))
	}
}

// TestMobileChatGoldenProse: a warm turn, byte-for-byte legacy framing (json.dumps, ASCII
// escapes), one delta per word with the replay pause, then done.
func TestMobileChatGoldenProse(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	cid := ce.warm("tok-7")
	if !regexp.MustCompile(`^mobile-[0-9a-f]{12}$`).MatchString(cid) {
		t.Fatalf("conversation id %q", cid)
	}
	chatWordPause = 20 * time.Millisecond
	ce.eng.say("Café opens at noon today.")
	start := time.Now()
	frames := ce.chat("tok-7", map[string]any{"message": "when does the café open", "conversation_id": cid})
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Fatalf("5 words replayed in %v: the 20 ms pause per word is kept (D32)", el)
	}
	ack := event(t, frames[0])
	wantFrames(t, frames,
		`{"type": "acknowledgment", "text": `+pyjson.Dumps(ack["text"], true)+`}`,
		`{"type": "delta", "text": "Caf\u00e9 "}`,
		`{"type": "delta", "text": "opens "}`,
		`{"type": "delta", "text": "at "}`,
		`{"type": "delta", "text": "noon "}`,
		`{"type": "delta", "text": "today."}`,
		`{"type": "done", "conversation_id": "`+cid+`", "full_text": "Caf\u00e9 opens at noon today.", "stop_reason": "complete", "trace_summary": "<ok>"}`,
	)
	if ack["type"] != "acknowledgment" || ack["text"] == "" {
		t.Fatalf("ack %v", ack)
	}
	// The trace summary has the legacy shape.
	ts := event(t, frames[len(frames)-1])["trace_summary"].(map[string]any)
	if ts["span_count"] != 1.0 || ts["status"] != "ok" {
		t.Fatalf("trace summary %v", ts)
	}
	hops := ts["service_hops"].([]any)
	if len(hops) != 1 || fmt.Sprint(hops[0].(map[string]any)["steps"]) != "[process_command]" {
		t.Fatalf("hops %v", hops)
	}

	// The chat turn ran the voice pipeline as the JWT user, with the chat turn hint.
	msgs := messagesOf(ce.eng.last())
	var speaker, user string
	for _, m := range msgs {
		c, _ := m["content"].(string)
		if strings.HasPrefix(c, "You are speaking with ") {
			speaker = c
		}
		if m["role"] == "user" {
			user = c
		}
	}
	if !strings.HasPrefix(speaker, "You are speaking with alex.") {
		t.Fatalf("speaker block %q", speaker)
	}
	if !strings.Contains(user, "[turn context: typed message") {
		t.Fatalf("user message %q", user)
	}
	// Three-word answers are not paced; short replies are a single word frame each.
	chatWordPause = time.Millisecond
	ce.eng.say("Yes.")
	wantFrames(t, normalize(ce.chat("tok-7", map[string]any{"message": "ok?", "conversation_id": cid}))[1:],
		`{"type": "delta", "text": "Yes."}`,
		`{"type": "done", "conversation_id": "`+cid+`", "full_text": "Yes.", "stop_reason": "complete", "trace_summary": "<ok>"}`)
}

// TestMobileChatColdStart: no conversation id warms first (status), under a fresh id.
func TestMobileChatColdStart(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.eng.say("Hello there.")
	frames := ce.chat("tok-7", map[string]any{"message": "hi"})
	if frames[0] != `{"type": "status", "message": "Starting conversation..."}` {
		t.Fatalf("first frame %s", frames[0])
	}
	if event(t, frames[1])["type"] != "acknowledgment" {
		t.Fatalf("second frame %s", frames[1])
	}
	done := event(t, frames[len(frames)-1])
	cid, _ := done["conversation_id"].(string)
	if done["type"] != "done" || !strings.HasPrefix(cid, "mobile-") || ce.m.convs.get(cid) == nil {
		t.Fatalf("done %v", done)
	}
	ts := done["trace_summary"].(map[string]any)
	if ts["span_count"] != 2.0 {
		t.Fatalf("spans %v", ts)
	}
	// The cold warmup asked the (online) node for its tools.
	if len(ce.pub.commands("report_tools")) != 1 {
		t.Fatal("warmup did not fetch the node's tools")
	}
	// An expired id is warmed up under the SAME id (13 §7.3).
	ce.eng.say("Again.")
	frames = ce.chat("tok-7", map[string]any{"message": "hi", "conversation_id": "mobile-abcdefabcdef"})
	if frames[0] != `{"type": "status", "message": "Starting conversation..."}` ||
		event(t, frames[len(frames)-1])["conversation_id"] != "mobile-abcdefabcdef" {
		t.Fatalf("expired id %v", frames)
	}
}

// TestMobileChatNodeToolRoundTrip: tool_calls run headlessly on the selected node, actions
// are harvested, the intermediate message is a vanishing delta, and the continue's answer is
// replayed (text path, D23 fast path).
func TestMobileChatNodeToolRoundTrip(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	ce.pub.toolReply = `{"output": {"success": true, "message": "Draft ready: hi mom.", "actions": [{"button_text": "Send", ` +
		`"button_action": "send", "button_type": "primary"}], "context": {"draft": "hi mom", "preview": "To Mom: hi mom"}}}`
	r := ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": ce.node.id, "household_id": voiceHH}, bearer("tok-7")).want(200).json()
	if r["tools_loaded"] != 1.0 {
		t.Fatalf("warmup %v", r)
	}
	cid := r["conversation_id"].(string)
	ce.eng.push(engineReply{content: "Let me check.", toolCalls: []map[string]any{{"id": "call_w1", "type": "function",
		"function": map[string]any{"name": "get_weather", "arguments": `{"city": "Boston"}`}}}})
	ce.eng.say("Draft ready: hi mom.")
	frames := ce.chat("tok-7", map[string]any{"message": "weather in boston", "conversation_id": cid})
	n := normalize(frames)
	if event(t, n[0])["type"] != "acknowledgment" {
		t.Fatal(n[0])
	}
	var types []string
	for _, f := range n[1:] {
		e := event(t, f)
		types = append(types, fmt.Sprint(e["type"]))
	}
	// The intermediate message is one delta (with a trailing space) that done.full_text then
	// replaces (D40 Q5).
	if strings.Join(types, ",") != "delta,status,delta,delta,delta,delta,done" || n[1] != `{"type": "delta", "text": "Let me check. "}` {
		t.Fatalf("event order %v", n)
	}
	last := n[len(n)-1]
	want := `{"type": "done", "conversation_id": "` + cid + `", "full_text": "Draft ready: hi mom.", "stop_reason": "complete", "trace_summary": "<ok>", ` +
		`"actions": [{"button_text": "Send", "button_action": "send", "button_type": "primary"}], ` +
		`"action_context": {"command_name": "get_weather", "context": {"draft": "hi mom", "preview": "To Mom: hi mom"}}, ` +
		`"action_preview": "To Mom: hi mom"}`
	if last != want {
		t.Fatalf("done\n got %s\nwant %s", last, want)
	}
	// What the node got: tool_call with the typed message, the user, the call id, no trusted.
	calls := ce.pub.commands("tool_call")
	if len(calls) != 1 {
		t.Fatalf("tool calls %v", calls)
	}
	d := calls[0]
	if d["command_name"] != "get_weather" || d["voice_command"] != "weather in boston" || d["user_id"] != 7.0 ||
		d["reply_request_id"] != d["request_id"] || d["trusted"] != nil || d["tool_call_id"] != "call_w1" {
		t.Fatalf("details %v", d)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(d["arguments"].(string)), &args); err != nil || args["city"] != "Boston" {
		t.Fatalf("arguments %v (%v)", d["arguments"], err)
	}
	// The trace has the node hop.
	ts := event(t, frames[len(frames)-1])["trace_summary"].(map[string]any)
	var services []string
	for _, h := range ts["service_hops"].([]any) {
		services = append(services, h.(map[string]any)["service"].(string))
	}
	if !strings.Contains(strings.Join(services, ","), "node") {
		t.Fatalf("hops %v", ts)
	}
}

// TestMobileChatOfflineNode: warmup skips the MQTT fetch for an offline node and uses the
// client's tools (Q7); a tool call fails fast with a status event (Q8).
func TestMobileChatOfflineNode(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.setOnline(false)
	r := ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": ce.node.id, "household_id": voiceHH,
		"client_tools": json.RawMessage(weatherTool)}, bearer("tok-7")).want(200).json()
	if r["tools_loaded"] != 1.0 || len(ce.pub.cmds) != 0 {
		t.Fatalf("offline warmup %v, published %v", r, ce.pub.cmds)
	}
	ce.eng.say(`<tool_call>{"name": "get_weather", "arguments": {}}</tool_call>`)
	ce.eng.say("Your kitchen node is offline right now.")
	start := time.Now()
	frames := normalize(ce.chat("tok-7", map[string]any{"message": "weather", "conversation_id": r["conversation_id"]}))
	if time.Since(start) > 5*time.Second || len(ce.pub.cmds) != 0 {
		t.Fatal("offline node was waited on")
	}
	if frames[1] != `{"type": "status", "message": "Node is offline"}` {
		t.Fatalf("frames %v", frames)
	}
	done := event(t, frames[len(frames)-1])
	if done["full_text"] != "Your kitchen node is offline right now." {
		t.Fatalf("done %v", done)
	}
	// The LLM saw the failure.
	msgs := messagesOf(ce.eng.last())
	if c := msgs[len(msgs)-1]["content"].(string); !strings.Contains(c, "the node is offline") {
		t.Fatalf("formatting prompt %q", c)
	}
}

func TestMobileChatValidationAndReasoning(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	cid := ce.warm("tok-7")
	ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_v", "type": "function",
		"function": map[string]any{"name": "request_validation", "arguments": `{"question":"Which room?","parameter_name":"room","options":["kitchen","den"]}`}}}})
	frames := normalize(ce.chat("tok-7", map[string]any{"message": "turn on the light", "conversation_id": cid}))
	want := `{"type": "done", "conversation_id": "` + cid + `", "full_text": "Which room?", "stop_reason": "validation_required", ` +
		`"validation": {"question": "Which room?", "parameter_name": "room", "options": ["kitchen", "den"]}, "trace_summary": "<ok>"}`
	if len(frames) != 2 || frames[1] != want {
		t.Fatalf("frames %v", frames)
	}

	ce.eng.say("<think>the user wants a joke</think>Why not.")
	frames = ce.chat("tok-7", map[string]any{"message": "tell me a joke", "conversation_id": cid, "include_reasoning": true})
	done := event(t, frames[len(frames)-1])
	if done["reasoning"] != "the user wants a joke" || done["full_text"] != "Why not." {
		t.Fatalf("done %v", done)
	}
	ce.eng.say("<think>again</think>Sure.")
	frames = ce.chat("tok-7", map[string]any{"message": "another", "conversation_id": cid})
	if _, has := event(t, frames[len(frames)-1])["reasoning"]; has {
		t.Fatal("reasoning without include_reasoning")
	}
}

func TestMobileChatErrors(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ce.auth.addUser(9, "hh-other", "member")
	other := ce.createNode("node-other", "hh-other")
	chat := func(tok string, b map[string]any) *resp {
		return ce.do("POST", "/api/v0/mobile/chat", b, bearer(tok))
	}
	ok := map[string]any{"message": "hi", "node_id": ce.node.id, "household_id": voiceHH}
	ce.do("POST", "/api/v0/mobile/chat", ok, nil).want(401)
	// Cross-household: not a member of the household, or the node isn't in it.
	chat("tok-9", ok).detail(403, "User is not a member of this household")
	chat("tok-7", map[string]any{"message": "hi", "node_id": other.id, "household_id": voiceHH}).
		detail(404, "Node node-other not found in household "+voiceHH)
	ce.do("POST", "/api/v0/mobile/chat/warmup", map[string]any{"node_id": other.id, "household_id": voiceHH}, bearer("tok-7")).
		detail(404, "Node node-other not found in household "+voiceHH)
	chat("tok-9", map[string]any{"message": "hi", "node_id": ce.node.id, "household_id": "hh-other"}).
		detail(404, "Node node-v1 not found in household hh-other")
	// Validation (CC's 400 shape).
	d := chat("tok-7", map[string]any{"message": "", "household_id": voiceHH}).want(400).json()["details"]
	if fmt.Sprint(d) != "[body -> message: String should have at least 1 character body -> node_id: Field required]" {
		t.Fatalf("validation %v", d)
	}
	d = chat("tok-7", map[string]any{"message": strings.Repeat("x", 5001), "node_id": ce.node.id, "household_id": voiceHH}).want(400).json()["details"]
	if fmt.Sprint(d) != "[body -> message: String should have at most 5000 characters]" {
		t.Fatalf("validation %v", d)
	}

	// A failing warmup ends the stream with one error event (HTTP stays 200).
	ce.set(settingPromptProvider, "NoSuchProvider", settings.Scope{})
	frames := ce.chat("tok-7", map[string]any{"message": "hi"})
	last := event(t, frames[len(frames)-1])
	if len(frames) != 2 || last["type"] != "error" || !strings.Contains(last["message"].(string), "NoSuchProvider") ||
		!strings.HasPrefix(last["conversation_id"].(string), "mobile-") {
		t.Fatalf("frames %v", frames)
	}
	if ts := last["trace_summary"].(map[string]any); ts["status"] != "error" {
		t.Fatalf("trace %v", ts)
	}
	ce.set(settingPromptProvider, prompts.Qwen3_8B, settings.Scope{})

	// An LLM failure mid-turn: error event.
	cid := ce.warm("tok-7")
	ce.eng.srv.Close()
	frames = normalize(ce.chat("tok-7", map[string]any{"message": "hi", "conversation_id": cid}))
	if e := event(t, frames[len(frames)-1]); e["type"] != "error" || e["message"] == "" || e["conversation_id"] != cid {
		t.Fatalf("frames %v", frames)
	}
}

// TestMobileChatForeignConversation: a conversation id that isn't the caller's chat with this
// node is never joined (another member's, or a node's voice conversation).
func TestMobileChatForeignConversation(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	alexCID := ce.warm("tok-7")
	ce.start("voice-1", weatherTool)
	for _, foreign := range []string{alexCID, "voice-1"} {
		ce.eng.say("Hi Sam.")
		frames := ce.chat("tok-8", map[string]any{"message": "hello", "conversation_id": foreign})
		if frames[0] != `{"type": "status", "message": "Starting conversation..."}` {
			t.Fatalf("frames %v", frames)
		}
		cid := event(t, frames[len(frames)-1])["conversation_id"].(string)
		if cid == foreign || ce.m.convs.get(cid).chatUserID != 8 {
			t.Fatalf("joined %s as %s", foreign, cid)
		}
	}
	if c := ce.m.convs.get(alexCID); c.chatUserID != 7 || len(c.messages) != 1 {
		t.Fatalf("alex's conversation was touched: %+v", c.messages)
	}
}

// TestMobileChatTranscriptsD19: the chat owner is a known speaker, so a completed exchange is
// logged for extraction even with speaker recognition off; memory off logs nothing.
func TestMobileChatTranscriptsD19(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	cid := ce.warm("tok-7")
	ce.eng.say("Noted.")
	ce.chat("tok-7", map[string]any{"message": "I like jazz", "conversation_id": cid})
	rows := ce.transcripts()
	if len(rows) != 1 || rows[0]["user_id"] != int64(7) || rows[0]["user"] != "I like jazz" || rows[0]["assistant"] != "Noted." ||
		rows[0]["conversation_id"] != cid {
		t.Fatalf("transcripts %v", rows)
	}
	ce.set(settingMemoryEnabled, false, settings.Scope{HouseholdID: voiceHH})
	ce.eng.say("Okay.")
	ce.chat("tok-7", map[string]any{"message": "I like blues", "conversation_id": cid})
	if len(ce.transcripts()) != 1 {
		t.Fatal("memory off still logged a chat transcript (D19)")
	}
	// The memory tools are offered to a chat (the speaker is known) while memory is on.
	ce.set(settingMemoryEnabled, true, settings.Scope{HouseholdID: voiceHH})
	cid = ce.warm("tok-7")
	if conv := ce.m.convs.get(cid); !conv.serverNames["remember"] {
		t.Fatalf("remember not offered to a chat: %v", conv.serverNames)
	}
}

// TestMobileChatContext: the selected node's room, the household's devices as agent context
// (doc 07) and the ambient bundle (when opted in) reach the prompt.
func TestMobileChatContext(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	if _, err := ce.d.Write.ExecContext(ctx, `INSERT INTO cc_rooms (id, household_id, name, normalized_name) VALUES ('r1', ?, 'Den', 'den')`, voiceHH); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO cc_devices (id, household_id, room_id, entity_id, name, domain) VALUES ('d1', '` + voiceHH + `', 'r1', 'light.den_lamp', 'Den Lamp', 'light')`,
		`INSERT INTO cc_devices (id, household_id, entity_id, name, domain, is_active) VALUES ('d2', '` + voiceHH + `', 'light.gone', 'Gone', 'light', 0)`,
		`INSERT INTO cc_user_memories (household_id, category, content, updated_at) VALUES ('` + voiceHH + `', 'weather', 'Tomorrow''s forecast: rain', '2026-10-06T11:00:00.000Z')`,
		`INSERT INTO cc_user_memories (household_id, category, content, updated_at) VALUES ('` + voiceHH + `', 'weather', 'Current weather: 61F, cloudy', '2026-10-06T10:00:00.000Z')`,
		`INSERT INTO cc_user_memories (household_id, category, content) VALUES ('` + voiceHH + `', 'calendar', 'Dentist at 3pm')`,
		`INSERT INTO cc_signals (household_id, kind, subject, source_key, summary) VALUES ('` + voiceHH + `', 'presence', 'alex', 'k1', 'Alex is home.')`,
	} {
		if _, err := ce.d.Write.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	cid := ce.warm("tok-7")
	conv := ce.m.convs.get(cid)
	if conv.ambient != "" {
		t.Fatal("ambient bundle without the opt-in")
	}
	agents := pyjson.Dumps(conv.agents, false)
	if agents != `{"home_assistant": {"device_controls": {"light": [{"entity_id": "light.den_lamp", "name": "Den Lamp", "area": "Den", "state": "unknown"}]}}}` {
		t.Fatalf("agents %s", agents)
	}
	if !strings.Contains(conv.messages[0].Content, "light.den_lamp") || conv.room != "kitchen" {
		t.Fatalf("device context missing from the system prompt (room %q)", conv.room)
	}
	if conv.timezone != chatDefaultTimezone {
		t.Fatalf("timezone %q", conv.timezone)
	}

	ce.set(settingAmbientContext, true, settings.Scope{HouseholdID: voiceHH})
	cid = ce.warm("tok-7")
	conv = ce.m.convs.get(cid)
	want := "As of 8:00 AM, Tuesday, Oct 6.\nWeather: Current weather: 61F, cloudy\nToday: Dentist at 3pm\nAlex is home."
	if conv.ambient != want {
		t.Fatalf("ambient %q", conv.ambient)
	}
	ce.eng.say("Fine.")
	ce.chat("tok-7", map[string]any{"message": "how is my day", "conversation_id": cid})
	msgs := messagesOf(ce.eng.last())
	var order []string
	for _, m := range msgs[1:] {
		c := m["content"].(string)
		switch {
		case strings.HasPrefix(c, "You are speaking with"):
			order = append(order, "speaker")
		case strings.HasPrefix(c, prompts.AmbientBlock(want)):
			order = append(order, "ambient")
		case m["role"] == "user":
			order = append(order, "user")
		}
	}
	if strings.Join(order, ",") != "speaker,ambient,user" {
		t.Fatalf("per-turn order %v", order)
	}
	// Voice turns get it too (01 §3.3 C.6).
	ce.start("v-amb", weatherTool)
	if ce.m.convs.get("v-amb").ambient != want {
		t.Fatal("voice warmup without the ambient bundle")
	}
}

func TestMobileChatNotForMe(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_8B)
	cid := ce.warm("tok-7")
	ce.eng.say("<not_for_me/>")
	ce.eng.say("<not_for_me/>") // a chat turn earns one /think double check
	frames := normalize(ce.chat("tok-7", map[string]any{"message": "she said so", "conversation_id": cid}))
	if frames[len(frames)-1] != `{"type": "done", "conversation_id": "`+cid+`", "full_text": "", "stop_reason": "not_for_me", "trace_summary": "<ok>"}` {
		t.Fatalf("frames %v", frames)
	}
}

func TestChatTraceSummaryLeafHops(t *testing.T) {
	tr := &chatTrace{t0: time.Now(), status: "ok", list: []chatSpan{
		{name: "warmup", service: "cc", status: "ok", start: 0, end: 10 * time.Millisecond},
		{name: "process_command", service: "cc", status: "ok", start: 10 * time.Millisecond, end: 30 * time.Millisecond},
		{name: "llm", service: "llm_proxy", status: "ok", start: 12 * time.Millisecond, end: 20 * time.Millisecond},
		{name: "mqtt_tool_x", service: "node", status: "error", start: 31 * time.Millisecond, end: 40 * time.Millisecond},
		{name: "mqtt_tool_y", service: "node", status: "ok", start: 40 * time.Millisecond, end: 45 * time.Millisecond},
		{name: "zero", service: "cc", status: "ok", start: 45 * time.Millisecond, end: 45 * time.Millisecond},
	}}
	got := traceRE.ReplaceAllString(`"trace_summary": `+pyjson.Dumps(tr.summary(), true), "X")
	if got != "X" {
		t.Fatalf("shape %s", got)
	}
	hops := pyjson.Dumps(func() any { v, _ := tr.summary().Get("service_hops"); return v }(), true)
	if hops != `[{"service": "cc", "duration_ms": 10.0, "status": "ok", "steps": ["warmup"]}, `+
		`{"service": "llm_proxy", "duration_ms": 8.0, "status": "ok", "steps": ["llm"]}, `+
		`{"service": "node", "duration_ms": 14.0, "status": "error", "steps": ["mqtt_tool_x", "mqtt_tool_y"]}]` {
		t.Fatal(hops)
	}
	if n, _ := tr.summary().Get("span_count"); n != 6 {
		t.Fatalf("span_count %v", n)
	}
}
