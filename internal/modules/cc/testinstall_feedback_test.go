package cc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// completedTI inserts a finished test install of days_until_date on n1, as the agent reports it.
func completedTI(t *testing.T, e *env, id string, at time.Time) {
	t.Helper()
	if _, err := e.d.Write.Exec(`INSERT INTO cc_test_install_requests (id, node_id, household_id, share_code,
		package_name, status, results_json, created_at, expires_at, completed_at) VALUES (?, 'n1', 'hh1', 'ABC123',
		'countdown', 'completed', '{"package_name":"countdown","commands":["days_until_date"]}', ?, ?, ?)`,
		id, dbTime(at), dbTime(at), dbTime(at)); err != nil {
		t.Fatal(err)
	}
}

func testRunConv(calls ...parse.ToolCall) *conversation {
	return &conversation{nodeID: "n1", messages: []chatMsg{{Role: "user", Content: "how long till christmas"},
		{Role: "assistant", ToolCalls: calls}}}
}

func TestSendToForge(t *testing.T) {
	e := newEnv(t)
	pantry := newFakePantry(t)
	pantry.set(func() { pantry.drafts["ABC123"] = `{"package_name":"countdown"}` })
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleOwner)
	e.createNode("n1", "hh1")
	completedTI(t, e, "ti1", e.clock())
	ctx := context.Background()

	// Only the draft's command is recorded; another tool in the same turn is not.
	conv := testRunConv(
		parse.ToolCall{ID: "c1", Function: parse.FunctionCall{Name: "days_until_date", Arguments: `{"date":"2026-12-25"}`}},
		parse.ToolCall{ID: "c2", Function: parse.FunctionCall{Name: "get_weather", Arguments: `{}`}})
	e.m.recordTestRuns(ctx, conv, []toolResult{
		{ToolCallID: "c1", Output: map[string]any{"success": false, "error": "KeyError: 'date'"}},
		{ToolCallID: "c2", Output: "sunny, 21C at home"}})

	const base = "/api/v0/nodes/n1/test-install/ti1"
	poll := e.do("GET", base, nil, bearer(member)).want(200).json()
	runs, _ := poll["runs"].([]any)
	if len(runs) != 1 {
		t.Fatal(poll)
	}
	run := runs[0].(map[string]any)
	if run["command"] != "days_until_date" || run["arguments"].(map[string]any)["date"] != "2026-12-25" ||
		run["output"].(map[string]any)["error"] != "KeyError: 'date'" {
		t.Fatal(run)
	}

	e.do("POST", base+"/feedback", map[string]any{}, nil).detail(401, "Authentication required")
	e.do("POST", base+"/feedback", map[string]any{}, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("POST", "/api/v0/nodes/n1/test-install/nope/feedback", map[string]any{}, bearer(member)).detail(404, "Test install request not found")
	r := e.do("POST", base+"/feedback", map[string]any{"note": "crashes on christmas"}, bearer(member)).want(200).json()
	if r["status"] != "sent" || r["runs"] != float64(1) {
		t.Fatal(r)
	}
	var sent map[string]any
	pantry.set(func() { _ = json.Unmarshal([]byte(pantry.feedback["ABC123"]), &sent) })
	install, _ := sent["install"].(map[string]any)
	if install["status"] != "completed" || install["package_name"] != "countdown" || sent["note"] != "crashes on christmas" ||
		len(sent["runs"].([]any)) != 1 {
		t.Fatal(sent)
	}
	if _, err := json.Marshal(sent); err != nil || len(pantry.feedback["ABC123"]) == 0 {
		t.Fatal(err)
	}

	// The draft cleaned up on Pantry: say so.
	pantry.set(func() { delete(pantry.drafts, "ABC123") })
	e.do("POST", base+"/feedback", map[string]any{}, bearer(member)).detail(404, "The Forge draft is gone")
}

func TestTestRunsWindowAndCap(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	e.createNode("n1", "hh1")
	completedTI(t, e, "old", e.clock().Add(-testRunWindow-time.Minute))
	ctx := context.Background()
	call := parse.ToolCall{ID: "c1", Function: parse.FunctionCall{Name: "days_until_date", Arguments: `{}`}}
	res := []toolResult{{ToolCallID: "c1", Output: "47 days"}}

	// Past the node's test window the command is gone: nothing is recorded.
	e.m.recordTestRuns(ctx, testRunConv(call), res)
	if runs, _ := e.m.testRuns(ctx, "old"); len(runs) != 0 {
		t.Fatal(runs)
	}

	completedTI(t, e, "new", e.clock())
	for range maxTestRuns + 5 {
		e.m.recordTestRuns(ctx, testRunConv(call), res)
	}
	if runs, _ := e.m.testRuns(ctx, "new"); len(runs) != maxTestRuns || runs[0].(map[string]any)["output"] != "47 days" {
		t.Fatal(len(runs), runs)
	}
}

// What a test install did on the node besides chat tool calls (log lines, scans, device
// controls): only its node sends them, only during the test window; the poll shows them and
// Send to Forge sends the newest ones.
func TestTestInstallEvents(t *testing.T) {
	e := newEnv(t)
	pantry := newFakePantry(t)
	pantry.set(func() { pantry.drafts["ABC123"] = `{"package_name":"countdown"}` })
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	completedTI(t, e, "ti1", e.clock())
	const base = "/api/v0/nodes/n1/test-install/ti1"

	events := []any{
		map[string]any{"seq": 1, "kind": "log", "level": "WARNING", "message": "Zigbee2MQTT broker host is not configured"},
		map[string]any{"seq": 2, "kind": "scan", "protocol": "zigbee", "count": 0},
	}
	e.do("POST", base+"/events", map[string]any{"events": events}, other.h()).detail(403, "Node mismatch")
	e.do("POST", base+"/events", map[string]any{}, n.h()).want(400)
	if r := e.do("POST", base+"/events", map[string]any{"events": events}, n.h()).want(200).json(); r["kept"] != float64(2) {
		t.Fatal(r)
	}
	poll := e.do("GET", base, nil, bearer(member)).want(200).json()
	got, _ := poll["events"].([]any)
	if len(got) != 2 || got[0].(map[string]any)["message"] != "Zigbee2MQTT broker host is not configured" {
		t.Fatal(poll["events"])
	}

	// An oversized event is kept without its text.
	big := map[string]any{"seq": 3, "kind": "log", "message": string(make([]byte, maxTestEvent+1))}
	e.do("POST", base+"/events", map[string]any{"events": []any{big}}, n.h()).want(200)
	got, _ = e.do("GET", base, nil, bearer(member)).want(200).json()["events"].([]any)
	if got[2].(map[string]any)["message"] != "[too long: cut]" {
		t.Fatal(got[2])
	}

	e.do("POST", base+"/feedback", map[string]any{}, bearer(member)).want(200)
	var sent map[string]any
	pantry.set(func() { _ = json.Unmarshal([]byte(pantry.feedback["ABC123"]), &sent) })
	if ev, _ := sent["events"].([]any); len(ev) != 3 {
		t.Fatal(sent["events"])
	}

	// After the window, nothing more.
	e.advance(testRunWindow + 2*time.Minute)
	e.do("POST", base+"/events", map[string]any{"events": events}, n.h()).detail(409, "The test window is over")
}
