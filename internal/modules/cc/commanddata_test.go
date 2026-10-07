package cc

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/mochi-mqtt/server/v2/packets"
)

// fakeDataNode answers jarvis/nodes/{id}/command-data/{op} like the frozen node's
// command_data_handler: reply(op, payload) returns the response (a map, or raw bytes; nil = no
// answer). Every request payload is recorded.
type fakeDataNode struct {
	mu    sync.Mutex
	reply func(op string, p map[string]any) any
	got   []map[string]any
	ops   []string
}

func (e *env) dataNode(n testNode) *fakeDataNode {
	e.t.Helper()
	f := &fakeDataNode{}
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/" + n.id + "/command-data/+")
	prefix := "jarvis/nodes/" + n.id + "/command-data/"
	go func() {
		for pk := range c.in {
			if pk.FixedHeader.Type != packets.Publish || !strings.HasPrefix(pk.TopicName, prefix) {
				continue
			}
			op := strings.TrimPrefix(pk.TopicName, prefix)
			var p map[string]any
			_ = json.Unmarshal(pk.Payload, &p)
			f.mu.Lock()
			f.got = append(f.got, p)
			f.ops = append(f.ops, op)
			reply := f.reply
			f.mu.Unlock()
			var out any
			if reply != nil {
				out = reply(op, p)
			}
			if out == nil {
				continue
			}
			raw, isRaw := out.([]byte)
			if !isRaw {
				raw, _ = json.Marshal(out)
			}
			cid, _ := p["correlation_id"].(string)
			c.publish(pk.TopicName+"/response/"+cid, raw)
		}
	}()
	return f
}

func (f *fakeDataNode) set(reply func(op string, p map[string]any) any) {
	f.mu.Lock()
	f.reply = reply
	f.mu.Unlock()
}

func (f *fakeDataNode) last() (string, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.ops) == 0 {
		return "", nil
	}
	return f.ops[len(f.ops)-1], f.got[len(f.got)-1]
}

func (f *fakeDataNode) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ops)
}

var reminderFields = []any{
	map[string]any{"name": "text", "type": "string"},
	map[string]any{"name": "owner", "type": "user_ref", "editable": false},
	map[string]any{"name": "meta", "type": "object", "fields": []any{map[string]any{"name": "assignee", "type": "user_ref"}}},
}

func TestCommandDataRoutes(t *testing.T) {
	e := newEnv(t, envOpts{configure: func(m *Module) { m.Names = fakeNames{7: "alex", 8: "sam"} }})
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	f := e.dataNode(n)
	f.set(func(op string, p map[string]any) any {
		switch op {
		case "commands":
			return map[string]any{"commands": []any{map[string]any{"command_name": "reminder", "mode": "enabled", "storage_name": "set_reminder"}}}
		case "schema":
			return map[string]any{"ok": true, "mode": "enabled", "supports_create": true, "fields": reminderFields}
		case "list":
			return map[string]any{"ok": true, "truncated": false, "count": 2, "records": []any{
				map[string]any{"key": "k1", "summary": map[string]any{"title": "t", "subtitle": nil, "icon": "bell"},
					"data": map[string]any{"text": "a", "owner": 7, "meta": map[string]any{"assignee": 8}, "assignee": 8}},
				map[string]any{"key": "k2", "data": map[string]any{"text": "b", "owner": 99}},
			}}
		case "get":
			return map[string]any{"ok": true, "record": map[string]any{"text": "a", "owner": 7},
				"schema": map[string]any{"mode": "readonly", "fields": reminderFields}}
		case "create":
			return map[string]any{"ok": true, "record": map[string]any{"text": "new", "owner": 7}, "key": "k9"}
		case "update":
			return map[string]any{"ok": true, "record": map[string]any{"text": "edited", "owner": 7}}
		case "delete":
			return map[string]any{"ok": true}
		}
		return nil
	})
	const base = "/api/v0/mobile/command-data/nodes"

	// Auth scoping.
	e.do("GET", base+"/n1/commands", nil, nil).want(401)
	e.do("GET", base+"/n1/commands", nil, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("GET", base+"/ghost/commands", nil, bearer(tok)).detail(404, "Node ghost not found")

	cmds := e.do("GET", base+"/n1/commands", nil, bearer(tok)).want(200).json()
	if l := cmds["commands"].([]any); len(l) != 1 || l[0].(map[string]any)["storage_name"] != "set_reminder" {
		t.Fatal(cmds)
	}
	if op, p := f.last(); op != "commands" || len(p) != 1 || p["correlation_id"] == "" {
		t.Fatal(op, p)
	}

	// Schema: passed through untouched, then cached (no second round trip).
	s := e.do("GET", base+"/n1/commands/reminder/schema", nil, bearer(tok)).want(200).json()
	fields := s["fields"].([]any)
	if s["mode"] != "enabled" || s["supports_create"] != true || len(fields) != 3 || len(fields[0].(map[string]any)) != 2 ||
		fields[1].(map[string]any)["editable"] != false || len(s) != 3 {
		t.Fatal(s)
	}
	if op, p := f.last(); op != "schema" || p["command_name"] != "reminder" {
		t.Fatal(op, p)
	}
	before := f.count()
	e.do("GET", base+"/n1/commands/reminder/schema", nil, bearer(tok)).want(200)
	if f.count() != before {
		t.Fatal("schema not cached")
	}

	// List: requesting_user_id from the JWT; user_ref names injected into record["data"],
	// including nested object specs; unknown ids get no _display.
	l := e.do("GET", base+"/n1/commands/reminder/records", nil, bearer(tok)).want(200).json()
	recs := l["records"].([]any)
	d0 := recs[0].(map[string]any)["data"].(map[string]any)
	d1 := recs[1].(map[string]any)["data"].(map[string]any)
	if d0["owner_display"] != "alex" || d0["assignee_display"] != "sam" || d0["owner"] != 7.0 || d1["owner_display"] != nil ||
		l["truncated"] != false || l["count"] != 2.0 {
		t.Fatal(l)
	}
	if op, p := f.last(); op != "list" || p["requesting_user_id"] != 7.0 || p["command_name"] != "reminder" {
		t.Fatal(op, p)
	}

	// Get: the record itself is enriched; the node's schema is returned and cached.
	g := e.do("GET", base+"/n1/commands/reminder/records/k1", nil, bearer(tok)).want(200).json()
	if g["record"].(map[string]any)["owner_display"] != "alex" || g["schema"].(map[string]any)["mode"] != "readonly" {
		t.Fatal(g)
	}
	if op, p := f.last(); op != "get" || p["key"] != "k1" || p["requesting_user_id"] != 7.0 {
		t.Fatal(op, p)
	}
	if c := e.m.cmdData.get("n1", "reminder"); c["mode"] != "readonly" || c["supports_create"] != false {
		t.Fatal(c)
	}

	// Create: a client-supplied owner can't override the JWT; enriched since the schema is cached.
	cr := e.do("POST", base+"/n1/commands/reminder/records", map[string]any{"data": map[string]any{"text": "new"}, "requesting_user_id": 1}, bearer(tok)).want(200).json()
	if cr["key"] != "k9" || cr["record"].(map[string]any)["owner_display"] != "alex" {
		t.Fatal(cr)
	}
	if op, p := f.last(); op != "create" || p["requesting_user_id"] != 7.0 || p["data"].(map[string]any)["text"] != "new" {
		t.Fatal(op, p)
	}
	e.do("POST", base+"/n1/commands/reminder/records", map[string]any{"data": "x"}, bearer(tok)).want(400)
	e.do("POST", base+"/n1/commands/reminder/records", nil, bearer(tok)).want(400)

	// Update: not enriched.
	up := e.do("PATCH", base+"/n1/commands/reminder/records/k1", map[string]any{"patch": map[string]any{"text": "edited"}}, bearer(tok)).want(200).json()
	if rec := up["record"].(map[string]any); rec["text"] != "edited" || rec["owner_display"] != nil || len(up) != 1 {
		t.Fatal(up)
	}
	if op, p := f.last(); op != "update" || p["key"] != "k1" || p["patch"].(map[string]any)["text"] != "edited" {
		t.Fatal(op, p)
	}

	del := e.do("DELETE", base+"/n1/commands/reminder/records/k1", nil, bearer(tok)).want(200).json()
	if del["ok"] != true || len(del) != 1 {
		t.Fatal(del)
	}

	// Home picker: nodes in the caller's households only.
	e.createNode("n2", "hh2")
	if _, err := e.d.Write.Exec(`INSERT INTO cc_nodes (node_id, room) VALUES ('loose', 'attic')`); err != nil {
		t.Fatal(err)
	}
	nodes := e.do("GET", base, nil, bearer(tok)).want(200).json()["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["node_id"] != "n1" || nodes[0].(map[string]any)["room"] != "kitchen" ||
		nodes[0].(map[string]any)["household_id"] != "hh1" {
		t.Fatal(nodes)
	}
	// D40 12.Q5: a node with no household fails closed.
	e.do("GET", base+"/loose/commands", nil, bearer(tok)).detail(403, "Not authorized")
}

func TestCommandDataErrorMapping(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	f := e.dataNode(n)
	var msg any
	var mu sync.Mutex
	failWith := func(m any) {
		mu.Lock()
		msg = m
		mu.Unlock()
	}
	f.set(func(op string, p map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		switch x := msg.(type) {
		case []byte:
			return x
		case map[string]any:
			return x
		case nil:
			return map[string]any{"ok": false}
		}
		return map[string]any{"ok": false, "error": map[string]any{"message": msg}}
	})
	const rec = "/api/v0/mobile/command-data/nodes/n1/commands/reminder/records"
	create := func(m any, status int, detail string) {
		t.Helper()
		failWith(m)
		e.do("POST", rec, map[string]any{"data": map[string]any{}}, bearer(tok)).detail(status, detail)
	}
	create("command is read-only", 403, "command is read-only")
	create("command not found", 404, "command not found")
	create("command or values missing", 404, "command or values missing") // "missing" → 404 (Q11, exact)
	create("command does not support adding records", 404, "command does not support adding records")
	create("at least one dose time is required", 400, "at least one dose time is required")
	create(nil, 400, "create failed")

	update := func(m any, status int, detail string) {
		t.Helper()
		failWith(m)
		e.do("PATCH", rec+"/k1", map[string]any{"patch": map[string]any{}}, bearer(tok)).detail(status, detail)
	}
	update("record not found", 404, "record not found")
	update("patch has no editable fields", 404, "patch has no editable fields")
	update("command is read-only", 403, "command is read-only")
	update("handler error: boom", 400, "handler error: boom")
	update(nil, 400, "update failed")

	del := func(m any, status int, detail string) {
		t.Helper()
		failWith(m)
		e.do("DELETE", rec+"/k1", nil, bearer(tok)).detail(status, detail)
	}
	del("command is read-only", 403, "command is read-only")
	del("delete failed", 404, "delete failed")
	del(nil, 404, "delete failed")

	failWith(nil)
	e.do("GET", rec, nil, bearer(tok)).detail(404, "command not found")
	e.do("GET", rec+"/k1", nil, bearer(tok)).detail(404, "record not found")
	e.do("GET", "/api/v0/mobile/command-data/nodes/n1/commands/reminder/schema", nil, bearer(tok)).detail(404, "command not found")

	// Non-JSON answers are a 502; D8: ok without the required keys is a 502, not a crash.
	failWith([]byte("not json"))
	e.do("GET", rec+"/k1", nil, bearer(tok)).detail(502, "Node returned invalid response")
	failWith(map[string]any{"ok": true})
	e.do("GET", "/api/v0/mobile/command-data/nodes/n1/commands/reminder/schema", nil, bearer(tok)).detail(502, "Node returned invalid response")
	e.do("GET", rec+"/k1", nil, bearer(tok)).detail(502, "Node returned invalid response")
	// A list whose schema answer lacks keys just skips enrichment.
	if l := e.do("GET", rec, nil, bearer(tok)).want(200).json(); l["count"] != 0.0 || len(l["records"].([]any)) != 0 {
		t.Fatal(l)
	}
	// The commands route doesn't read ok.
	failWith(map[string]any{"commands": []any{}})
	e.do("GET", "/api/v0/mobile/command-data/nodes/n1/commands", nil, bearer(tok)).want(200)

	// Timeout: 504 with the legacy message.
	old := cmdDataTimeout
	cmdDataTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cmdDataTimeout = old })
	f.set(func(string, map[string]any) any { return nil })
	e.do("GET", "/api/v0/mobile/command-data/nodes/n1/commands", nil, bearer(tok)).detail(504, "Node n1 did not respond within 0.2s")
}

func TestCommandDataNoBroker(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	e.do("GET", "/api/v0/mobile/command-data/nodes/n1/commands", nil, bearer(tok)).detail(503, "MQTT client not available")
	if pySeconds(10*time.Second) != "10.0" {
		t.Fatal(pySeconds(10 * time.Second))
	}
}

func TestSchemaCacheTTL(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := newSchemaCache(func() time.Time { return now })
	c.put("n1", "a", map[string]any{"mode": "enabled"})
	now = now.Add(600 * time.Second)
	if c.get("n1", "a") == nil {
		t.Fatal("expired at exactly the TTL")
	}
	now = now.Add(time.Second)
	if c.get("n1", "a") != nil {
		t.Fatal("not expired after the TTL")
	}
}
