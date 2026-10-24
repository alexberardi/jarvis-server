package cc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestErrandToolsOfferedAndGated(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("c1", weatherTool, `"available_commands":[{"command_name":"get_weather","description":"Weather","parameters":[{"name":"city","description":"where"}]}]`)
	conv := ve.m.convs.get("c1")
	for _, n := range errands.ToolNames {
		if !conv.serverNames[n] {
			t.Fatalf("%s not offered: %v", n, conv.serverNames)
		}
	}
	ve.set(errands.SettingEnabled, false, settings.Scope{HouseholdID: voiceHH})
	ve.start("c2", weatherTool)
	conv = ve.m.convs.get("c2")
	for _, n := range errands.ToolNames {
		if conv.serverNames[n] {
			t.Fatalf("%s offered with errands off", n)
		}
	}
	if len(ve.m.conversationCommands("c1")) == 0 {
		t.Fatal("run_errand can't see the conversation's commands")
	}
	// No queue in this env: the tool answers honestly instead of promising a card.
	res := ve.m.tools.Execute(context.Background(), servertools.Call{Name: "run_errand", Args: servertools.Obj("goal", "x")},
		servertools.Turn{ConversationID: "c1", HouseholdID: voiceHH, NodeID: ve.node.id})
	if got := pyjson.Dumps(res, false); got != `{"error": "task_failed", "message": "I couldn't start planning that errand: errands are unavailable"}` {
		t.Fatal(got)
	}
}

// autoNode answers tool_call and report_tools publishes the way a node posts its results.
type autoNode struct {
	mu   sync.Mutex
	m    *Module
	cmds []map[string]any
	// output, when set, is every tool_call's output.
	output string
}

func (a *autoNode) Publish(topic string, payload []byte, _ byte, _ bool) error {
	var msgs []map[string]any
	if err := json.Unmarshal(payload, &msgs); err != nil || len(msgs) == 0 {
		return nil
	}
	a.mu.Lock()
	a.cmds = append(a.cmds, msgs[0])
	a.mu.Unlock()
	node := strings.Split(topic, "/")[2]
	d := msgs[0]["details"].(map[string]any)
	reply, _ := d["reply_request_id"].(string)
	var body string
	switch msgs[0]["command"] {
	case "tool_call":
		body = `{"output": {"success": true, "message": "sunny", "echo": ` + jsonString(d["arguments"]) + `}}`
		if a.output != "" {
			body = `{"output": ` + a.output + `}`
		}
	case "report_tools":
		body = `{"client_tools": [], "available_commands": [{"command_name": "get_weather", "description": "W"}]}`
	}
	// The result slot is open before the publish, so the reply may land at once (off the
	// publisher's goroutine, as a real node's POST would).
	go a.m.bus.Deliver(reply, node, json.RawMessage(body))
	return nil
}

func (a *autoNode) Request(context.Context, string, []byte, string) ([]byte, error) { return nil, nil }

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestErrandNodeAdapter(t *testing.T) {
	pub := &autoNode{}
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Publisher = pub; pub.m = m }})
	nodes := errandNodes{e.m}
	ctx := context.Background()
	uid := int64(7)
	out := nodes.RunTool(ctx, "node-1", "get_weather", servertools.Obj("resolved_datetimes", []any{"x"}), &uid, "goal", time.Second)
	if got := pyjson.Dumps(out, false); got != `{"success": true, "message": "sunny", "echo": {"resolved_datetimes": ["x"]}}` {
		t.Fatal(got)
	}
	d := pub.cmds[0]["details"].(map[string]any)
	if d["command_name"] != "get_weather" || d["user_id"] != float64(7) || d["voice_command"] != "goal" ||
		d["tool_call_id"] != d["reply_request_id"] || d["trusted"] != nil || d["request_id"] == nil {
		t.Fatalf("details = %v", d)
	}
	cmds, ok := nodes.ReportCommands(ctx, "node-1", time.Second)
	if !ok || len(cmds) != 1 {
		t.Fatalf("report = %v %v", cmds, ok)
	}
	// A silent node times out with the legacy failure dict.
	silent := newEnv(t, envOpts{noMQTT: true})
	out = errandNodes{silent.m}.RunTool(ctx, "node-1", "x", nil, nil, "", 10*time.Millisecond)
	if got := pyjson.Dumps(out, false); got != `{"success": false, "error": "could not dispatch to node: MQTT is unavailable", "error_code": "no_broker"}` {
		t.Fatal(got)
	}
}

// TestErrandNodeImages: RunTool drops a node's attached images (labels kept), so a step can
// never store them; RunToolImages hands them back for one call; nothing answering is an error.
func TestErrandNodeImages(t *testing.T) {
	pub := &autoNode{output: `{"success": true, "image_attachments": [{"mime": "image/png", "data": "` +
		base64.StdEncoding.EncodeToString(pngBytes) + `", "label": "screen"}]}`}
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Publisher = pub; pub.m = m }})
	nodes := errandNodes{e.m}
	ctx := context.Background()
	want := `{"success": true, "image_attachments": ["screen` + toolImageNote + `"]}`
	if got := pyjson.Dumps(nodes.RunTool(ctx, "node-1", "desktop_read_screen", nil, nil, "", time.Second), false); got != want {
		t.Fatal(got)
	}
	out, imgs, err := nodes.RunToolImages(ctx, "node-1", "desktop_read_screen", nil, nil, time.Second)
	if err != nil || pyjson.Dumps(out, false) != want || len(imgs) != 1 || imgs[0].MIME != "image/png" ||
		imgs[0].Label != "screen" || !bytes.Equal(imgs[0].Data, pngBytes) {
		t.Fatalf("images = %s %+v %v", pyjson.Dumps(out, false), imgs, err)
	}
	silent := newEnv(t, envOpts{noMQTT: true})
	out, imgs, err = errandNodes{silent.m}.RunToolImages(ctx, "node-1", "x", nil, nil, 10*time.Millisecond)
	if err == nil || imgs != nil || pyjson.Dumps(out, false) != `{"success": false, "error": "could not dispatch to node: MQTT is unavailable", "error_code": "no_broker"}` {
		t.Fatalf("no MQTT = %s %v", pyjson.Dumps(out, false), err)
	}
}

func TestErrandPurgeUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.d.Write.ExecContext(ctx, `INSERT INTO cc_errand_plans (id, household_id, user_id, goal, steps, state, created_at, updated_at)
		VALUES ('pl_a', 'hh', 7, 'g', '[]', 'draft', 'n', 'n')`); err != nil {
		t.Fatal(err)
	}
	tx, _ := e.d.Write.BeginTx(ctx, nil)
	if err := e.m.PurgeUser(ctx, tx, 7); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	var n int
	_ = e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_errand_plans`).Scan(&n)
	if n != 0 {
		t.Fatal("plan survived account deletion")
	}
}

// ackTool stands in for run_errand: work queued for after the reply, or a refusal.
type ackTool struct{ result *pyjson.Object }

func (ackTool) Name() string { return "run_errand" }
func (ackTool) Definition() *pyjson.Object {
	return servertools.LegacyDefinition("run_errand")
}
func (a ackTool) Execute(context.Context, servertools.Call, servertools.Turn) (any, error) {
	return a.result, nil
}

// The demo dry run: a native model called run_errand for "set a timer", got the "I'm planning
// it" ack, and then composed "I've got the timer set for 10 minutes" (after waiting 106 s for
// the shared slot) while the plan later failed. An accepted ack ends the turn as said, with no
// second LLM call; anything else still goes back to the model.
func TestNativeAcceptedAckIsTheReply(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ack := "On it. I'll send the plan to your phone."
	ve.m.tools.Register(ackTool{servertools.Obj("status", "accepted", "message", ack)})
	ve.start("ack1", "[]")
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_e1", "type": "function",
		"function": map[string]any{"name": "run_errand", "arguments": `{"goal": "set a timer for 10 minutes"}`}}}})
	before := len(ve.eng.requests())
	b := ve.turn("/api/v0/voice/command", "ack1", "set a timer for 10 minutes", nil).want(200).json()
	if b["assistant_message"] != ack {
		t.Fatalf("reply = %v", b["assistant_message"])
	}
	if got := len(ve.eng.requests()) - before; got != 1 {
		t.Fatalf("LLM calls = %d, want 1 (no composing call after the ack)", got)
	}
	msgs := ve.m.convs.get("ack1").messages
	if last := msgs[len(msgs)-1]; last.Role != "assistant" || last.Content != ack || msgs[len(msgs)-2].Role != "tool" {
		t.Fatalf("history tail %+v", msgs[len(msgs)-2:])
	}

	// An error result is not an ack: the model answers from it.
	ve.m.tools.Register(ackTool{servertools.Obj("error", "task_failed", "message", "couldn't start")})
	ve.start("ack2", "[]")
	ve.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_e2", "type": "function",
		"function": map[string]any{"name": "run_errand", "arguments": `{"goal": "x"}`}}}})
	ve.eng.say("Sorry, I couldn't start that errand.")
	b = ve.turn("/api/v0/voice/command", "ack2", "run an errand", nil).want(200).json()
	if b["assistant_message"] != "Sorry, I couldn't start that errand." {
		t.Fatalf("error reply = %v", b["assistant_message"])
	}
}

func TestAcceptedAck(t *testing.T) {
	tool := func(s string) chatMsg { return chatMsg{Role: "tool", Content: s} }
	for _, c := range []struct {
		in   []chatMsg
		want string
		ok   bool
	}{
		{nil, "", false},
		{[]chatMsg{tool(`{"status": "accepted", "message": " A. "}`), tool(`{"status": "accepted", "message": "B."}`)}, "A. B.", true},
		{[]chatMsg{tool(`{"status": "accepted", "message": "A."}`), tool(`{"temp": 70}`)}, "", false},
		{[]chatMsg{tool(`{"status": "accepted", "message": ""}`)}, "", false},
		{[]chatMsg{tool(`{"status": "accepted"}`)}, "", false},
		{[]chatMsg{tool(`"accepted"`)}, "", false},
		{[]chatMsg{tool(`not json`)}, "", false},
	} {
		if got, ok := acceptedAck(c.in); got != c.want || ok != c.ok {
			t.Errorf("%v: got %q %v", c.in, got, c.ok)
		}
	}
}

func TestGatedErrandCard(t *testing.T) {
	ctx := context.Background()
	uid := int64(7)
	se := newSigEnv(t)
	se.createNode("n1", sigHH)
	se.set(settingAttention, true, sigHH)
	se.set(settingAttnQuietHours, "", sigHH)
	card := errands.Card{HouseholdID: sigHH, UserID: &uid, Title: "🤖 Ran on its own: Weather", Summary: "Done.", Body: "b",
		Category: errands.CategoryAutonomy, Metadata: map[string]any{"card_type": "autonomy_ran"}, Push: true, TargetType: "user",
		Gated: true, DedupeKey: "autorun:wf_1"}
	cards := errandCards{se.m}
	first := cards.Post(ctx, card)
	if first == "" {
		t.Fatal("first card withheld")
	}
	var n int
	if err := se.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_attention_deliveries WHERE inbox_item_id = ?`, first).Scan(&n); err != nil || n != 1 {
		t.Fatalf("deliveries = %d, %v", n, err)
	}
	if id := cards.Post(ctx, card); id != "" {
		t.Fatalf("duplicate delivered as %s", id)
	}
	if items := se.items(); len(items) != 1 || items[0].Metadata["card_type"] != "autonomy_ran" {
		t.Fatal(items)
	}
}
