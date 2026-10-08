package cc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func settingsScope(hh string) settings.Scope { return settings.Scope{HouseholdID: hh} }

// Per-node broker credentials and ACLs (D4).
func TestBrokerAuthAndACL(t *testing.T) {
	e := newEnv(t)
	a := e.createNode("a", "hh1")
	b := e.createNode("b", "hh1")

	if _, code := e.dialMQTT("jarvis-node-a", "", ""); code == 0 {
		t.Fatal("anonymous client admitted")
	}
	if _, code := e.dialMQTT("jarvis-node-a", "a", "wrong"); code == 0 {
		t.Fatal("bad password admitted")
	}
	if _, code := e.dialMQTT("jarvis-node-b", "a", a.key); code == 0 {
		t.Fatal("node a admitted under node b's client id")
	}
	ca := e.dialNode(a)
	if code := ca.subscribe("jarvis/nodes/a/#"); code != 1 {
		t.Fatalf("own subtree suback %d", code)
	}
	if code := ca.subscribe("jarvis/auth/+/ready"); code != 1 {
		t.Fatalf("auth ready suback %d", code)
	}
	if code := ca.subscribe("jarvis/nodes/b/#"); code < 0x80 {
		t.Fatalf("node a subscribed to node b: %d", code)
	}
	// Node a can't publish a command to node b, or forge one to itself.
	cb := e.dialNode(b)
	cb.subscribe("jarvis/nodes/b/#")
	ca.publish("jarvis/nodes/b/commands", []byte(`[{"command":"tool_call","details":{}}]`))
	ca.publish("jarvis/nodes/a/commands", []byte(`[]`))
	cb.nothing(300 * time.Millisecond)

	// A deactivated node can no longer connect.
	e.auth.DeactivateNode(context.Background(), "b")
	if _, code := e.dialMQTT("jarvis-node-b", "b", b.key); code == 0 {
		t.Fatal("deactivated node admitted")
	}
}

func TestSettingsRequestRoundTrip(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RolePowerUser)
	stranger := e.auth.addUser(2, "hh2", authn.RoleAdmin)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	path := "/api/v0/nodes/n1/settings/requests"

	req := e.do("POST", path+"?include_values=true", nil, bearer(owner)).want(201).json()
	rid := req["request_id"].(string)
	pk := c.next()
	p := payload(t, pk)
	if pk.TopicName != "jarvis/nodes/n1/settings/request" || p["request_id"] != rid || p["include_values"] != true || p["user_id"] != 1.0 {
		t.Fatalf("%s %v", pk.TopicName, p)
	}
	e.do("POST", path, nil, bearer(stranger)).want(403)
	e.do("POST", "/api/v0/nodes/zz/settings/requests", nil, bearer(owner)).detail(404, "Node not found")

	// The reconnect backstop list carries include_values and user_id (D40 Q8).
	l := e.do("GET", path, nil, n.h()).want(200).list()
	if len(l) != 1 || l[0].(map[string]any)["include_values"] != true || l[0].(map[string]any)["user_id"] != 1.0 {
		t.Fatalf("list %v", l)
	}
	e.do("GET", "/api/v0/nodes/other/settings/requests", nil, n.h()).detail(403, "Cannot access other node's requests")
	e.do("GET", path+"/"+rid, nil, n.h()).want(200)

	r := e.do("GET", path+"/"+rid+"/result", nil, bearer(owner)).want(202).json()
	if r["message"] != "Waiting for node response" {
		t.Fatal(r)
	}
	// D4: the poll checks the household.
	e.do("GET", path+"/"+rid+"/result", nil, bearer(stranger)).want(403)

	snap := map[string]any{"ciphertext": "c", "nonce": "n", "tag": "t", "aad_schema_version": 1, "aad_commands_schema_version": 2, "aad_revision": 3}
	e.do("PUT", path+"/"+rid+"/snapshot", map[string]any{"ciphertext": "c"}, n.h()).want(400)
	e.do("PUT", path+"/"+rid+"/snapshot", snap, n.h()).want(201)
	e.do("PUT", path+"/"+rid+"/snapshot", snap, n.h()).detail(409, "Request already fulfilled")
	res := e.do("GET", path+"/"+rid+"/result", nil, bearer(owner)).want(200).json()
	aad := res["snapshot"].(map[string]any)["aad"].(map[string]any)
	if res["status"] != "fulfilled" || aad["revision"] != 3.0 || aad["request_id"] != rid || aad["node_id"] != "n1" {
		t.Fatal(res)
	}

	// Expiry: 410 for the node and for a pending poll.
	rid2 := e.do("POST", path, nil, bearer(owner)).want(201).json()["request_id"].(string)
	c.next()
	e.advance(6 * time.Minute)
	e.do("GET", path+"/"+rid2, nil, n.h()).detail(410, "Request expired")
	e.do("GET", path+"/"+rid2+"/result", nil, bearer(owner)).detail(410, "Request expired")
	if l := e.do("GET", path, nil, n.h()).list(); len(l) != 0 {
		t.Fatalf("expired listed: %v", l)
	}
	// 24 h prune.
	e.advance(25 * time.Hour)
	e.m.cleanup(context.Background())
	e.do("GET", path+"/"+rid+"/result", nil, bearer(owner)).detail(404, "Request not found")
}

func TestK2Relay(t *testing.T) {
	k2Wait = 500 * time.Millisecond
	defer func() { k2Wait = 15 * time.Second }()
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	body := map[string]any{"k2": "secret", "kid": "kid1", "created_at": "2026-10-06T00:00:00Z"}

	type result struct{ r *resp }
	run := func() chan result {
		ch := make(chan result, 1)
		go func() { ch <- result{e.do("POST", "/api/v0/nodes/n1/k2", body, bearer(owner))} }()
		return ch
	}

	ch := run()
	p := payload(t, c.next())
	if len(p) != 1 {
		t.Fatalf("nudge carries more than request_id: %v", p)
	}
	rid := p["request_id"].(string)
	pull := "/api/v0/nodes/n1/k2/provision/" + rid
	e.do("GET", "/api/v0/nodes/n2/k2/provision/"+rid, nil, n.h()).detail(403, "Node mismatch")
	if k := e.do("GET", pull, nil, n.h()).want(200).json(); k["k2"] != "secret" || k["kid"] != "kid1" {
		t.Fatal(k)
	}
	e.do("GET", pull, nil, n.h()).detail(404, "No pending K2 for this request")
	// D8: the ack's path node must be the caller; another node can't answer for n1.
	e.do("POST", "/api/v0/nodes/n1/k2/ack/"+rid, map[string]any{"success": false}, other.h()).detail(403, "Node mismatch")
	e.do("POST", "/api/v0/nodes/n1/k2/ack/"+rid, map[string]any{"success": true}, n.h()).want(200)
	if r := (<-ch).r.want(200).json(); r["ok"] != true || r["kid"] != "kid1" || r["node_id"] != "n1" {
		t.Fatal(r)
	}

	ch = run()
	rid = payload(t, c.next())["request_id"].(string)
	e.do("POST", "/api/v0/nodes/n1/k2/ack/"+rid, map[string]any{"success": false, "error": "disk full"}, n.h()).want(200)
	(<-ch).r.detail(502, "disk full")

	ch = run()
	rid = payload(t, c.next())["request_id"].(string)
	(<-ch).r.detail(504, "Node did not acknowledge K2 — it may not be online yet. Try again in a few seconds.")
	// The key never outlives the call.
	e.do("GET", "/api/v0/nodes/n1/k2/provision/"+rid, nil, n.h()).want(404)
}

func TestActionVerifyAndResultSink(t *testing.T) {
	actionWait = 2 * time.Second
	defer func() { actionWait = 10 * time.Second }()
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	ch := make(chan *resp, 1)
	go func() {
		ch <- e.do("POST", "/api/v0/nodes/n1/actions", map[string]any{"command_name": "cmd", "action_name": "send", "context": map[string]any{"k": "v"}}, bearer(owner))
	}()
	pk := c.next()
	verb, d := command(t, pk)
	if pk.TopicName != "jarvis/nodes/n1/commands" || verb != "action" || d["command_name"] != "cmd" || d["user_id"] != 1.0 {
		t.Fatalf("%s %s %v", pk.TopicName, verb, d)
	}
	if _, ok := d["trusted"]; ok {
		t.Fatal("trusted must never be published (D4/D7)")
	}
	rid := d["request_id"].(string)

	// The node verifies the action (D48): mismatch false and not consumed; owner once.
	verify := "/api/v0/commands/" + rid + "/verify"
	if e.do("POST", verify, map[string]any{}, other.h()).json()["valid"] != false {
		t.Fatal("other node verified")
	}
	if e.do("POST", verify, nil, n.h()).json()["valid"] != true {
		t.Fatal("owner not verified")
	}
	if e.do("POST", verify, map[string]any{}, n.h()).json()["valid"] != false {
		t.Fatal("replay verified")
	}

	// D4: node auth on the sink, and the rid must be this node's.
	sink := "/api/v0/device-control-results/" + rid
	e.do("POST", sink, map[string]any{"success": false}, nil).want(400)
	e.do("POST", sink, map[string]any{"success": false}, other.h()).detail(403, "Request does not belong to this node")
	e.do("POST", sink, map[string]any{"success": true, "input_required": map[string]any{"field": "x"}}, n.h()).want(200)
	r := (<-ch).want(200).json()
	if r["status"] != "completed" || r["request_id"] != rid || r["success"] != true || r["error"] != nil || r["input_required"] == nil {
		t.Fatal(r)
	}
	// Late result: acknowledged and dropped.
	e.do("POST", sink, map[string]any{"success": true}, n.h()).want(200)

	// Timeout.
	go func() {
		ch <- e.do("POST", "/api/v0/nodes/n1/actions", map[string]any{"command_name": "cmd", "action_name": "x"}, bearer(owner))
	}()
	c.next()
	if r := (<-ch).want(200).json(); r["status"] != "timeout" || r["error"] != "Node did not respond in time" {
		t.Fatal(r)
	}
	stranger := e.auth.addUser(9, "hh9", authn.RoleAdmin)
	e.do("POST", "/api/v0/nodes/n1/actions", map[string]any{"command_name": "cmd", "action_name": "x"}, bearer(stranger)).want(403)
}

func TestFireAndForgetCommands(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	r := e.do("POST", "/api/v0/nodes/n1/node-config", map[string]any{"settings": map[string]any{"a": 1}, "restart": false}, bearer(owner)).want(200).json()
	verb, d := command(t, c.next())
	if verb != "update_node_config" || d["restart"] != false || d["request_id"] != r["request_id"] || d["settings"].(map[string]any)["a"] != 1.0 {
		t.Fatal(verb, d)
	}
	e.do("POST", "/api/v0/nodes/n1/node-config", map[string]any{"settings": map[string]any{"a": []int{1}}}, bearer(owner)).want(400)

	e.do("POST", "/api/v0/nodes/n1/led/preview", map[string]any{"pattern": "rainbow"}, bearer(owner)).want(200)
	verb, d = command(t, c.next())
	if verb != "preview_led_pattern" || d["duration_seconds"] != 3.0 || d["pattern"] != "rainbow" {
		t.Fatal(verb, d)
	}
}

func TestAmbientNoise(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	path := "/api/v0/nodes/n1/ambient-noise-measurements"

	rid := e.do("POST", path, map[string]any{"duration_seconds": 30}, bearer(owner)).want(200).json()["request_id"].(string)
	verb, d := command(t, c.next())
	if verb != "measure_ambient_noise" || d["duration_seconds"] != 10.0 || d["request_id"] != rid {
		t.Fatal(verb, d)
	}
	e.do("POST", path, nil, bearer(stranger)).want(403) // D4
	if p := e.do("GET", path+"/"+rid, nil, bearer(owner)).want(200).json(); p["status"] != "pending" || p["result"] != nil {
		t.Fatal(p)
	}
	e.do("GET", path+"/"+rid, nil, bearer(stranger)).want(403)
	res := map[string]any{"success": true, "duration_seconds": 10, "chunks": 5, "p95_rms": 3.5, "suggested_silence_threshold": 7}
	e.do("POST", "/api/v0/nodes/other/ambient-noise-measurements/"+rid+"/result", res, n.h()).detail(403, "Node may only post results for itself")
	e.do("POST", path+"/unknown/result", res, n.h()).detail(404, "Not found")
	e.do("POST", path+"/"+rid+"/result", res, n.h()).want(200)
	p := e.do("GET", path+"/"+rid, nil, bearer(owner)).want(200).json()
	result := p["result"].(map[string]any)
	if p["status"] != "completed" || result["chunks"] != 5.0 || result["p50_rms"] != nil || result["error"] != nil || len(result) != 9 {
		t.Fatal(p)
	}
	e.do("GET", "/api/v0/nodes/other/ambient-noise-measurements/"+rid, nil, bearer(owner)).detail(404, "Not found")
}

func TestBusRequestResponse(t *testing.T) {
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	go func() {
		pk := c.next()
		p := payload(t, pk)
		reply, _ := json.Marshal(map[string]any{"commands": []string{"x"}})
		c.publish(pk.TopicName+"/response/"+p["correlation_id"].(string), reply)
	}()
	e.advance(5 * time.Minute) // past the liveness debounce
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := e.m.Bus().Request(ctx, "n1", "command-data/commands", map[string]any{"op": "commands"})
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != `{"commands":["x"]}` {
		t.Fatalf("%s", res)
	}
	var seen string
	e.d.Read.QueryRow(`SELECT last_seen FROM cc_nodes WHERE node_id = 'n1'`).Scan(&seen)
	if parseTS(seen) != e.clock() {
		t.Fatalf("round trip didn't refresh liveness: %s", seen)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel2()
	if _, err := e.m.Bus().Request(ctx2, "n1", "context/query", nil); err != ErrNoResult {
		t.Fatalf("timeout err %v", err)
	}
}

func TestConfigPush(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	body := map[string]any{"config_type": "auth:nest", "ciphertext": "c", "nonce": "n", "tag": "t"}
	e.do("POST", "/api/v0/nodes/n1/config/push", body, bearer(stranger)).want(403)
	p := e.do("POST", "/api/v0/nodes/n1/config/push", body, bearer(owner)).want(201).json()
	m := payload(t, c.next())
	if m["push_id"] != p["id"] || m["config_type"] != "auth:nest" {
		t.Fatal(m)
	}
	// D6: node auth bound to the path node.
	e.do("GET", "/api/v0/nodes/n1/config/pending", nil, nil).want(400)
	e.do("GET", "/api/v0/nodes/n1/config/pending", nil, other.h()).want(403)
	if l := e.do("GET", "/api/v0/nodes/n1/config/pending", nil, n.h()).want(200).list(); len(l) != 1 {
		t.Fatal(l)
	}
	e.do("POST", "/api/v0/nodes/n1/config/"+p["id"].(string)+"/ack", nil, other.h()).want(403)
	if r := e.do("POST", "/api/v0/nodes/n1/config/"+p["id"].(string)+"/ack", nil, n.h()).want(200).json(); r["status"] != "consumed_and_deleted" {
		t.Fatal(r)
	}
	e.do("POST", "/api/v0/nodes/n1/config/"+p["id"].(string)+"/ack", nil, n.h()).detail(404, "Config push not found")
}

func TestNoBroker(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	cr := e.do("GET", "/api/v0/node/mqtt-credentials", nil, n.h()).want(200).json()
	if cr["username"] != nil || cr["password"] != nil {
		t.Fatal(cr)
	}
	e.do("POST", "/api/v0/nodes/n1/k2", map[string]any{"k2": "a", "kid": "b", "created_at": "c"}, bearer(owner)).detail(503, "MQTT not available")
	// Commands still return a request id (legacy: stored, not delivered).
	e.do("POST", "/api/v0/nodes/n1/led/preview", map[string]any{"pattern": "x"}, bearer(owner)).want(200)
}

func TestTraces(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createNode("n1", "hh1")
	id, err := e.m.RecordTrace(ctx, Trace{ConversationID: "c1", RequestType: "voice_command", Source: "node", NodeID: "n1",
		HouseholdID: "hh1", UserCommand: "hi", TotalDurationMS: 12.5, Spans: []map[string]any{{"name": "stt"}}})
	if err != nil {
		t.Fatal(err)
	}
	e.m.RecordTrace(ctx, Trace{ConversationID: "c2", RequestType: "stt", Source: "mobile", Status: "error", TotalDurationMS: 1})
	if id2, _ := e.m.RecordTrace(ctx, Trace{ConversationID: "c3", Source: "unknown"}); id2 != "" {
		t.Fatal("unknown source stored")
	}
	e.do("GET", "/api/v0/admin/traces", nil, nil).want(400)
	l := e.do("GET", "/api/v0/admin/traces?source=node", nil, adminH()).want(200).json()
	traces := l["traces"].([]any)
	if l["total"] != 1.0 || len(traces) != 1 || traces[0].(map[string]any)["span_count"] != 1.0 {
		t.Fatal(l)
	}
	e.do("GET", "/api/v0/admin/traces?limit=0", nil, adminH()).want(400)
	d := e.do("GET", "/api/v0/admin/traces/"+id, nil, adminH()).want(200).json()
	if d["spans"].([]any)[0].(map[string]any)["name"] != "stt" || d["error_message"] != nil {
		t.Fatal(d)
	}
	e.do("GET", "/api/v0/admin/traces/nope", nil, adminH()).detail(404, "Trace not found")
	e.advance(8 * 24 * time.Hour)
	e.m.cleanup(ctx)
	if l := e.do("GET", "/api/v0/admin/traces", nil, adminH()).json(); l["total"] != 0.0 {
		t.Fatal(l)
	}
}

// capturePub records publishes.
type capturePub struct{ payloads [][]byte }

func (p *capturePub) Publish(_ string, payload []byte, _ byte, _ bool) error {
	p.payloads = append(p.payloads, payload)
	return nil
}

func (p *capturePub) Request(context.Context, string, []byte, string) ([]byte, error) {
	return nil, context.Canceled
}

// A reply key is also the published request_id, as legacy publish_command_with_id(...,
// request_id) did for tool_call (request_id == reply_request_id == tool_call_id).
func TestBusCommandAwaitReplyKeyIsRequestID(t *testing.T) {
	pub := &capturePub{}
	b := newBus(pub, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rid, _, _ := b.CommandAwait(ctx, "n1", "tool_call", map[string]any{"reply_request_id": "k1"}, "k1")
	var msg []struct {
		Details map[string]any `json:"details"`
	}
	if len(pub.payloads) != 1 || json.Unmarshal(pub.payloads[0], &msg) != nil || len(msg) != 1 {
		t.Fatalf("published %q", pub.payloads)
	}
	if rid != "k1" || msg[0].Details["request_id"] != "k1" {
		t.Fatalf("rid %q, details %v", rid, msg[0].Details)
	}
	// No reply key: a fresh rid, used for both.
	rid, _, _ = b.CommandAwait(ctx, "n1", "action", map[string]any{}, "")
	if err := json.Unmarshal(pub.payloads[1], &msg); err != nil || rid == "" || msg[0].Details["request_id"] != rid {
		t.Fatalf("rid %q, details %v", rid, msg[0].Details)
	}
}

func TestBusVerifyExpiry(t *testing.T) {
	now := time.Now()
	b := newBus(nil, nil, func() time.Time { return now })
	rid := b.Command("n1", "measure_ambient_noise", map[string]any{"request_id": "ignored"})
	if rid == "ignored" {
		t.Fatal("caller request_id must be overridden")
	}
	now = now.Add(6 * time.Minute)
	if b.VerifyCommand(rid, "n1") {
		t.Fatal("expired command verified")
	}
}
