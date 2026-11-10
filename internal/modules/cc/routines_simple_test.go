package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

type pendingRoutineTool struct{ calls *int }

func TestSimpleRoutineConfirmationWaitsForRealAnswer(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
	ve.start("g1", confirmTools)
	ve.eng.push(nativeCall("call_f1", "browser_fill", `{"field":"Search","text":"featured article","submit":true}`))
	ve.turn("/api/v0/voice/command", "g1", "Morning shortcut", map[string]any{"routine_name": "morning", "routine_utterance": "Search for the featured article"}).want(200)
	before := len(ve.eng.requests())
	ve.eng.push(nativeCall("call_c3", "desktop_confirm", `{"confirmation_id":"confirm-2","answer":"model guess"}`))
	r := ve.continueWith("call_f1", parkedOutput())
	if r["assistant_message"] != parkedOutput()["question"] || r["stop_reason"] != "complete" || len(ve.eng.requests()) != before {
		t.Fatalf("did not stop at actual question: %v", r)
	}
	r = ve.turn("/api/v0/voice/command", "g1", "yes go ahead", map[string]any{"turn_source": "follow_up"}).want(200).json()
	if call := confirmCallSent(t, r); call == nil || call["answer"] != "yes go ahead" {
		t.Fatalf("lost real answer: %v", r)
	}
	// At this native model request every earlier assistant call has a result.
	pending := map[string]bool{}
	for _, msg := range messagesOf(ve.eng.last()) {
		if calls, ok := msg["tool_calls"].([]any); ok {
			for _, call := range calls {
				pending[call.(map[string]any)["id"].(string)] = true
			}
		}
		if id, ok := msg["tool_call_id"].(string); ok {
			delete(pending, id)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("dangling calls before follow-up: %v", pending)
	}
}

func (pendingRoutineTool) Name() string { return "test_pending" }
func (pendingRoutineTool) Definition() *pyjson.Object {
	return servertools.Obj("type", "function", "function", servertools.Obj("name", "test_pending", "description", "Request approval", "parameters", servertools.Obj("type", "object", "properties", servertools.Obj())))
}
func (s pendingRoutineTool) Execute(context.Context, servertools.Call, servertools.Turn) (any, error) {
	*s.calls++
	return servertools.Obj("success", true, "status", "awaiting_confirmation", "message", "Confirm the action?"), nil
}

func TestSimpleRoutineStopsServerBatchAtPendingAction(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	calls := 0
	ce.m.tools.Register(pendingRoutineTool{&calls})
	if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET capabilities='{"simple_routines":true}' WHERE node_id=?`, ce.node.id); err != nil {
		t.Fatal(err)
	}
	batch := []map[string]any{}
	for _, id := range []string{"first", "second"} {
		batch = append(batch, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "test_pending", "arguments": "{}"}})
	}
	ce.eng.push(engineReply{toolCalls: batch})
	ce.eng.say("Done. <exchange_complete/>")
	got := ce.m.runSimpleRoutine(context.Background(), &routineRow{id: "pending", householdID: voiceHH, utterance: "Perform both actions"}, ce.node.id)
	if calls != 1 || got.Status != "failed" || got.Passed != 0 {
		t.Fatalf("calls=%d result=%+v", calls, got)
	}
}

func TestSimpleRoutineUnattendedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, status                string
		toolFirst, timeout, capable bool
	}{
		{"clarification", "failed", false, false, true},
		{"partial before clarification", "partial", true, false, true},
		{"timeout never retries", "timeout", true, true, true},
		{"old node", "failed", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ce := newChatEnv(t, prompts.Qwen3_5_9B)
			old := routineWait
			routineWait = time.Second
			if tc.timeout {
				routineWait = 100 * time.Millisecond
			}
			t.Cleanup(func() { routineWait = old })
			caps, _ := json.Marshal(map[string]bool{"simple_routines": tc.capable})
			if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET capabilities=? WHERE node_id=?`, string(caps), ce.node.id); err != nil {
				t.Fatal(err)
			}
			ce.pub.toolReply = `{"output":{"success":true}}`
			if tc.timeout {
				ce.pub.toolReply = ""
			}
			if tc.toolFirst {
				ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_weather", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Boston"}`}}}})
			}
			ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_question", "type": "function", "function": map[string]any{"name": "request_validation", "arguments": `{"question":"Which room?","parameter_name":"room"}`}}}})
			got := ce.m.runSimpleRoutine(context.Background(), &routineRow{id: "simple", mode: "simple", householdID: voiceHH, utterance: "Tell me the weather and turn on the lights"}, ce.node.id)
			if got.Status != tc.status {
				t.Fatalf("result=%+v", got)
			}
			if tc.toolFirst && len(ce.pub.commands("tool_call")) != 1 {
				t.Fatal("tool call lost or retried")
			}
			if !tc.capable && len(ce.pub.commands("report_tools")) != 0 {
				t.Fatal("queried unsupported node")
			}
			if !tc.timeout && tc.capable && !strings.Contains(got.Message.(string), "Which room?") {
				t.Fatalf("lost clarification: %+v", got)
			}
		})
	}
}

func TestSimpleRoutineContinuesAfterIslandTool(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET capabilities='{"simple_routines":true}' WHERE node_id=?`, ce.node.id); err != nil {
		t.Fatal(err)
	}
	ce.pub.toolReply = `{"output":{"success":true,"island_only":true,"message":"Shown on the node"}}`
	for i, city := range []string{"Boston", "Denver"} {
		args, _ := json.Marshal(map[string]string{"city": city})
		ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": fmt.Sprintf("call_%d", i), "type": "function", "function": map[string]any{"name": "get_weather", "arguments": string(args)}}}})
	}
	ce.eng.say("Both forecasts are ready. <exchange_complete/>")
	got := ce.m.runSimpleRoutine(context.Background(), &routineRow{id: "compound", householdID: voiceHH, utterance: "Get Boston's weather and then Denver's"}, ce.node.id)
	if got.Status != "success" || got.Passed != 2 || len(ce.pub.commands("tool_call")) != 2 {
		t.Fatalf("stopped early: %+v", got)
	}
}

func TestSimpleRoutinePendingToolCannotClaimSuccess(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			ce := newChatEnv(t, prompts.Qwen3_5_9B)
			if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET capabilities='{"simple_routines":true}' WHERE node_id=?`, ce.node.id); err != nil {
				t.Fatal(err)
			}
			if explicit {
				ce.pub.toolReply = `{"output":{"success":true,"requires_follow_up":true,"message":"Which room?"}}`
			} else {
				ce.pub.toolReply = `{"output":{"success":true,"wait_for_input":true,"message":"Which room?"}}`
			}
			ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "question", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Boston"}`}}}})
			ce.eng.say("Which room would you like?")
			got := ce.m.runSimpleRoutine(context.Background(), &routineRow{id: "question", householdID: voiceHH, utterance: "Get the forecast"}, ce.node.id)
			if got.Status == "success" || !strings.Contains(fmt.Sprint(got.Message), "follow-up") {
				t.Fatalf("question declared complete: %+v", got)
			}
			if explicit && got.Passed != 0 {
				t.Fatalf("counted parked action: %+v", got)
			}
		})
	}
}

func TestSimpleRoutineRejectsUnsupportedScheduledTarget(t *testing.T) {
	e := newRoutineEnv(t)
	e.createNode("old", rhh)
	schedule := map[string]any{"type": "cron", "cron": "0 7 * * *", "target_node_id": "old"}
	e.do("POST", e.path(""), routineBody("Unsupported", map[string]any{"mode": "simple", "utterance": "Play music", "schedule": schedule}), bearer(e.owner)).want(400)
	r := e.create("Existing schedule", map[string]any{"schedule": schedule})
	e.do("PATCH", e.path("/"+r["id"].(string)), map[string]any{"mode": "simple", "utterance": "Play music"}, bearer(e.owner)).want(400)
}

func TestSimpleRoutineScheduleUsesInstructionAndPostsOneResult(t *testing.T) {
	ce := newChatEnv(t, prompts.Qwen3_5_9B)
	token := ce.auth.addUser(7, voiceHH, authn.RoleOwner)
	if _, err := ce.d.Write.Exec(`UPDATE cc_nodes SET capabilities='{"simple_routines":true}' WHERE node_id=?`, ce.node.id); err != nil {
		t.Fatal(err)
	}
	r := ce.do("POST", "/api/v0/households/"+voiceHH+"/routines", routineBody("Scheduled greeting", map[string]any{
		"mode": "simple", "utterance": "Say good morning", "steps": []any{},
		"schedule": map[string]any{"type": "cron", "cron": "0 7 * * *", "target_node_id": ce.node.id, "timezone": "UTC"},
	}), bearer(token)).want(201).json()
	id := r["id"].(string)
	ce.eng.say("Good morning. <exchange_complete/>")
	inner, _ := json.Marshal(map[string]string{"routine_id": id})
	payload, _ := json.Marshal(scheduler.Fire{Trigger: routineTrigger(id), ScheduledAt: ce.clock(), Payload: inner})
	raw, err := ce.m.runRoutineFire(context.Background(), queue.Job{Type: routineFireJob, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["status"] != "success" || len(ce.notify.items) != 1 || ce.notify.items[0].Metadata["status"] != "success" || ce.notify.items[0].UserID != nil {
		t.Fatalf("scheduled result=%s cards=%v", raw, ce.notify.items)
	}
}

func TestSimpleRoutineRunUsesNormalToolsWithoutAUser(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "success", false: "tool failure"}[success], func(t *testing.T) {
			ce := newChatEnv(t, prompts.Qwen3_5_9B)
			old := routineWait
			routineWait = 2 * time.Second
			t.Cleanup(func() { routineWait = old })
			token := ce.auth.addUser(7, voiceHH, authn.RoleOwner)
			_, err := ce.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = 7, capabilities = '{"simple_routines":true}' WHERE node_id = ?`, ce.node.id)
			if err != nil {
				t.Fatal(err)
			}
			base := "/api/v0/households/" + voiceHH + "/routines"
			r := ce.do("POST", base, routineBody("Weather shortcut", map[string]any{"mode": "simple", "utterance": "Tell me the weather tomorrow", "steps": []any{}}), bearer(token)).want(201).json()
			output := map[string]any{"success": success, "temperature": 72}
			if !success {
				output["error"] = "weather unavailable"
			}
			reply, _ := json.Marshal(map[string]any{"output": output})
			ce.pub.toolReply = string(reply)
			ce.eng.push(engineReply{toolCalls: []map[string]any{{"id": "call_weather", "type": "function", "function": map[string]any{"name": "get_weather", "arguments": `{"city":"Boston"}`}}}})
			ce.eng.say("Finished. <exchange_complete/>")
			got := ce.do("POST", base+"/"+r["id"].(string)+"/run-now", map[string]any{"node_id": ce.node.id}, bearer(token)).want(200).json()
			want := "failed"
			if success {
				want = "success"
			}
			if got["status"] != want {
				t.Fatalf("result=%v, want %s", got, want)
			}
			calls := ce.pub.commands("tool_call")
			if len(calls) != 1 || calls[0]["user_id"] != nil || calls[0]["voice_command"] != "Tell me the weather tomorrow" || calls[0]["wait_for_completion"] != true {
				t.Fatalf("wrong execution context: %v", calls)
			}
			if len(ce.pub.commands("routine")) != 0 {
				t.Fatal("Simple was dispatched as Advanced steps")
			}
		})
	}
}

func TestSimpleRoutineVoiceExpansionUsesNormalModelTurn(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ve.start("simple-voice", weatherTool)
	ve.eng.say("The weather is clear. <exchange_complete/>")
	ve.turn("/api/v0/voice/command", "simple-voice", "Morning shortcut", map[string]any{
		"routine_name": "morning_shortcut", "routine_utterance": "Tell me the weather tomorrow",
	}).want(200)
	var user string
	for _, m := range messagesOf(ve.eng.last()) {
		if m["role"] == "user" {
			user, _ = m["content"].(string)
		}
	}
	if !strings.Contains(user, "Tell me the weather tomorrow") || strings.Contains(user, "Morning shortcut") {
		t.Fatalf("model saw the shortcut instead of its instruction: %q", user)
	}
}

func TestSimpleRoutineContentAndModePersistence(t *testing.T) {
	e := newRoutineEnv(t)
	r := e.create("Shortcut", map[string]any{"mode": "simple", "utterance": "Turn on the lights and play a song."})
	if r["mode"] != "simple" || r["utterance"] != "Turn on the lights and play a song." {
		t.Fatalf("simple content missing: %v", r)
	}
	p := e.path("/" + r["id"].(string))
	r = e.do("PATCH", p, map[string]any{"name": "Morning"}, bearer(e.owner)).want(200).json()
	if r["mode"] != "simple" || r["utterance"] == "" || len(r["steps"].([]any)) != 1 {
		t.Fatalf("patch discarded content: %v", r)
	}
	// A legacy editor cannot unknowingly overwrite a Simple routine's content.
	e.do("PATCH", p, routineBody("Legacy editor", nil), bearer(e.owner)).want(409)
	for _, mode := range []string{"advanced", "simple"} {
		r = e.do("PATCH", p, map[string]any{"mode": mode}, bearer(e.owner)).want(200).json()
		if r["mode"] != mode || r["utterance"] == "" || len(r["steps"].([]any)) != 1 {
			t.Fatalf("switch discarded content: %v", r)
		}
	}
	legacy := e.create("Old client", nil)
	if legacy["mode"] != "advanced" || legacy["utterance"] != "" {
		t.Fatalf("legacy default: %v", legacy)
	}
}

func TestSimpleRoutineValidation(t *testing.T) {
	e := newRoutineEnv(t)
	for _, over := range []map[string]any{
		{"mode": "unknown"},
		{"mode": "simple"},
		{"mode": "simple", "utterance": " \n "},
		{"mode": "simple", "utterance": 42},
		{"mode": "simple", "utterance": strings.Repeat("界", 5001)},
	} {
		e.do("POST", e.path(""), routineBody("Invalid", over), bearer(e.owner)).want(400)
	}
	r := e.create("No steps needed", map[string]any{"mode": "simple", "utterance": "Tell me the time", "steps": []any{}})
	p := e.path("/" + r["id"].(string))
	e.do("PATCH", p, map[string]any{"mode": "simple", "utterance": ""}, bearer(e.owner)).want(400)
	e.do("PATCH", p, map[string]any{"mode": "advanced"}, bearer(e.owner)).want(400)
}

func TestSimpleRoutineSupportAndNodeProjection(t *testing.T) {
	e := newRoutineEnv(t)
	n := e.createNode("n1", rhh)
	list := e.do("GET", e.path(""), nil, bearer(e.owner)).want(200).json()
	if modes, ok := list["supported_modes"].([]any); !ok || len(modes) != 2 {
		t.Fatalf("server did not advertise support: %v", list["supported_modes"])
	}
	var id string
	for _, raw := range list["routines"].([]any) {
		r := raw.(map[string]any)
		if r["slug"] == "good_morning" {
			id = r["id"].(string)
		}
	}
	e.do("PATCH", e.path("/"+id), map[string]any{"mode": "simple", "utterance": "Play music"}, bearer(e.owner)).want(200)
	pull := func(query string) map[string]any {
		return e.do("GET", "/api/v0/nodes/n1/routines"+query, nil, n.h()).want(200).json()["routines"].(map[string]any)["good_morning"].(map[string]any)
	}
	current := pull("?simple_routines=1")
	if current["mode"] != "simple" || current["utterance"] != "Play music" || len(current["steps"].([]any)) != 0 {
		t.Fatalf("active projection: %v", current)
	}
	// Keep a nonempty inert definition, including for protected built-in slugs:
	// old agents fall back to stale/default steps on an empty step list.
	assertInert := func(got map[string]any) {
		t.Helper()
		if len(got["trigger_phrases"].([]any)) != 0 || len(got["steps"].([]any)) != 1 || got["utterance"] != nil {
			t.Fatalf("unsafe legacy projection: %v", got)
		}
	}
	assertInert(pull(""))
	e.do("PATCH", e.path("/"+id), map[string]any{"enabled": false}, bearer(e.owner)).want(200)
	assertInert(pull(""))
	assertInert(pull("?simple_routines=1"))
}
