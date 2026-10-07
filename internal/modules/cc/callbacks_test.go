package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

const cbHH = "hh-cb"

type cbEnv struct {
	*env
	notify *fakeNotifier
	member string // user 5, member of cbHH
}

func newCBEnv(t *testing.T, o ...envOpts) *cbEnv {
	t.Helper()
	n := &fakeNotifier{}
	var opt envOpts
	if len(o) > 0 {
		opt = o[0]
	}
	inner := opt.configure
	opt.configure = func(m *Module) {
		m.Notify = n
		if inner != nil {
			inner(m)
		}
	}
	e := newEnv(t, opt)
	return &cbEnv{env: e, notify: n, member: e.auth.addUser(5, cbHH, authn.RoleMember)}
}

func (ce *cbEnv) items() []string {
	ce.notify.mu.Lock()
	defer ce.notify.mu.Unlock()
	var out []string
	for _, it := range ce.notify.items {
		out = append(out, it.Category+"|"+it.Title)
	}
	return out
}

// onlineNode creates a node in hh and makes it live (an authenticated request stamps last_seen).
func (ce *cbEnv) onlineNode(id, hh string) testNode {
	ce.t.Helper()
	n := ce.createNode(id, hh)
	ce.do("POST", "/api/v0/admin/nodes/heartbeat", nil, n.h()).want(200)
	return n
}

// pollStatus waits for a job to leave pending (the server plane runs off the request).
func (ce *cbEnv) pollStatus(id, tok string) map[string]any {
	ce.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		r := ce.do("GET", "/api/v0/callbacks/"+id+"/status", nil, bearer(tok)).want(200).json()
		if r["status"] != "pending" || time.Now().After(deadline) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// nodeCallback is jarvis-node-setup's handle_callback over raw HTTP (safe off the test
// goroutine): GET the payload, then POST result.
func nodeCallback(base string, n testNode, jobID string, result map[string]any) error {
	req, _ := http.NewRequest("GET", base+"/api/v0/callbacks/"+jobID, nil)
	req.Header.Set("X-API-Key", n.id+":"+n.key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("payload %d", res.StatusCode)
	}
	raw, _ := json.Marshal(result)
	req, _ = http.NewRequest("POST", base+"/api/v0/callbacks/"+jobID+"/result", bytes.NewReader(raw))
	req.Header.Set("X-API-Key", n.id+":"+n.key)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("result %d", res.StatusCode)
	}
	return nil
}

func TestCallbackNodePlaneRoundTrip(t *testing.T) {
	ce := newCBEnv(t)
	n := ce.onlineNode("n1", cbHH)
	other := ce.onlineNode("n2", cbHH)
	c := ce.dialNode(n)
	c.subscribe("jarvis/nodes/n1/commands")

	r := ce.do("POST", "/api/v0/callbacks", `{"command_name": "movies", "callback_name": "expand_actor",
		"data": {"b": 1, "a": "é"}, "target_node_id": "n1"}`, bearer(ce.member)).want(201).json()
	id := r["id"].(string)
	if r["status"] != "pending" || r["navigation_type"] != "new_notification" || r["created_at"] != "2026-10-06T12:00:00" {
		t.Fatal(r)
	}
	// MQTT carries the opaque id only.
	pk := c.next()
	var cmds []map[string]any
	_ = json.Unmarshal(pk.Payload, &cmds)
	if len(cmds) != 1 || cmds[0]["command"] != "callback" || fmt.Sprint(cmds[0]["details"]) != "map[request_id:"+id+"]" {
		t.Fatalf("%s", pk.Payload)
	}
	var stored string
	ce.d.Read.QueryRow(`SELECT data_json FROM cc_callback_jobs WHERE id = ?`, id).Scan(&stored)
	if stored != `{"b": 1, "a": "\u00e9"}` {
		t.Fatal(stored) // json.dumps bytes, dict order kept
	}

	// Only the owning node reads or completes it; others get 404 (no existence leak).
	ce.do("GET", "/api/v0/callbacks/"+id, nil, other.h()).detail(404, "Callback job not found")
	ce.do("POST", "/api/v0/callbacks/"+id+"/result", map[string]any{"success": true}, other.h()).detail(404, "Callback job not found")
	ce.do("GET", "/api/v0/callbacks/nope", nil, n.h()).detail(404, "Callback job not found")
	p := ce.do("GET", "/api/v0/callbacks/"+id, nil, n.h()).want(200)
	if string(bytes.TrimSpace(p.body)) != `{"callback_name":"expand_actor","command_name":"movies","conversation_id":"callback:`+id+
		`","data":{"b":1,"a":"é"},"job_id":"`+id+`","user_id":5,"voice_command":"cb:expand_actor"}` {
		t.Fatalf("%s", p.body)
	}

	// Mobile polls while pending.
	if s := ce.do("GET", "/api/v0/callbacks/"+id+"/status", nil, bearer(ce.member)).want(200).json(); s["status"] != "pending" ||
		s["completed_at"] != nil || s["context_data"] != nil || s["error_message"] != nil {
		t.Fatal(s)
	}
	ce.do("POST", "/api/v0/callbacks/"+id+"/result", map[string]any{}, n.h()).want(400)
	res := ce.do("POST", "/api/v0/callbacks/"+id+"/result", map[string]any{"success": true, "error": nil, "context_data": map[string]any{
		"inbox": map[string]any{"title": "Tom Hanks", "summary": "Actor", "metadata": map[string]any{"x": 1}}}}, n.h()).want(200).json()
	if res["id"] != id || res["status"] != "completed" || res["completed_at"] != "2026-10-06T12:00:00" {
		t.Fatal(res)
	}
	ce.notify.mu.Lock()
	it := ce.notify.items[0]
	ce.notify.mu.Unlock()
	if it.Title != "Tom Hanks" || it.Category != "callback_result" || it.Metadata["node_id"] != "n1" || it.UserID == nil || *it.UserID != 5 ||
		it.HouseholdID != cbHH || it.Summary != "Actor" {
		t.Fatalf("%+v", it)
	}
	s := ce.do("GET", "/api/v0/callbacks/"+id+"/status", nil, bearer(ce.member)).want(200).json()
	if s["status"] != "completed" || s["context_data"].(map[string]any)["inbox"].(map[string]any)["title"] != "Tom Hanks" {
		t.Fatal(s)
	}
	// A finished job can still be read by its node (legacy: only expired/unknown states are 410).
	ce.do("GET", "/api/v0/callbacks/"+id, nil, n.h()).want(200)

	// Cross-household: an outsider can't tap the node, nor poll the job.
	outsider := ce.auth.addUser(9, "hh-other", authn.RoleOwner)
	ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "movies", "callback_name": "x", "target_node_id": "n1"},
		bearer(outsider)).detail(403, "User is not a member of this household")
	ce.do("GET", "/api/v0/callbacks/"+id+"/status", nil, bearer(outsider)).detail(403, "User is not a member of this household")
	// D5: a user in several households may tap any household's node they belong to.
	ce.auth.addUser(9, cbHH, authn.RoleMember)
	ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "movies", "callback_name": "x", "target_node_id": "n1"},
		bearer(outsider)).want(201)
}

func TestCallbackStackNoFanOutAndExpiry(t *testing.T) {
	ce := newCBEnv(t)
	n := ce.onlineNode("n1", cbHH)
	r := ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": "n1",
		"navigation_type": "stack"}, bearer(ce.member)).want(201).json()
	id := r["id"].(string)
	ce.do("POST", "/api/v0/callbacks/"+id+"/result", map[string]any{"success": true, "context_data": map[string]any{
		"inbox": map[string]any{"title": "Inline"}}}, n.h()).want(200)
	if len(ce.items()) != 0 {
		t.Fatal(ce.items()) // stack renders inline: no fan-out
	}

	r = ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": "n1",
		"navigation_type": "stack"}, bearer(ce.member)).want(201).json()
	id2 := r["id"].(string)
	ce.advance(6 * time.Minute)
	ce.do("GET", "/api/v0/callbacks/"+id2, nil, n.h()).detail(410, "Callback job expired")
	ce.do("GET", "/api/v0/callbacks/"+id2, nil, n.h()).detail(410, "Callback job is expired")
	if s := ce.do("GET", "/api/v0/callbacks/"+id2+"/status", nil, bearer(ce.member)).want(200).json(); s["status"] != "expired" {
		t.Fatal(s)
	}
}

func TestCallbackCreateErrors(t *testing.T) {
	ce := newCBEnv(t)
	ce.createNode("lonely", "")
	_, _ = ce.d.Write.Exec(`UPDATE cc_nodes SET household_id = NULL WHERE node_id = 'lonely'`)
	tap := func(b map[string]any) *resp { return ce.do("POST", "/api/v0/callbacks", b, bearer(ce.member)) }
	tap(map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": "ghost"}).detail(404, "Target node not found")
	tap(map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": "lonely"}).detail(400, "Target node has no household")
	for _, b := range []map[string]any{
		{"callback_name": "cb", "target_node_id": "n1"},
		{"command_name": "", "callback_name": "cb", "target_node_id": "n1"},
		{"command_name": "c", "callback_name": "cb", "target_node_id": ""},
		{"command_name": "c", "callback_name": "cb", "household_id": ""},
		{"command_name": "c", "callback_name": "cb", "target_node_id": "n1", "navigation_type": "sideways"},
		{"command_name": "c", "callback_name": "cb", "target_node_id": "n1", "data": []any{1}},
	} {
		if r := tap(b).want(400).json(); r["error"] != "validation_error" {
			t.Fatal(b, r)
		}
	}
	ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb"}, nil).want(401)

	// Server plane: handler first (400), then household (400), then membership (403).
	tap(map[string]any{"command_name": "c", "callback_name": "cb", "household_id": cbHH}).
		detail(400, "No server-side handler registered for c.cb")
	tap(map[string]any{"command_name": "schedule", "callback_name": "cancel_schedule"}).
		detail(400, "household_id is required when target_node_id is omitted")
	tap(map[string]any{"command_name": "schedule", "callback_name": "cancel_schedule", "household_id": "hh-other"}).
		detail(403, "User is not a member of this household")
	ce.do("GET", "/api/v0/callbacks/nope/status", nil, bearer(ce.member)).detail(404, "Callback job not found")
}

// TestCallbackFailFast: D40 Q4 — an offline node (or no MQTT) fails the tap at once, with a
// "couldn't reach" card for a new_notification tap.
func TestCallbackFailFast(t *testing.T) {
	ce := newCBEnv(t)
	n := ce.createNode("n1", cbHH)
	_, _ = ce.d.Write.Exec(`UPDATE cc_nodes SET room = 'Kitchen', last_seen = ? WHERE node_id = 'n1'`, dbTime(ce.clock().Add(-time.Hour)))
	r := ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": n.id},
		bearer(ce.member)).want(201).json()
	if r["status"] != "failed" {
		t.Fatal(r)
	}
	s := ce.do("GET", "/api/v0/callbacks/"+r["id"].(string)+"/status", nil, bearer(ce.member)).want(200).json()
	if s["status"] != "failed" || s["error_message"] != "Node is offline" || s["completed_at"] == nil {
		t.Fatal(s)
	}
	if got := ce.items(); !slices.Equal(got, []string{"callback_result|Couldn't reach Kitchen"}) {
		t.Fatal(got)
	}
	// A stack tap sees the failure on its poll; no card.
	ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": n.id,
		"navigation_type": "stack"}, bearer(ce.member)).want(201)
	if len(ce.items()) != 1 {
		t.Fatal(ce.items())
	}

	noMQTT := newCBEnv(t, envOpts{noMQTT: true})
	n2 := noMQTT.onlineNode("n2", cbHH)
	r = noMQTT.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "c", "callback_name": "cb", "target_node_id": n2.id},
		bearer(noMQTT.member)).want(201).json()
	s = noMQTT.do("GET", "/api/v0/callbacks/"+r["id"].(string)+"/status", nil, bearer(noMQTT.member)).want(200).json()
	if s["status"] != "failed" || s["error_message"] != "MQTT is not available" {
		t.Fatal(s)
	}
}

// TestServerCallbackWiring: every 5c handler is in the static map (D40 Q9) and one real tap per
// subsystem reaches its handler through POST /callbacks.
func TestServerCallbackWiring(t *testing.T) {
	ce := newCBEnv(t)
	want := []string{
		"errand.approve_errand_plan", "errand.approve_replan", "errand.discard_errand_plan", "errand.refine_errand_plan",
		"errand.stop_errand", "jarvis.proposable_action.dismiss", "jarvis.proposable_action.execute",
		"jarvis.proposable_action.suppress", "jarvis.signal_automation.dismiss", "jarvis.signal_automation.execute",
		"make_phone_call.cancel_call", "make_phone_call.confirm_call", "make_phone_call.escalation_answer",
		"schedule.cancel_schedule",
	}
	if got := ce.m.ServerCallbackNames(); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	cases := []struct {
		command, callback string
		data              map[string]any
		status, errMsg    string
	}{
		{"errand", "discard_errand_plan", map[string]any{"plan_id": "missing"}, "completed", ""},
		{"errand", "approve_errand_plan", map[string]any{}, "failed", "Errands are unavailable right now."}, // no queue here
		{"errand", "stop_errand", map[string]any{"workflow_id": "missing"}, "completed", ""},
		{"make_phone_call", "confirm_call", map[string]any{}, "failed", "Missing session_id"},
		{"schedule", "cancel_schedule", map[string]any{}, "failed", "No schedule to cancel."},
		{"jarvis.proposable_action", "dismiss", map[string]any{}, "completed", ""},
		{"jarvis.signal_automation", "execute", map[string]any{}, "failed", "malformed automation action"},
	}
	for _, c := range cases {
		r := ce.do("POST", "/api/v0/callbacks", map[string]any{"command_name": c.command, "callback_name": c.callback,
			"data": c.data, "household_id": cbHH, "navigation_type": "stack"}, bearer(ce.member)).want(201).json()
		if r["status"] != "pending" {
			t.Fatal(r)
		}
		s := ce.pollStatus(r["id"].(string), ce.member)
		em, _ := s["error_message"].(string)
		if s["status"] != c.status || em != c.errMsg {
			t.Fatalf("%s.%s: %v", c.command, c.callback, s)
		}
	}
	// A server-plane row is invisible to every node.
	n := ce.onlineNode("n1", cbHH)
	var id string
	ce.d.Read.QueryRow(`SELECT id FROM cc_callback_jobs WHERE node_id IS NULL LIMIT 1`).Scan(&id)
	ce.do("GET", "/api/v0/callbacks/"+id, nil, n.h()).detail(404, "Callback job not found")
	ce.do("POST", "/api/v0/callbacks/"+id+"/result", map[string]any{"success": true}, n.h()).detail(404, "Callback job not found")
}

// TestServerCallbackRunner: the anti-vanishing rule (errors, panics and a vanished handler
// all record failed) and the new_notification fan-out from a server result.
func TestServerCallbackRunner(t *testing.T) {
	ce := newCBEnv(t)
	ctx := context.Background()
	m := ce.m
	m.cbOnce.Do(func() {})
	m.cbMap = map[string]serverCallbackFunc{
		"t.ok": func(_ context.Context, c callbackContext) (callbackResult, error) {
			if c.HouseholdID != cbHH || c.UserID == nil || *c.UserID != 5 || fmt.Sprint(c.Data["n"]) != "2" {
				return callbackResult{Error: "bad context"}, nil
			}
			return callbackResult{Success: true, ContextData: map[string]any{"inbox": map[string]any{"title": "Done",
				"category": "custom", "target_type": "household", "create_push_notification": true}}}, nil
		},
		"t.err": func(context.Context, callbackContext) (callbackResult, error) {
			return callbackResult{}, errors.New("boom")
		},
		"t.panic": func(context.Context, callbackContext) (callbackResult, error) { panic("kaput") },
	}
	uid := int64(5)
	run := func(cmd, cb string) *callbackJob {
		j, err := m.insertCallbackJob(ctx, newCallbackJob{command: cmd, callback: cb, dataJSON: `{"n": 2}`,
			nav: "new_notification", householdID: cbHH, userID: &uid})
		if err != nil {
			t.Fatal(err)
		}
		m.runServerCallback(ctx, j.id)
		got, _ := m.loadCallbackJob(ctx, ce.d.Read, j.id)
		return got
	}
	if j := run("t", "ok"); j.status != "completed" {
		t.Fatal(j.status, j.errMsg)
	}
	ce.notify.mu.Lock()
	it, pushes := ce.notify.items[0], len(ce.notify.pushes)
	ce.notify.mu.Unlock()
	if it.Title != "Done" || it.Category != "custom" || it.Metadata["node_id"] != nil || pushes != 1 {
		t.Fatalf("%+v %d", it, pushes)
	}
	if j := run("t", "err"); j.status != "failed" || j.errMsg.String != "boom" {
		t.Fatal(j.status, j.errMsg)
	}
	if j := run("t", "panic"); j.status != "failed" || j.errMsg.String != "kaput" {
		t.Fatal(j.status, j.errMsg)
	}
	if j := run("t", "gone"); j.status != "failed" || j.errMsg.String != "Server-side handler no longer registered" {
		t.Fatal(j.status, j.errMsg)
	}
	// A failed result never fans out.
	if len(ce.items()) != 1 {
		t.Fatal(ce.items())
	}
}

func TestCallbackSweeper(t *testing.T) {
	ce := newCBEnv(t)
	ctx := context.Background()
	n := ce.onlineNode("n1", cbHH)
	_, _ = ce.d.Write.Exec(`UPDATE cc_nodes SET room = 'Den' WHERE node_id = 'n1'`)
	tap := func(b map[string]any) string {
		b["command_name"], b["callback_name"] = "c", "cb"
		return ce.do("POST", "/api/v0/callbacks", b, bearer(ce.member)).want(201).json()["id"].(string)
	}
	nodeJob := tap(map[string]any{"target_node_id": n.id})
	stackJob := tap(map[string]any{"target_node_id": n.id, "navigation_type": "stack"})
	uid := int64(5)
	srv, _ := ce.m.insertCallbackJob(ctx, newCallbackJob{command: "x", callback: "y", dataJSON: "{}", nav: "new_notification",
		householdID: cbHH, userID: &uid})
	done := tap(map[string]any{"target_node_id": n.id, "navigation_type": "stack"})
	ce.do("POST", "/api/v0/callbacks/"+done+"/result", map[string]any{"success": false, "error": "nope"}, n.h()).want(200)

	ce.advance(6 * time.Minute)
	if err := ce.m.sweepCallbacks(ctx); err != nil {
		t.Fatal(err)
	}
	status := func(id string) string {
		var s string
		if err := ce.d.Read.QueryRow(`SELECT status FROM cc_callback_jobs WHERE id = ?`, id).Scan(&s); errors.Is(err, sql.ErrNoRows) {
			return "deleted"
		}
		return s
	}
	if status(nodeJob) != "expired" || status(stackJob) != "expired" || status(srv.id) != "pending" || status(done) != "failed" {
		t.Fatal(status(nodeJob), status(stackJob), status(srv.id), status(done))
	}
	if got := ce.items(); !slices.Equal(got, []string{"callback_result|Couldn't reach Den"}) {
		t.Fatal(got) // only the new_notification tap gets a card
	}
	// A server-plane job that lost its run is failed after the grace period.
	ce.advance(serverGrace)
	_ = ce.m.sweepCallbacks(ctx)
	if status(srv.id) != "failed" {
		t.Fatal(status(srv.id))
	}
	// Finished rows go after the retention period.
	ce.advance(callbackRetention)
	_ = ce.m.sweepCallbacks(ctx)
	for _, id := range []string{nodeJob, stackJob, srv.id, done} {
		if status(id) != "deleted" {
			t.Fatal(id, status(id))
		}
	}
}

// TestCallbacksOnTheQueue: with the platform queue, a server tap is a cc.callback.server job and
// the sweeper is a scheduler trigger (D27).
func TestCallbacksOnTheQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, scheduler.MigrationModule, scheduler.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "cc", Migrations()); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := queue.New(d, log)
	q.PollInterval = 20 * time.Millisecond
	sch := scheduler.New(d, q, log)
	fa := newFakeAuth()
	m := &Module{Auth: fa, Users: fa, Nodes: fa, AdminKey: testAdminKey, MQTT: MQTTOptions{Disabled: true}, Notify: &fakeNotifier{}}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{DB: d, Log: log, Queue: q, Scheduler: sch})
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if st, err := sch.Status(ctx, callbackSweepJob); err != nil || st.NextFireAt.IsZero() {
		t.Fatal(st, err)
	}
	srv := httptest.NewServer(httpx.Middleware(log, "command-center", mux))
	t.Cleanup(srv.Close)
	e := &env{t: t, m: m, auth: fa, srv: srv, d: d, now: time.Now()}
	tok := fa.addUser(5, cbHH, authn.RoleMember)
	id := e.do("POST", "/api/v0/callbacks", map[string]any{"command_name": "jarvis.proposable_action", "callback_name": "dismiss",
		"household_id": cbHH}, bearer(tok)).want(201).json()["id"].(string)
	var jobs int
	d.Read.QueryRow(`SELECT COUNT(*) FROM platform_jobs WHERE type = ? AND payload = ?`, serverCallbackJob, []byte(id)).Scan(&jobs)
	if jobs != 1 {
		t.Fatalf("server callback jobs %d", jobs)
	}
	q.Start(ctx)
	ce := &cbEnv{env: e}
	if s := ce.pollStatus(id, tok); s["status"] != "completed" {
		t.Fatal(s)
	}
}

func TestPurgeUserCallbacks(t *testing.T) {
	ce := newCBEnv(t)
	ctx := context.Background()
	other := int64(6)
	for _, uid := range []int64{5, 5, 6} {
		u := uid
		if _, err := ce.m.insertCallbackJob(ctx, newCallbackJob{command: "c", callback: "cb", dataJSON: `{"secret": 1}`,
			nav: "stack", householdID: cbHH, userID: &u}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ce.d.Tx(ctx, func(tx *sql.Tx) error { return ce.m.PurgeUser(ctx, tx, 5) }); err != nil {
		t.Fatal(err)
	}
	var mine, theirs int
	ce.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_callback_jobs WHERE user_id = 5`).Scan(&mine)
	ce.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_callback_jobs WHERE user_id = ?`, other).Scan(&theirs)
	if mine != 0 || theirs != 1 {
		t.Fatal(mine, theirs)
	}
}
