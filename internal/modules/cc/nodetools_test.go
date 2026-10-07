package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestNodeTools(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	const path = "/api/v0/mobile/nodes/n1/tools"

	e.do("GET", path, nil, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("GET", "/api/v0/mobile/nodes/ghost/tools", nil, bearer(tok)).detail(404, "Node not found")

	done := make(chan *resp, 1)
	go func() { done <- e.do("GET", path, nil, bearer(tok)) }()
	verb, details := command(t, c.next())
	rid, _ := details["request_id"].(string)
	if verb != "report_tools" || details["reply_request_id"] != rid || details["trusted"] != nil || len(details) != 2 {
		t.Fatal(verb, details)
	}
	// The node verifies the command (D48) and posts its report; D4: only this node may.
	if v := e.do("POST", "/api/v0/commands/"+rid+"/verify", nil, n.h()).want(200).json(); v["valid"] != true {
		t.Fatal(v)
	}
	report := map[string]any{
		"client_tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}}},
		"available_commands": []any{"get_weather"},
		"installed_packages": []any{map[string]any{"name": "weather", "version": "1.2.0", "previous_version": "1.1.0", "health": "ok"}},
	}
	e.do("POST", "/api/v0/mobile/node-tool-reports/"+rid, report, other.h()).detail(403, "Request does not belong to this node")
	e.do("POST", "/api/v0/mobile/node-tool-reports/"+rid, report, nil).want(400)
	e.do("POST", "/api/v0/mobile/node-tool-reports/"+rid, report, n.h()).want(200)
	var r *resp
	select {
	case r = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("tools request didn't return")
	}
	out := r.want(200).json()
	pkgs := out["installed_packages"].([]any)
	if len(out) != 3 || len(out["client_tools"].([]any)) != 1 || out["available_commands"].([]any)[0] != "get_weather" ||
		pkgs[0].(map[string]any)["previous_version"] != "1.1.0" {
		t.Fatal(out)
	}

	// A partial report degrades missing lists to [] (D8).
	go func() { done <- e.do("GET", path, nil, bearer(tok)) }()
	_, details = command(t, c.next())
	e.do("POST", "/api/v0/mobile/node-tool-reports/"+details["request_id"].(string), map[string]any{"client_tools": []any{}}, n.h()).want(200)
	if out := (<-done).want(200).json(); len(out["available_commands"].([]any)) != 0 || len(out["installed_packages"].([]any)) != 0 {
		t.Fatal(out)
	}

	// No answer: 200 with three empty lists (D40 12.Q9); a late report is acknowledged and dropped.
	old := nodeToolsWait
	nodeToolsWait = 200 * time.Millisecond
	t.Cleanup(func() { nodeToolsWait = old })
	out = e.do("GET", path, nil, bearer(tok)).want(200).json()
	if len(out) != 3 || len(out["client_tools"].([]any)) != 0 || len(out["available_commands"].([]any)) != 0 || len(out["installed_packages"].([]any)) != 0 {
		t.Fatal(out)
	}
	_, details = command(t, c.next())
	e.do("POST", "/api/v0/mobile/node-tool-reports/"+details["request_id"].(string), report, n.h()).want(200)
}

func TestNodeToolsNoHouseholdOrBroker(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	if _, err := e.d.Write.Exec(`INSERT INTO cc_nodes (node_id, room) VALUES ('loose', 'attic')`); err != nil {
		t.Fatal(err)
	}
	e.do("GET", "/api/v0/mobile/nodes/loose/tools", nil, bearer(tok)).detail(403, "Not authorized")
	start := time.Now()
	out := e.do("GET", "/api/v0/mobile/nodes/n1/tools", nil, bearer(tok)).want(200).json()
	if len(out["client_tools"].([]any)) != 0 || time.Since(start) > 2*time.Second {
		t.Fatal(out)
	}
}
