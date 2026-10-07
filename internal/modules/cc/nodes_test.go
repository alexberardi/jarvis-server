package cc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestNodeAuthErrors(t *testing.T) {
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	const path = "/api/v0/node/mqtt-credentials"

	r := e.do("GET", path, nil, nil).want(400).json()
	if r["error"] != "validation_error" || r["details"].([]any)[0] != "header -> x-api-key: Field required" {
		t.Fatalf("missing header: %v", r)
	}
	e.do("GET", path, nil, hdr{"X-API-Key": "bare"}).detail(401, "Invalid API Key")
	e.do("GET", path, nil, hdr{"X-API-Key": "n1:wrong"}).detail(401, "Invalid node credentials")
	e.do("GET", path, nil, hdr{"X-API-Key": "nosuch:k"}).detail(401, "Node not found")

	// Valid in auth, but no CC row.
	e.auth.nodes["authonly"] = &fakeNode{key: "k", household: "hh1", active: true, services: []string{ServiceName}}
	e.do("GET", path, nil, hdr{"X-API-Key": "authonly:k"}).detail(401, "Node not configured locally")
	e.auth.nodes["logsonly"] = &fakeNode{key: "k", household: "hh1", active: true, services: []string{"jarvis-logs"}}
	e.do("GET", path, nil, hdr{"X-API-Key": "logsonly:k"}).detail(401, "Node is not authorized to access service 'jarvis-command-center'")

	cr := e.do("GET", path, nil, n.h()).want(200).json()
	if cr["username"] != "n1" || cr["password"] != n.key {
		t.Fatalf("mqtt credentials: %v", cr)
	}

	// User JWT errors.
	r2 := e.do("POST", "/api/v0/nodes/n1/settings/requests", nil, nil)
	r2.detail(401, "Missing or invalid Authorization header")
	if r2.header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatal("missing WWW-Authenticate")
	}
	e.do("POST", "/api/v0/nodes/n1/settings/requests", nil, bearer("junk")).detail(401, "Invalid token")
	e.do("POST", "/api/v0/nodes/n1/settings/requests", nil, bearer("expired")).detail(401, "Token has expired")

	// Admin key.
	e.do("POST", "/api/v0/admin/nodes", map[string]any{}, hdr{"X-API-Key": "nope"}).detail(401, "Invalid Admin API Key")
	if r := e.do("POST", "/api/v0/admin/nodes", map[string]any{}, nil).want(400).json(); r["details"].([]any)[0] != "header -> x-api-key: Field required" {
		t.Fatalf("%v", r)
	}
}

func TestCreateGetHeartbeat(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	stranger := e.auth.addUser(2, "hh2", authn.RoleAdmin)
	n := e.createNode("n1", "hh1")
	if !e.auth.active("n1") || len(e.auth.nodes["n1"].services) != 2 {
		t.Fatalf("auth registration: %+v", e.auth.nodes["n1"])
	}
	e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": "n1", "household_id": "hh1", "room": "x"}, adminH()).
		detail(400, "Node already exists locally")
	e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": "n9", "household_id": "missing-household", "room": "x"}, adminH()).
		detail(404, "Household not found")
	if r := e.do("POST", "/api/v0/admin/nodes", map[string]any{"node_id": "n9"}, adminH()).want(400).json(); len(r["details"].([]any)) != 2 {
		t.Fatalf("validation: %v", r)
	}

	got := e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200).json()
	if got["user"] != "default" || got["voice_mode"] != "brief" || got["needs_k2"] != true || got["online"] != true ||
		got["household_id"] != "hh1" || got["room"] != "kitchen" {
		t.Fatalf("fresh node: %v", got)
	}
	if _, ok := got["adapter_hash"]; ok {
		t.Fatal("adapter_hash should be gone (D9)")
	}
	e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("GET", "/api/v0/admin/nodes/nope", nil, bearer(owner)).detail(404, "Node not found")

	e.do("POST", "/api/v0/admin/nodes/heartbeat", nil, n.h()).want(200)
	r := e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
		"version_info": map[string]any{"version": "0.1.0", "install_mode": "tarball", "git_sha": "abc"},
		"is_busy":      true, "protocols": []string{"lifx"}, "needs_k2": false, "extra": 1,
	}, n.h()).want(200).json()
	if len(r) != 1 || r["status"] != "ok" {
		t.Fatalf("heartbeat: %v", r)
	}
	got = e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200).json()
	if got["last_seen_version"] != "0.1.0" || got["install_mode"] != "tarball" || got["is_busy"] != true || got["needs_k2"] != false {
		t.Fatalf("after heartbeat: %v", got)
	}
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"is_busy": "maybe"}, n.h()).want(400)

	// Online threshold.
	e.advance(16 * time.Minute)
	if got := e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).json(); got["online"] != false {
		t.Fatalf("should be offline: %v", got)
	}

	// List: members see their household's nodes only; inactive hidden.
	e.createNode("n2", "hh2")
	if l := e.do("GET", "/api/v0/admin/nodes", nil, bearer(owner)).want(200).list(); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	e.do("GET", "/api/v0/admin/nodes?household_id=hh2", nil, bearer(owner)).want(403)
	e.d.Write.Exec(`UPDATE cc_nodes SET is_active = 0 WHERE node_id = 'n1'`)
	if l := e.do("GET", "/api/v0/admin/nodes", nil, bearer(owner)).list(); len(l) != 0 {
		t.Fatalf("inactive listed: %v", l)
	}
	if l := e.do("GET", "/api/v0/admin/nodes?include_inactive=true", nil, bearer(owner)).list(); len(l) != 1 {
		t.Fatalf("include_inactive: %v", l)
	}

	// PATCH.
	p := e.do("PATCH", "/api/v0/admin/nodes/n2", map[string]any{"room": "den", "voice_mode": nil}, adminH()).want(200).json()
	if p["room"] != "den" || p["voice_mode"] != nil {
		t.Fatalf("patch: %v", p)
	}
	e.do("PATCH", "/api/v0/admin/nodes/zz", map[string]any{}, adminH()).detail(404, "Node not found")
}

func TestLivenessDebounce(t *testing.T) {
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	seen := func() string {
		var s string
		e.d.Read.QueryRow(`SELECT last_seen FROM cc_nodes WHERE node_id = 'n1'`).Scan(&s)
		return s
	}
	// The first contact always writes (registration's stamp doesn't count, A10 F19).
	e.advance(10 * time.Second)
	e.do("GET", "/api/v0/node/mqtt-credentials", nil, n.h()).want(200)
	first := seen()
	e.advance(30 * time.Second)
	e.do("GET", "/api/v0/node/mqtt-credentials", nil, n.h()).want(200)
	if seen() != first {
		t.Fatal("liveness write inside the 60 s debounce")
	}
	e.advance(31 * time.Second)
	e.do("GET", "/api/v0/node/mqtt-credentials", nil, n.h()).want(200)
	if seen() == first {
		t.Fatal("liveness not refreshed after the debounce")
	}
}

func TestProvisioning(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.auth.addUser(1, "hh3", authn.RoleMember) // multi-household user (D5)
	other := e.auth.addUser(2, "hh2", authn.RoleAdmin)

	// D5: the target household's membership, among all of the caller's.
	tok := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3", "room": "office"}, bearer(member)).want(201).json()
	if !strings.HasPrefix(tok["token"].(string), "prov_") || tok["expires_in"] != 600.0 {
		t.Fatalf("token: %v", tok)
	}
	e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3"}, bearer(other)).
		detail(403, "User is not a member of this household")
	e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3"}, hdr{"X-API-Key": "bad"}).detail(401, "Invalid API key")
	e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3"}, bearer("junk")).detail(401, "Invalid or expired JWT")
	e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3"}, nil).detail(401, "Authentication required")

	nid := tok["node_id"].(string)
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": nid, "provisioning_token": "prov_wrong"}, nil).
		detail(401, "Invalid or expired provisioning token")
	reg := e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": nid, "provisioning_token": tok["token"]}, nil).want(201).json()
	if reg["room"] != "office" || reg["user"] != "default" || reg["voice_mode"] != "brief" || reg["node_key"] == "" {
		t.Fatalf("register: %v", reg)
	}
	// Consumed.
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": nid, "provisioning_token": tok["token"]}, nil).
		detail(401, "Invalid or expired provisioning token")
	// Refresh of a registered id.
	e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh3", "node_id": nid}, adminH()).
		detail(400, "Node already registered")

	// Expiry, and body room beating the token's.
	t2 := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1", "room": "a"}, adminH()).want(201).json()
	e.advance(11 * time.Minute)
	e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": t2["node_id"], "provisioning_token": t2["token"]}, nil).want(401)
	t3 := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1", "room": "a"}, adminH()).want(201).json()
	r3 := e.do("POST", "/api/v0/nodes/register", map[string]any{"node_id": t3["node_id"], "provisioning_token": t3["token"], "room": "b"}, nil).want(201).json()
	if r3["room"] != "b" {
		t.Fatalf("room precedence: %v", r3)
	}

	// Cleanup drops tokens expired over a day ago.
	e.advance(25 * time.Hour)
	if err := e.m.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var left int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_provisioning_tokens`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d tokens left", left)
	}
}

func TestDeleteAndVerifyReset(t *testing.T) {
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(2, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	e.do("DELETE", "/api/v0/admin/nodes/n1", nil, bearer(member)).detail(403, "User has member role, requires power_user or higher")
	e.do("DELETE", "/api/v0/admin/nodes/n1", nil, bearer(owner)).want(200)
	pk := c.next()
	p := payload(t, pk)
	if pk.TopicName != "jarvis/nodes/n1/factory-reset" || pk.FixedHeader.Qos != 1 || pk.FixedHeader.Retain || len(p) != 2 || p["node_id"] != "n1" {
		t.Fatalf("reset publish %s %v", pk.TopicName, p)
	}
	if e.auth.active("n1") {
		t.Fatal("auth not deactivated")
	}
	body := map[string]any{"node_id": "n1", "request_id": p["request_id"]}
	if r := e.do("POST", "/api/v0/nodes/verify-reset", body, nil).want(200).json(); r["verified"] != true {
		t.Fatal(r)
	}
	e.do("POST", "/api/v0/nodes/verify-reset", body, nil).detail(404, "Invalid or expired reset token")
	e.do("GET", "/api/v0/admin/nodes/n1", nil, bearer(owner)).detail(404, "Node not found")
}

func TestTrackedFactoryReset(t *testing.T) {
	path := t.TempDir() + "/jarvis.db"
	auth := newFakeAuth()
	e := newEnv(t, envOpts{dbPath: path, auth: auth})
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	r := e.do("POST", "/api/v0/admin/nodes/n1/factory-reset", nil, bearer(owner)).want(200).json()
	tid, tok := r["task_id"].(string), r["reset_token"].(string)
	p := payload(t, c.next())
	if p["task_id"] != tid || p["request_id"] != tok || p["node_id"] != "n1" {
		t.Fatalf("publish %v", p)
	}
	conflict := e.do("POST", "/api/v0/admin/nodes/n1/factory-reset", nil, bearer(owner)).want(409).json()["detail"].(map[string]any)
	if conflict["task_id"] != tid || conflict["state"] != "dispatched" {
		t.Fatalf("409: %v", conflict)
	}

	// A restart (new module on the same DB) keeps the token: it lives on the task (D10).
	e2 := newEnv(t, envOpts{dbPath: path, auth: auth})
	status := "/api/v0/nodes/factory-reset/" + tid + "/status"
	e2.do("POST", status, map[string]any{"state": "in_progress"}, nil).detail(401, "Invalid or expired reset token")
	e2.do("POST", status, map[string]any{"state": "in_progress"}, hdr{"X-Reset-Token": "nope"}).detail(401, "Invalid or expired reset token")
	e2.do("POST", status, map[string]any{"state": "bogus"}, hdr{"X-Reset-Token": tok}).
		detail(400, "state must be one of: in_progress, success, failed (got 'bogus')")
	e2.do("POST", "/api/v0/nodes/factory-reset/other/status", map[string]any{"state": "in_progress"}, hdr{"X-Reset-Token": tok}).
		detail(404, "Factory-reset task not found")
	if s := e2.do("POST", status, map[string]any{"state": "in_progress"}, hdr{"X-Reset-Token": tok}).want(200).json(); s["state"] != "in_progress" {
		t.Fatal(s)
	}
	// Not consumed: the same token reports success, which deactivates the node.
	s := e2.do("POST", status, map[string]any{"state": "success"}, hdr{"X-Reset-Token": tok}).want(200).json()
	if s["state"] != "success" || s["finished_at"] == nil || s["kind"] != "factory_reset" {
		t.Fatal(s)
	}
	if auth.active("n1") {
		t.Fatal("auth not deactivated on success")
	}
	// Terminal: idempotent.
	if s := e2.do("POST", status, map[string]any{"state": "failed"}, hdr{"X-Reset-Token": tok}).want(200).json(); s["state"] != "success" {
		t.Fatal(s)
	}
	if l := e2.do("GET", "/api/v0/admin/nodes", nil, bearer(owner)).list(); len(l) != 0 {
		t.Fatalf("reset node still listed: %v", l)
	}
}

func TestTrackedResetViaVerifyReset(t *testing.T) {
	// An older node build ignores task_id and calls verify-reset with the task's token.
	e := newEnv(t)
	owner := e.auth.addUser(1, "hh1", authn.RoleAdmin)
	e.createNode("n1", "hh1")
	r := e.do("POST", "/api/v0/admin/nodes/n1/factory-reset", nil, bearer(owner)).want(200).json()
	body := map[string]any{"node_id": "n1", "request_id": r["reset_token"]}
	e.do("POST", "/api/v0/nodes/verify-reset", map[string]any{"node_id": "n2", "request_id": r["reset_token"]}, nil).want(404)
	e.do("POST", "/api/v0/nodes/verify-reset", body, nil).want(200)
	e.do("POST", "/api/v0/nodes/verify-reset", body, nil).want(404)
	task := e.do("GET", "/api/v0/tasks/"+r["task_id"].(string), nil, bearer(owner)).want(200).json()
	if task["state"] != "success" || e.auth.active("n1") {
		t.Fatalf("task %v, auth active %v", task, e.auth.active("n1"))
	}
}

func TestUpdates(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/alexberardi/jarvis-node-setup/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v0.5.0","published_at":"2026-10-01T00:00:00Z"}`))
	}))
	defer gh.Close()
	e := newEnv(t, envOpts{github: gh.URL})
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	hb := func(version string, busy bool) map[string]any {
		return e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{
			"version_info": map[string]any{"version": version, "install_mode": "tarball"}, "is_busy": busy,
		}, n.h()).want(200).json()
	}
	hb("0.4.0", false)

	// Gate off (default): "latest" can't resolve; /releases/latest is null.
	if r := e.do("GET", "/api/v0/releases/latest", nil, nil).want(200); strings.TrimSpace(string(r.body)) != "null" {
		t.Fatalf("latest with gate off: %s", r.body)
	}
	e.do("POST", "/api/v0/nodes/n1/update", nil, bearer(owner)).want(503)
	if err := e.m.settings.Set(context.Background(), settingUpdatesAllowCheck, true, settingsScope("hh1")); err != nil {
		t.Fatal(err)
	}
	// D40 Q5: not newer → 400.
	e.do("POST", "/api/v0/nodes/n1/update", map[string]any{"target_version": "v0.4.0"}, bearer(owner)).want(400)
	task := e.do("POST", "/api/v0/nodes/n1/update", nil, bearer(owner)).want(200).json()
	if task["target_version"] != "0.5.0" || task["state"] != "pending" {
		t.Fatal(task)
	}
	tid := task["id"].(string)
	e.do("POST", "/api/v0/nodes/n1/update", nil, bearer(owner)).want(409)

	// Busy: no dispatch. Idle: dispatched once.
	if r := hb("0.4.0", true); r["pending_update"] != nil {
		t.Fatal(r)
	}
	r := hb("0.4.0", false)
	if pu := r["pending_update"].(map[string]any); pu["task_id"] != tid || pu["target_version"] != "0.5.0" {
		t.Fatal(r)
	}
	if r := hb("0.4.0", false); r["pending_update"] != nil {
		t.Fatal("dispatched twice")
	}
	e.advance(31 * time.Second)
	hb("0.4.0", false)
	if s := e.do("GET", "/api/v0/tasks/"+tid, nil, bearer(owner)).json(); s["state"] != "in_progress" {
		t.Fatal(s)
	}
	hb("0.5.0", false)
	if s := e.do("GET", "/api/v0/tasks/"+tid, nil, bearer(owner)).json(); s["state"] != "success" {
		t.Fatal(s)
	}
	e.do("POST", "/api/v0/nodes/n1/tasks/"+tid+"/cancel", nil, bearer(owner)).detail(409, "Task is already success")

	// Docker installs are refused up front.
	e.d.Write.Exec(`UPDATE cc_nodes SET install_mode = 'docker' WHERE node_id = 'n1'`)
	e.do("POST", "/api/v0/nodes/n1/update", map[string]any{"target_version": "9.0.0"}, bearer(owner)).want(400)
	e.d.Write.Exec(`UPDATE cc_nodes SET install_mode = 'tarball' WHERE node_id = 'n1'`)

	// Node-reported failure; terminal states are immutable.
	t2 := e.do("POST", "/api/v0/nodes/n1/update", map[string]any{"target_version": "0.6.0"}, bearer(owner)).want(200).json()["id"].(string)
	e.do("POST", "/api/v0/nodes/tasks/"+t2+"/status", map[string]any{"state": "success"}, n.h()).want(400)
	f := e.do("POST", "/api/v0/nodes/tasks/"+t2+"/status", map[string]any{"state": "failed", "error_message": "consent"}, n.h()).want(200).json()
	if f["state"] != "failed" || f["error_message"] != "consent" {
		t.Fatal(f)
	}
	e.do("POST", "/api/v0/nodes/tasks/"+t2+"/status", map[string]any{"state": "failed"}, n.h()).detail(409, "Task is already failed")
	if l := e.do("GET", "/api/v0/nodes/n1/tasks?limit=1", nil, bearer(owner)).want(200).list(); len(l) != 1 {
		t.Fatal(l)
	}

	// Sweeper: kind-specific messages, factory resets wait days (D10, D8).
	t3 := e.do("POST", "/api/v0/nodes/n1/update", map[string]any{"target_version": "0.7.0"}, bearer(owner)).want(200).json()["id"].(string)
	e.do("POST", "/api/v0/admin/nodes/n1/factory-reset", nil, bearer(owner)).want(403) // member is not power_user
	e.auth.roles["hh1"][1] = authn.RoleAdmin
	fr := e.do("POST", "/api/v0/admin/nodes/n1/factory-reset", nil, bearer(owner)).want(200).json()["task_id"].(string)
	e.advance(16 * time.Minute)
	if _, err := e.m.sweepTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := e.do("GET", "/api/v0/tasks/"+t3, nil, bearer(owner)).json(); s["state"] != "failed" || s["error_message"] != "Timeout: no heartbeat confirming 0.7.0" {
		t.Fatal(s)
	}
	if s := e.do("GET", "/api/v0/tasks/"+fr, nil, bearer(owner)).json(); s["state"] != "dispatched" {
		t.Fatal(s)
	}
	e.advance(8 * 24 * time.Hour)
	e.m.sweepTasks(context.Background())
	if s := e.do("GET", "/api/v0/tasks/"+fr, nil, bearer(owner)).json(); s["state"] != "failed" || !strings.Contains(s["error_message"].(string), "factory reset") {
		t.Fatal(s)
	}

	// The gate opens /releases/latest globally only at system scope.
	if r := e.do("GET", "/api/v0/releases/latest", nil, nil); strings.TrimSpace(string(r.body)) != "null" {
		t.Fatal(string(r.body))
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
		ok   bool
	}{{"0.3.10", "0.3.9", 1, true}, {"1.0", "1.0.0", 0, true}, {"v1.2.0", "1.3", -1, true}, {"1.0-rc1", "1.0", 0, false}} {
		got, ok := compareVersions(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("compare(%s, %s) = %d %v", c.a, c.b, got, ok)
		}
	}
}

func TestPurgeUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createNode("n1", "hh1")
	if _, err := e.m.RecordTrace(ctx, Trace{ConversationID: "c", RequestType: "voice_command", Source: "node", NodeID: "n1", UserID: 7, TotalDurationMS: 1}); err != nil {
		t.Fatal(err)
	}
	e.m.RecordTrace(ctx, Trace{ConversationID: "c2", RequestType: "voice_command", Source: "node", NodeID: "gone", TotalDurationMS: 1})
	tx, _ := e.d.Write.Begin()
	if err := e.m.PurgeUser(ctx, tx, 7); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	var n int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_request_traces`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d traces", n)
	}
}
