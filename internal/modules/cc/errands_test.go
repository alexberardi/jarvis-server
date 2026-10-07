package cc

import (
	"context"
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
	case "report_tools":
		body = `{"client_tools": [], "available_commands": [{"command_name": "get_weather", "description": "W"}]}`
	}
	go func() {
		time.Sleep(5 * time.Millisecond)
		a.m.bus.Deliver(reply, node, json.RawMessage(body))
	}()
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
	if got := pyjson.Dumps(out, false); got != `{"success": false, "error": "could not dispatch to node: MQTT is unavailable"}` {
		t.Fatal(got)
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
