package cc

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/mochi-mqtt/server/v2/packets"
)

// --- fixtures ---

// appAuth accepts one app credential (the shared fakeAuth refuses every app).
type appAuth struct{ *fakeAuth }

func (a appAuth) ValidateApp(_ context.Context, id, key string) (authn.App, bool, error) {
	return authn.App{ID: id}, id == "app1" && key == "secret", nil
}

type sigEnv struct {
	*env
	notify *fakeNotifier
}

const sigHH = "hh-sig"

func newSigEnv(t *testing.T, configure ...func(m *Module)) *sigEnv {
	t.Helper()
	probeTimeout, resolveTimeout, dispatchTimeout, callbackTimeout, callbackPoll = time.Second, 2*time.Second, 2*time.Second, 2*time.Second, 20*time.Millisecond
	t.Cleanup(func() {
		probeTimeout, resolveTimeout, dispatchTimeout, callbackTimeout, callbackPoll = 4*time.Second, 6*time.Second, 20*time.Second, 25*time.Second, 500*time.Millisecond
	})
	n := &fakeNotifier{}
	fa := newFakeAuth()
	e := newEnv(t, envOpts{auth: fa, configure: func(m *Module) {
		m.Auth = appAuth{fa}
		m.Notify = n
		for _, c := range configure {
			c(m)
		}
	}})
	t.Cleanup(func() { e.m.sig.wg.Wait() })
	return &sigEnv{env: e, notify: n}
}

func (se *sigEnv) set(key string, v any, hh string) {
	se.t.Helper()
	if err := se.m.Settings().Set(context.Background(), key, v, settings.Scope{HouseholdID: hh}); err != nil {
		se.t.Fatal(err)
	}
}

func (se *sigEnv) items() []notifications.NewInboxItem {
	se.notify.mu.Lock()
	defer se.notify.mu.Unlock()
	return append([]notifications.NewInboxItem(nil), se.notify.items...)
}

func (se *sigEnv) wait() { se.m.sig.wg.Wait() }

func signal(kind, key string, extra map[string]any) map[string]any {
	s := map[string]any{"kind": kind, "source_key": key, "summary": "s"}
	for k, v := range extra {
		s[k] = v
	}
	return map[string]any{"signal": s}
}

// fakeNodeAgent answers report_tools, tool_call and callback commands like jarvis-node-setup.
type fakeNodeAgent struct {
	mu       sync.Mutex
	report   map[string]any
	tool     func(name string, args map[string]any) map[string]any
	calls    []string
	callback func(jobID string)
}

func (se *sigEnv) runNode(n testNode, a *fakeNodeAgent) {
	c := se.dialNode(n)
	c.subscribe("jarvis/nodes/" + n.id + "/commands")
	base := se.srv.URL + "/api/v0"
	post := func(path string, body any) {
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", base+path, bytes.NewReader(raw))
		req.Header.Set("X-API-Key", n.id+":"+n.key)
		req.Header.Set("Content-Type", "application/json")
		if res, err := http.DefaultClient.Do(req); err == nil {
			res.Body.Close()
		}
	}
	go func() {
		for pk := range c.in {
			if pk.FixedHeader.Type != packets.Publish {
				continue
			}
			var arr []struct {
				Command string         `json:"command"`
				Details map[string]any `json:"details"`
			}
			if json.Unmarshal(pk.Payload, &arr) != nil || len(arr) != 1 {
				continue
			}
			d := arr[0].Details
			reply, _ := d["reply_request_id"].(string)
			a.mu.Lock()
			a.calls = append(a.calls, arr[0].Command)
			a.mu.Unlock()
			switch arr[0].Command {
			case "report_tools":
				post("/mobile/node-tool-reports/"+reply, a.report)
			case "tool_call":
				name, _ := d["command_name"].(string)
				args, _ := d["arguments"].(map[string]any)
				a.mu.Lock()
				a.calls = append(a.calls, "tool:"+name)
				a.mu.Unlock()
				post("/device-control-results/"+reply, map[string]any{"output": a.tool(name, args)})
			case "callback":
				if a.callback != nil {
					a.callback(d["request_id"].(string))
				}
			}
		}
	}()
}

func (a *fakeNodeAgent) called(s string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c == s {
			return true
		}
	}
	return false
}

var reminderAction = map[string]any{"callback": "set_at", "card_title": "Set a reminder?",
	"params": []any{map[string]any{"name": "text", "required": true}, map[string]any{"name": "due_at_iso", "required": true},
		map[string]any{"name": "idempotency_key", "required": true}}, "idempotency_param": "idempotency_key"}

func nodeReport() map[string]any {
	return map[string]any{
		"client_tools": []any{map[string]any{"type": "function", "command_name": "lock_door", "function": map[string]any{
			"name": "lock_door", "description": "Lock a door", "parameters": map[string]any{"type": "object",
				"properties": map[string]any{"door": map[string]any{"type": "string", "_refinable": true}}}}}},
		"available_commands": []any{
			map[string]any{"command_name": "get_drive_time"},
			map[string]any{"command_name": "lock_door"},
			map[string]any{"command_name": "reminder", "proposable_actions": []any{reminderAction}},
			map[string]any{"command_name": "add_event", "proposable_actions": []any{map[string]any{"callback": "create",
				"card_title": "Add to calendar?", "params": []any{map[string]any{"name": "title", "required": true},
					map[string]any{"name": "kind", "enum_values": []any{"a", "b"}}}}}},
		},
	}
}

// --- golden ---

func TestStableIdempotencyKeyGolden(t *testing.T) {
	// Expected values from CPython's json.dumps(sort_keys=True, default=str) + sha256.
	v, err := pyjson.Loads(`{"title":"Dentist é","when":3,"x":1.5,"z":{"b":true,"a":null}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := stableIdempotencyKey(map[string]any{"items": []any{v}}, "add_event", "create"); got != "match:5befeb9ba9c3f91e" {
		t.Fatal(got)
	}
	if got := stableIdempotencyKey(map[string]any{"items": []any{pyjson.NewObject()}}, "cmd", "cmd"); got != "match:815747c19d913830" {
		t.Fatal(got)
	}
}

func TestCacheableRuleAndRender(t *testing.T) {
	s := func(x string) *string { return &x }
	for _, c := range []struct {
		summary string
		bad     bool
	}{{"Alex is home", false}, {"temp 21.5", true}, {"at 10:15:02", true}, {"5 minutes ago", true}, {"in 5 min", true}, {"at 10:15", false}} {
		if got := rejectNonQuantized(s(c.summary), "") != nil; got != c.bad {
			t.Errorf("%q: %v", c.summary, got)
		}
	}
	got := renderSignalBlock([]liveSignal{{"presence.seen", "user:2", " Sam is home "}, {"game.final", "", "Jets won"},
		{"presence.seen", "user:1", "Alex is home"}, {"x", "", "  "}})
	if got != "Jets won\nAlex is home\nSam is home" {
		t.Fatalf("%q", got)
	}
}

func TestQuietHours(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 30, 0, time.UTC) }
	for _, c := range []struct {
		spec string
		t    time.Time
		want bool
	}{{"22:00-07:00", at(23, 0), true}, {"22:00-07:00", at(6, 59), true}, {"22:00-07:00", at(7, 0), false},
		{"22:00-07:00", at(12, 0), false}, {"09:00-17:00", at(9, 0), true}, {"09:00-17:00", at(17, 0), false}, {"garbage", at(23, 0), false}} {
		if got := inQuietHours(c.spec, c.t); got != c.want {
			t.Errorf("%s %v: %v", c.spec, c.t, got)
		}
	}
}

// --- POST /signals ---

func TestSignalIngest(t *testing.T) {
	se := newSigEnv(t)
	n := se.createNode("n1", sigHH)
	foreign := se.createNode("nx", "hh-other")
	se.auth.addUser(5, sigHH, authn.RoleMember)
	se.auth.addUser(9, "hh-other", authn.RoleMember)
	const p = "/api/v0/signals"

	se.do("POST", p, signal("k", "a", nil), nil).detail(401, "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)")
	se.do("POST", p, signal("k", "a", nil), hdr{"X-API-Key": "n1:bad"}).detail(401, "Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)")
	app := hdr{"X-Jarvis-App-Id": "app1", "X-Jarvis-App-Key": "secret"}
	se.do("POST", p, signal("k", "a", nil), hdr{"X-Jarvis-App-Id": "app1", "X-Jarvis-App-Key": "no"}).detail(401, "Invalid app credentials")
	se.do("POST", p, signal("k", "a", nil), app).detail(400, "household_id required (provide in body or use node auth)")

	// Validation is CC's 400 shape.
	r := se.do("POST", p, map[string]any{"signal": map[string]any{"kind": strings.Repeat("x", 256), "ttl_seconds": 0}}, n.h()).want(400).json()
	det := r["details"].([]any)
	if len(det) != 3 || det[0] != "body -> signal -> kind: String should have at most 255 characters" ||
		det[1] != "body -> signal -> source_key: Field required" || det[2] != "body -> signal -> ttl_seconds: Input should be greater than 0" {
		t.Fatal(det)
	}

	body := signal("game.final", "sports:1", map[string]any{"ttl_seconds": 60, "scope": map[string]any{"user_id": 9, "node_id": "nx", "room": "den"}})
	body["data"] = map[string]any{"score": "24-17", "team": "Jets"}
	body["household_id"] = "hh-other"
	se.do("POST", p, body, n.h()).detail(403, "household_id does not match node's household")
	delete(body, "household_id")
	r = se.do("POST", p, body, n.h()).want(200).json()
	if r["mode"] != "open" || r["proposed"] != false {
		t.Fatal(r)
	}
	id := r["signal_id"]
	// Foreign node and user are never stored (security divergence); the auth node is used.
	var nodeID, facts string
	var uid any
	if err := se.d.Read.QueryRow(`SELECT node_id, user_id, facts FROM cc_signals WHERE id = ?`, id).Scan(&nodeID, &uid, &facts); err != nil {
		t.Fatal(err)
	}
	if nodeID != "n1" || uid != nil || facts != `{"score": "24-17", "team": "Jets"}` {
		t.Fatal(nodeID, uid, facts)
	}
	_ = foreign
	// Upsert: same id, re-emit without TTL never expires.
	r = se.do("POST", p, signal("game.final", "sports:1", nil), n.h()).want(200).json()
	if r["signal_id"] != id {
		t.Fatal(r)
	}
	if got := se.m.SignalContext(context.Background(), sigHH); got != "s" {
		t.Fatalf("%q", got)
	}

	// App auth names a household.
	ab := signal("k", "app:1", map[string]any{"scope": map[string]any{"user_id": 5}})
	ab["household_id"] = sigHH
	se.do("POST", p, ab, app).want(200)

	// Cacheable rule.
	se.do("POST", p, signal("k", "c", map[string]any{"cacheable": true, "summary": "21.5 degrees"}), n.h()).detail(422,
		"cacheable Signal must be quantized/absolute — it may not contain a live float, a seconds-precision time, or a relative-time phrase")

	// TTL sweep.
	se.do("POST", p, signal("k", "ttl", map[string]any{"ttl_seconds": 30}), n.h()).want(200)
	se.advance(time.Minute)
	if err := se.m.cleanupSignals(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	se.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_signals WHERE source_key = 'ttl'`).Scan(&count)
	if count != 0 {
		t.Fatal("expired signal kept")
	}

	// Kill switch (fail open by default) and the rate limit (keyed on the principal).
	se.set(settingSignalsEnabled, false, sigHH)
	se.do("POST", p, signal("k", "x", nil), n.h()).detail(409, "Signal bus is disabled for this household")
	se.set(settingSignalsEnabled, true, sigHH)
	var last *resp
	for i := 0; i < 61; i++ {
		last = se.do("POST", p, signal("k", "x", map[string]any{"source_agent": strings.Repeat("v", i%3+1)}), n.h())
	}
	last.detail(429, "Rate limit exceeded")
	if last.header.Get("Retry-After") != "1" {
		t.Fatal("Retry-After")
	}
}

// --- presence, automations ---

func TestMobilePresenceAndVoicePresence(t *testing.T) {
	se := newSigEnv(t)
	tok := se.auth.addUser(5, sigHH, authn.RoleMember)
	se.auth.addUser(6, "hh-other", authn.RoleMember)
	const p = "/api/v0/mobile/presence"
	se.do("POST", p, map[string]any{"household_id": sigHH, "state": "gone"}, bearer(tok)).detail(422, "state must be one of ['away', 'home']")
	se.do("POST", p, map[string]any{"household_id": "hh-other"}, bearer(tok)).detail(403, "User is not a member of this household")
	r := se.do("POST", p, map[string]any{"household_id": sigHH, "room": "kitchen", "name": " Alex "}, bearer(tok)).want(200).json()
	if r["ok"] != true || r["kind"] != "presence.seen" {
		t.Fatal(r)
	}
	if got := se.m.SignalContext(context.Background(), sigHH); got != "Alex is home in the kitchen" {
		t.Fatalf("%q", got)
	}
	// Voice: off unless ambient_context; never shortens the phone's 4 h row.
	ctx := context.Background()
	se.m.noteVoicePresence(ctx, sigHH, 5, "n1", "Alex")
	se.set(settingAmbientContext, true, sigHH)
	se.m.noteVoicePresence(ctx, sigHH, 5, "n1", "Alex")
	if got := se.m.SignalContext(ctx, sigHH); got != "Alex is home in the kitchen" {
		t.Fatalf("voice shortened the phone row: %q", got)
	}
	se.do("POST", p, map[string]any{"household_id": sigHH, "state": "away"}, bearer(tok)).want(200)
	se.m.noteVoicePresence(ctx, sigHH, 5, "n1", "Alex")
	if got := se.m.SignalContext(ctx, sigHH); got != "Alex was recently heard at the n1 node" {
		t.Fatalf("%q", got)
	}
	se.set(settingSignalsEnabled, false, sigHH) // D40 Q7: voice honours signals.enabled
	se.do("POST", p, map[string]any{"household_id": sigHH}, bearer(tok)).detail(409, "Signal bus is disabled for this household")
}

func TestSignalAutomationsRoutes(t *testing.T) {
	se := newSigEnv(t)
	member := se.auth.addUser(5, sigHH, authn.RoleMember)
	admin := se.auth.addUser(6, sigHH, authn.RoleAdmin)
	base := "/api/v0/mobile/household/" + sigHH + "/signal-automations"
	r := se.do("GET", base, nil, bearer(member)).want(200).json()
	autos := r["automations"].([]any)
	if len(autos) != 3 || autos[0].(map[string]any)["kind"] != "presence.left" || autos[0].(map[string]any)["observed"] != false ||
		autos[0].(map[string]any)["delivery"] != "notification" || autos[0].(map[string]any)["instruction"] != "" {
		t.Fatal(autos)
	}
	if !strings.Contains(string(se.do("GET", base, nil, bearer(member)).body), `"facts":{"title":"Event title","location"`) {
		t.Fatal("facts order")
	}
	se.do("PUT", base+"/game.final", map[string]any{"instruction": "x"}, bearer(admin)).detail(404, "Unknown or non-authorable signal kind: game.final")
	se.do("PUT", base+"/presence.left", map[string]any{"instruction": "x"}, bearer(member)).detail(403, "User has member role, requires admin or higher")
	r = se.do("PUT", base+"/presence.left", map[string]any{"delivery": "loud"}, bearer(admin)).want(400).json()
	if r["details"].([]any)[0] != "body -> delivery: Input should be 'automatic' or 'notification'" {
		t.Fatal(r)
	}
	r = se.do("PUT", base+"/presence.left", map[string]any{"instruction": " Lock the door ", "delivery": "automatic"}, bearer(admin)).want(200).json()
	if r["instruction"] != "Lock the door" || r["enabled"] != true || r["cleared"] != false {
		t.Fatal(r)
	}
	if rule := se.m.enabledRule(context.Background(), sigHH, "presence.left"); rule == nil || rule.Delivery != "automatic" {
		t.Fatal(rule)
	}
	r = se.do("PUT", base+"/presence.left", map[string]any{"instruction": "  "}, bearer(admin)).want(200).json()
	if r["cleared"] != true || r["enabled"] != false || se.m.enabledRule(context.Background(), sigHH, "presence.left") != nil {
		t.Fatal(r)
	}
}

// --- reactions over a fake node ---

func TestAutomationNotificationCardAndConfirm(t *testing.T) {
	var picked []map[string]any
	var mu sync.Mutex
	se := newSigEnv(t, func(m *Module) {
		m.LLM = fakeLLMFunc(func(req map[string]any) (string, string) {
			mu.Lock()
			picked = append(picked, req)
			mu.Unlock()
			return "lock_door", `{"door": "front"}`
		})
	})
	n := se.createNode("n1", sigHH)
	agent := &fakeNodeAgent{report: nodeReport(), tool: func(name string, args map[string]any) map[string]any {
		return map[string]any{"success": args["door"] == "front"}
	}}
	se.runNode(n, agent)
	tok := se.auth.addUser(5, sigHH, authn.RoleMember)
	admin := se.auth.addUser(6, sigHH, authn.RoleAdmin)
	se.do("PUT", "/api/v0/mobile/household/"+sigHH+"/signal-automations/presence.left",
		map[string]any{"instruction": "Lock the front door"}, bearer(admin)).want(200)

	se.do("POST", "/api/v0/mobile/presence", map[string]any{"household_id": sigHH, "state": "away"}, bearer(tok)).want(200)
	se.wait()
	items := se.items()
	if len(items) != 1 || items[0].Title != "Confirm automation" || items[0].Summary != "I leave home: Lock the front door" ||
		items[0].Category != "proposal" || *items[0].UserID != 5 {
		t.Fatal(items)
	}
	// The card carries only an opaque id (D7).
	els := items[0].Metadata["interactive_elements"].([]any)
	data := els[0].(map[string]any)["data"].(map[string]any)["_action"].(map[string]any)
	if _, leaked := data["arguments"]; leaked || data["action_id"] == nil {
		t.Fatal(data)
	}
	mu.Lock()
	req := picked[0]
	mu.Unlock()
	if !strings.Contains(req["user"].(string), `Event details: {"user_id": 5, "state": "away", "room": null}`) ||
		strings.Contains(req["tools"].(string), "_refinable") || strings.Contains(req["tools"].(string), "command_name") {
		t.Fatal(req)
	}
	// Heartbeat re-assert: unchanged (no second card). Arrival resets.
	se.do("POST", "/api/v0/mobile/presence", map[string]any{"household_id": sigHH, "state": "away"}, bearer(tok)).want(200)
	se.wait()
	if len(se.items()) != 1 {
		t.Fatal("re-assert acted")
	}

	h, _ := se.m.SignalCallback(automationCommand, "execute")
	ctx := context.Background()
	uid := int64(5)
	// Another household cannot run it.
	if res := h(ctx, SignalCallbackContext{HouseholdID: "hh-other", UserID: &uid, Data: map[string]any{"_action": data}}); res.Success {
		t.Fatal("cross-household confirm ran")
	}
	res := h(ctx, SignalCallbackContext{HouseholdID: sigHH, UserID: &uid, Data: map[string]any{"_action": data}})
	if !res.Success || res.ContextData["inbox"].(map[string]any)["summary"] != "Ran lock_door" || !agent.called("tool:lock_door") {
		t.Fatal(res)
	}
	res = h(ctx, SignalCallbackContext{HouseholdID: sigHH, UserID: &uid, Data: map[string]any{"_action": data}})
	if res.ContextData["inbox"].(map[string]any)["title"] != "Already done" {
		t.Fatal("double tap ran twice")
	}
}

func TestLeaveByReactionAndSuppression(t *testing.T) {
	se := newSigEnv(t)
	n := se.createNode("n1", sigHH)
	se.auth.addUser(5, sigHH, authn.RoleMember)
	agent := &fakeNodeAgent{report: nodeReport(), tool: func(name string, _ map[string]any) map[string]any {
		return map[string]any{"success": true, "duration_minutes": 25, "destination": "1 Main St"}
	}}
	se.runNode(n, agent)
	appt := func() *resp {
		b := signal("appt.upcoming", "cal:e1", map[string]any{"scope": map[string]any{"user_id": 5}})
		b["data"] = map[string]any{"event_id": "e1", "title": "Dentist", "location": "Dentist on Main",
			"start_iso": "2026-10-06T15:00:00Z", "start_display": "3:00 PM"}
		return se.do("POST", "/api/v0/signals", b, n.h()).want(200)
	}
	appt()
	se.wait()
	if len(se.items()) != 0 {
		t.Fatal("proposals.enabled is off: no card")
	}
	se.set(settingProposalsEnabled, true, sigHH)
	appt()
	se.wait()
	items := se.items()
	if len(items) != 1 || items[0].Title != "Set a reminder?" || items[0].Summary != "Dentist at 3:00 PM — 25 min drive from home" {
		t.Fatal(items)
	}
	els := items[0].Metadata["interactive_elements"].([]any)
	confirm := els[0].(map[string]any)
	cd := confirm["data"].(map[string]any)
	if confirm["id"] != "confirm-leaveby:e1" || cd["due_at_iso"] != "2026-10-06T14:30:00+00:00" ||
		cd["idempotency_key"] != "leaveby:e1" || cd["text"] != "Leave for Dentist" || len(els) != 3 {
		t.Fatal(confirm)
	}
	// Re-emit: claimed (terminal), no second card.
	appt()
	se.wait()
	if len(se.items()) != 1 {
		t.Fatal("duplicate leave-by card")
	}
	// Suppress via the card; the next event is silenced centrally.
	sup := els[2].(map[string]any)
	h, _ := se.m.SignalCallback(proposableCommand, "suppress")
	uid := int64(5)
	if res := h(context.Background(), SignalCallbackContext{HouseholdID: sigHH, UserID: &uid, Data: sup["data"].(map[string]any)}); !res.Success {
		t.Fatal(res)
	}
	b := signal("appt.upcoming", "cal:e2", map[string]any{"scope": map[string]any{"user_id": 5}})
	b["data"] = map[string]any{"event_id": "e2", "title": "X", "location": "Y", "start_iso": "2026-10-06T16:00:00Z"}
	se.do("POST", "/api/v0/signals", b, n.h()).want(200)
	se.wait()
	if len(se.items()) != 1 {
		t.Fatal("suppressed leave-by still carded")
	}
	// Node and mobile suppression routes.
	r := se.do("GET", "/api/v0/proposals/suppressions?command=reminder&user_id=5", nil, n.h()).want(200).json()
	if r["source_keys"].([]any)[0] != "leaveby" || r["descriptors"].([]any)[0] != "leave-by reminders for calendar events" {
		t.Fatal(r)
	}
	se.do("GET", "/api/v0/proposals/suppressions?command=reminder", nil, n.h()).want(400)
	tok := "tok-5"
	lst := se.do("GET", "/api/v0/mobile/proposal-suppressions?household_id="+sigHH, nil, bearer(tok)).want(200).json()["suppressions"].([]any)
	if len(lst) != 1 {
		t.Fatal(lst)
	}
	sid := lst[0].(map[string]any)["id"].(string)
	other := se.auth.addUser(7, sigHH, authn.RoleMember)
	se.do("DELETE", "/api/v0/mobile/proposal-suppressions/"+sid+"?household_id="+sigHH, nil, bearer(other)).detail(404, "Suppression not found")
	se.do("DELETE", "/api/v0/mobile/proposal-suppressions/"+sid+"?household_id="+sigHH, nil, bearer(tok)).want(200)
}

func TestDirectedProposalAndDispatcher(t *testing.T) {
	se := newSigEnv(t)
	n := se.createNode("n1", sigHH)
	se.createNode("nx", "hh-other")
	agent := &fakeNodeAgent{report: nodeReport()}
	agent.callback = func(jobID string) {
		_, _ = se.d.Write.Exec(`UPDATE cc_callback_jobs SET status = 'completed', result_context_data_json = '{"message": "Added"}' WHERE id = ?`, jobID)
	}
	se.runNode(n, agent)
	b := signal("email.appt", "mail:1", nil)
	b["command"] = "add_event.create"
	b["data"] = map[string]any{"title": "Dentist", "kind": "a", "extra": 1}
	r := se.do("POST", "/api/v0/signals", b, n.h()).want(200).json()
	if r["mode"] != "directed" || r["proposed"] != true {
		t.Fatal(r)
	}
	b["command"] = "nope"
	if r := se.do("POST", "/api/v0/signals", b, n.h()).want(200).json(); r["proposed"] != false {
		t.Fatal(r)
	}
	items := se.items()
	el := items[0].Metadata["interactive_elements"].([]any)[0].(map[string]any)
	data := el["data"].(map[string]any)
	if data["title"] != "Dentist" || data["extra"] != nil || items[0].UserID != nil {
		t.Fatal(data)
	}
	exec, _ := se.m.SignalCallback(proposableCommand, "execute")
	ctx := context.Background()
	if res := exec(ctx, SignalCallbackContext{HouseholdID: sigHH, Data: data}); res.Error != "proposals disabled" {
		t.Fatal(res)
	}
	se.set(settingProposalsEnabled, true, sigHH)
	// D4: a node of another household is refused.
	forged := map[string]any{"_action": map[string]any{"target_command": "add_event", "target_callback": "create", "node_id": "nx"}, "title": "x"}
	if res := exec(ctx, SignalCallbackContext{HouseholdID: sigHH, Data: forged}); res.Error != "no node" {
		t.Fatal(res)
	}
	bad := map[string]any{"_action": data["_action"], "title": "Dentist", "kind": "c"}
	if res := exec(ctx, SignalCallbackContext{HouseholdID: sigHH, Data: bad}); !strings.HasPrefix(res.Error, "invalid params: 'kind' must be one of ['a', 'b']") {
		t.Fatal(res)
	}
	res := exec(ctx, SignalCallbackContext{HouseholdID: sigHH, Data: data})
	if !res.Success || res.ContextData["inbox"].(map[string]any)["title"] != "Added" {
		t.Fatal(res)
	}
	if res := exec(ctx, SignalCallbackContext{HouseholdID: sigHH, Data: data}); res.ContextData["inbox"].(map[string]any)["title"] != "Already done" {
		t.Fatal(res)
	}
}

func TestReactionJobPayload(t *testing.T) {
	se := newSigEnv(t)
	payload, _ := json.Marshal(reactionJob{Reaction: "automation", Ctx: reactionCtx{HouseholdID: sigHH, Kind: "presence.left", Facts: "{}"}})
	out, err := se.m.runReactionJob(context.Background(), queue.Job{Payload: payload})
	if err != nil || string(out) != "no_rule" {
		t.Fatal(string(out), err)
	}
}

// --- attention ---

func TestAttentionBrokerGates(t *testing.T) {
	se := newSigEnv(t)
	n := se.createNode("n1", sigHH)
	ctx := context.Background()
	push := func(title string, extra map[string]any) map[string]any {
		b := map[string]any{"title": title, "body": "b"}
		for k, v := range extra {
			b[k] = v
		}
		return se.do("POST", "/api/v0/node/push-notification", b, n.h()).want(200).json()
	}
	// Off: legacy, nothing journaled.
	push("Hello", nil)
	var count int
	se.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_attention_events`).Scan(&count)
	if count != 0 {
		t.Fatal("journaled while off")
	}
	se.set(settingAttention, true, sigHH)
	se.set(settingAttnQuietHours, "", sigHH)
	if r := push("Hello", nil); r["sent"] != true {
		t.Fatal(r)
	}
	if r := push("Hello", nil); r["sent"] != false || r["withheld_by"] != "dedupe" {
		t.Fatal(r)
	}
	// Safety category without an explicit key is never deduped (Keppra rule).
	for i := 0; i < 2; i++ {
		if r := push("Take Keppra", map[string]any{"category": "medication"}); r["sent"] != true {
			t.Fatal(r)
		}
	}
	// Source cap 4/day.
	for i := 0; i < 3; i++ {
		push("alert "+string(rune('a'+i)), nil)
	}
	if r := push("alert z", nil); r["withheld_by"] != "budget" {
		t.Fatal(r)
	}
	// Quiet hours demote push to inbox (household timezone).
	se.set(settingAttnQuietHours, "11:00-13:00", sigHH)
	before := len(se.notify.pushes)
	if r := push("q1", map[string]any{"category": "other"}); r["sent"] != true {
		t.Fatal(r)
	}
	if len(se.notify.pushes) != before {
		t.Fatal("pushed during quiet hours")
	}
	// Journal card.
	summary, body, ok, err := se.m.composeJournal(ctx, sigHH)
	if err != nil || !ok || summary != "Delivered 7, withheld 2 in the last 24h" ||
		!strings.Contains(body, "**Withheld (2)** — 1 dedupe, 1 budget") || !strings.Contains(body, "- alert z — alert (budget: source cap 4/day reached)") {
		t.Fatal(summary, body, err)
	}
	if id, err := se.m.postJournalCard(ctx, sigHH); err != nil || id == "" {
		t.Fatal(err)
	}
	// TTL cleanup cascades to deliveries.
	se.advance(31 * 24 * time.Hour)
	if err := se.m.cleanupAttention(ctx); err != nil {
		t.Fatal(err)
	}
	se.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_attention_deliveries`).Scan(&count)
	if count != 0 {
		t.Fatal("deliveries survived cleanup")
	}
}

func TestPurgeSignals(t *testing.T) {
	se := newSigEnv(t)
	tok := se.auth.addUser(5, sigHH, authn.RoleMember)
	se.do("POST", "/api/v0/mobile/presence", map[string]any{"household_id": sigHH}, bearer(tok)).want(200)
	ctx := context.Background()
	uid := int64(5)
	if _, err := se.m.recordSuppression(ctx, sigHH, &uid, "reminder", "leaveby", ""); err != nil {
		t.Fatal(err)
	}
	tx, _ := se.d.Write.Begin()
	if err := se.m.PurgeUser(ctx, tx, 5); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	se.d.Read.QueryRow(`SELECT (SELECT COUNT(*) FROM cc_signals) + (SELECT COUNT(*) FROM cc_proposal_suppressions)`).Scan(&n)
	if n != 0 {
		t.Fatal("purge left rows")
	}
}
