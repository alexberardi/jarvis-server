package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	notifmod "github.com/alexberardi/jarvis-server/internal/modules/notifications"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

const tierCCAdminKey = "cc-admin-key"

// memberStack runs the real auth, cc and notifications modules behind the admin gateway, the
// way cmd/jarvisd wires them (webapp 01 §5 P1). It returns the admin and auth base URLs.
func memberStack(t *testing.T) (admin, auth, cc string, notif *notifmod.Module, home string) {
	t.Helper()
	return memberStackWith(t, nil)
}

// memberStackWith is memberStack with cc configured further (a fake LLM and node publisher).
func memberStackWith(t *testing.T, configure func(c *ccmod.Module)) (admin, auth, cc string, notif *notifmod.Module, home string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{}
	for n := range pconfig.DefaultPorts {
		ports[n] = 0
	}
	cfg := pconfig.Config{Home: t.TempDir(), Host: "127.0.0.1", Ports: ports}
	a := &authmod.Module{RateLimit: authmod.RateLimit{Disabled: true}}
	c := &ccmod.Module{Auth: a, Users: a, Nodes: a, AdminKey: tierCCAdminKey, MQTT: ccmod.MQTTOptions{Disabled: true}}
	if configure != nil {
		configure(c)
	}
	n := &notifmod.Module{Auth: a, Users: a, CleanupInterval: -1}
	c.Notify = n
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Modules: []module.Module{&Module{UI: builtUI(), Verify: a.VerifyUser, Accounts: a,
			SettingsSources: []SettingsSource{a}}, a, c, n},
	}
	ready := make(chan struct{})
	r.OnReady = func() { close(ready) }
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		d.Close()
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("runner stopped: %v", err)
	case <-time.After(30 * time.Second): // hang guard
		t.Fatal("the stack never became ready")
	}
	return "http://" + r.Addr(pconfig.ListenerAdmin), "http://" + r.Addr(pconfig.ListenerAuth),
		"http://" + r.Addr(pconfig.ListenerCC), n, cfg.Home
}

func obj(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("not an object: %v", v)
	}
	return m
}

func bearer(tok string) []string { return []string{"Authorization", "Bearer " + tok} }

// login returns the access token and user id.
func login(t *testing.T, base, email string) (string, int64) {
	t.Helper()
	code, out := call(t, "POST", base+"/api/auth/login", map[string]any{"email": email, "password": "password1"})
	if code != 200 {
		t.Fatalf("login %s: %d %v", email, code, out)
	}
	o := obj(t, out)
	return o["access_token"].(string), int64(obj(t, o["user"])["id"].(float64))
}

// openRegistration turns auth.open_registration on as the superuser, so the test can sign up
// plain accounts and place them with /households/{id}/members.
func openRegistration(t *testing.T, authURL, root string) {
	t.Helper()
	if code, out := call(t, "PUT", authURL+"/settings/auth.open_registration", map[string]any{"value": true}, bearer(root)...); code != 200 {
		t.Fatalf("open registration: %d %v", code, out)
	}
}

func TestMemberTiersEndToEnd(t *testing.T) {
	// No live model names the default built-in prompt here; the prompt routes need one.
	base, authURL, ccURL, notif, home := memberStackWith(t, func(c *ccmod.Module) {
		c.DefaultPromptProvider = func(context.Context) string { return prompts.Qwen3_8B }
	})

	// The superuser, through the gateway with the setup token (AD2).
	tok, err := os.ReadFile(authmod.SetupTokenPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, "POST", base+"/api/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		authmod.SetupTokenHeader, strings.TrimSpace(string(tok))); code != 201 {
		t.Fatalf("setup: %d %v", code, out)
	}
	root, _ := login(t, base, "root@example.com")

	// Two households, created by the superuser (who is their admin), and five members.
	mkHousehold := func(name string) string {
		code, out := call(t, "POST", authURL+"/households", map[string]any{"name": name}, bearer(root)...)
		if code != 201 {
			t.Fatalf("household %s: %d %v", name, code, out)
		}
		return obj(t, out)["id"].(string)
	}
	h1, h2 := mkHousehold("H1"), mkHousehold("H2")
	openRegistration(t, authURL, root)
	users := map[string]string{}
	for _, u := range []struct{ email, hh, role string }{
		{"m1@example.com", h1, "member"}, {"p1@example.com", h1, "power_user"},
		{"a1@example.com", h1, "admin"}, {"m2@example.com", h2, "member"},
	} {
		code, out := call(t, "POST", authURL+"/auth/register", map[string]any{"email": u.email, "password": "password1"})
		if code != 201 && code != 200 {
			t.Fatalf("register %s: %d %v", u.email, code, out)
		}
		id := obj(t, obj(t, out)["user"])["id"]
		if code, out := call(t, "POST", authURL+"/households/"+u.hh+"/members", map[string]any{"user_id": id, "role": u.role}, bearer(root)...); code != 201 && code != 200 {
			t.Fatalf("add %s: %d %v", u.email, code, out)
		}
		users[u.email], _ = login(t, base, u.email)
	}
	m1, m2 := users["m1@example.com"], users["m2@example.com"]
	_, m1ID := login(t, base, "m1@example.com")

	// One node per household.
	for _, n := range []struct{ id, hh string }{{"node-h1", h1}, {"node-h2", h2}} {
		if code, out := call(t, "POST", ccURL+"/api/v0/admin/nodes", map[string]any{"node_id": n.id, "household_id": n.hh, "room": "kitchen"},
			"X-API-Key", tierCCAdminKey); code != 201 && code != 200 {
			t.Fatalf("node %s: %d %v", n.id, code, out)
		}
	}

	testDeviceMemberRoutes(t, base, h1, h2, root, users)

	// Inbox cards: a callback job only on the caller's household's node, and its status only to
	// the job's household (cc D5).
	cb := map[string]any{"command_name": "make_phone_call", "callback_name": "confirm_call", "data": map[string]any{}}
	cb["target_node_id"] = "node-h2"
	if code, out := call(t, "POST", base+"/api/cc/api/v0/callbacks", cb, bearer(m1)...); code != 403 {
		t.Fatalf("callback on an H2 node as an H1 member: %d %v", code, out)
	}
	cb["target_node_id"] = "node-h1"
	cbCode, cbOut := call(t, "POST", base+"/api/cc/api/v0/callbacks", cb, bearer(m1)...)
	if cbCode != 201 {
		t.Fatalf("callback on the member's node: %d %v", cbCode, cbOut)
	}
	job := fmt.Sprint(obj(t, cbOut)["id"])
	if code, out := call(t, "GET", base+"/api/cc/api/v0/callbacks/"+job+"/status", nil, bearer(m1)...); code != 200 {
		t.Fatalf("callback status: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/callbacks/"+job+"/status", nil, bearer(m2)...); code != 403 && code != 404 {
		t.Fatalf("another household's callback status: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/proposal-suppressions?household_id="+h1, nil, bearer(m1)...); code != 200 {
		t.Fatalf("own proposal suppressions: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/proposal-suppressions?household_id="+h2, nil, bearer(m1)...); code != 403 {
		t.Fatalf("another household's proposal suppressions: %d %v", code, out)
	}
	// Scheduled errands: the member's household lists; another household's lists and cancels
	// are refused.
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/household/"+h1+"/schedules", nil, bearer(m1)...); code != 200 {
		t.Fatalf("own scheduled errands: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/household/"+h2+"/schedules", nil, bearer(m1)...); code != 403 {
		t.Fatalf("another household's scheduled errands: %d %v", code, out)
	}
	if code, out := call(t, "POST", base+"/api/cc/api/v0/mobile/household/"+h2+"/schedules/s1/cancel", map[string]any{}, bearer(m1)...); code != 403 {
		t.Fatalf("cancel another household's errand: %d %v", code, out)
	}
	// Routines: the member's household lists and creates; another household's list, create,
	// read, edit, delete and run-now are refused.
	rt := base + "/api/cc/api/v0/households/"
	if code, out := call(t, "GET", rt+h1+"/routines", nil, bearer(m1)...); code != 200 {
		t.Fatalf("own routines: %d %v", code, out)
	}
	newRoutine := map[string]any{"name": "Tier check", "trigger_phrases": []any{"tier check"},
		"steps":                []any{map[string]any{"command": "get_weather", "args": []any{}, "label": "get_weather"}},
		"response_instruction": "", "response_length": "short", "schedule": nil}
	if code, out := call(t, "POST", rt+h1+"/routines", newRoutine, bearer(m1)...); code != 200 && code != 201 {
		t.Fatalf("create own routine: %d %v", code, out)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/routines"}, {"POST", "/routines"}, {"GET", "/routines/r1"}, {"PATCH", "/routines/r1"},
		{"DELETE", "/routines/r1"}, {"POST", "/routines/r1/run-now"},
	} {
		var body any
		if c.method == "POST" || c.method == "PATCH" {
			body = newRoutine
		}
		if code, out := call(t, c.method, rt+h2+c.path, body, bearer(m1)...); code != 403 {
			t.Fatalf("%s another household's %s: %d %v", c.method, c.path, code, out)
		}
	}
	// A household's people: a member lists members and invites but can't invite, rename, change a
	// role or remove anyone; a power user invites; an admin renames; another household's are refused.
	p1, a1 := users["p1@example.com"], users["a1@example.com"]
	hp := base + "/api/households/"
	refused := func(code int) bool { return code == 403 || code == 404 }
	for _, path := range []string{"/members", "/invites"} {
		if code, out := call(t, "GET", hp+h1+path, nil, bearer(m1)...); code != 200 {
			t.Fatalf("member lists own %s: %d %v", path, code, out)
		}
		if code, out := call(t, "GET", hp+h2+path, nil, bearer(m1)...); !refused(code) {
			t.Fatalf("member lists another household's %s: %d %v", path, code, out)
		}
	}
	invite := map[string]any{"default_role": "member", "expires_in_days": 7}
	if code, out := call(t, "POST", hp+h1+"/invites", invite, bearer(m1)...); !refused(code) {
		t.Fatalf("member creates an invite: %d %v", code, out)
	}
	invCode, inv := call(t, "POST", hp+h1+"/invites", invite, bearer(p1)...)
	if invCode != 201 && invCode != 200 {
		t.Fatalf("power user creates an invite: %d %v", invCode, inv)
	}
	// A member of another household joins with the code, as themself; a bad code is refused.
	if code, out := call(t, "POST", base+"/api/households/join", map[string]any{"invite_code": obj(t, inv)["code"]}, bearer(m2)...); code != 200 && code != 201 {
		t.Fatalf("join with a code: %d %v", code, out)
	}
	if code, out := call(t, "POST", base+"/api/households/join", map[string]any{"invite_code": "NOPE-NOPE"}, bearer(m2)...); code < 400 || code >= 500 {
		t.Fatalf("join with a bad code: %d %v", code, out)
	}
	// ... and leaves again (two households now, not the last admin), so the checks below still
	// see m2 in H2 only.
	if code, out := call(t, "POST", hp+h1+"/leave", map[string]any{}, bearer(m2)...); code != 200 && code != 204 {
		t.Fatalf("leave after joining: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", hp+h1, map[string]any{"name": "Renamed"}, bearer(m1)...); !refused(code) {
		t.Fatalf("member renames the household: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", hp+h1, map[string]any{"name": "H1"}, bearer(a1)...); code != 200 {
		t.Fatalf("admin renames the household: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", hp+h1+"/members/"+fmt.Sprint(m1ID), map[string]any{"role": "admin"}, bearer(m1)...); !refused(code) {
		t.Fatalf("member promotes themself: %d %v", code, out)
	}
	if code, out := call(t, "DELETE", hp+h2+"/members/"+fmt.Sprint(m1ID), nil, bearer(m1)...); !refused(code) {
		t.Fatalf("member removes from another household: %d %v", code, out)
	}
	if code, out := call(t, "POST", hp+h2+"/leave", map[string]any{}, bearer(m1)...); !refused(code) {
		t.Fatalf("member leaves a household they're not in: %d %v", code, out)
	}
	// The household's voice starters and time zone: any member of it reads; another household's
	// are refused.
	for _, path := range []string{"/persona/presets", "/timezone", "/signal-automations"} {
		u := base + "/api/cc/api/v0/mobile/household/"
		if code, out := call(t, "GET", u+h1+path, nil, bearer(m1)...); code != 200 {
			t.Fatalf("own household %s: %d %v", path, code, out)
		}
		if code, out := call(t, "GET", u+h2+path, nil, bearer(m1)...); code != 403 {
			t.Fatalf("another household's %s: %d %v", path, code, out)
		}
	}
	// Signal automations: only a household admin writes; another household's are refused.
	sa := base + "/api/cc/api/v0/mobile/household/"
	rule := map[string]any{"instruction": "Lock the front door", "enabled": true, "delivery": "notification"}
	if code, out := call(t, "PUT", sa+h1+"/signal-automations/presence.left", rule, bearer(m1)...); code != 403 {
		t.Fatalf("member writes an automation: %d %v", code, out)
	}
	if code, out := call(t, "PUT", sa+h2+"/signal-automations/presence.left", rule, bearer(a1)...); code != 403 {
		t.Fatalf("admin writes another household's automation: %d %v", code, out)
	}
	if code, out := call(t, "PUT", sa+h1+"/signal-automations/presence.left", rule, bearer(a1)...); code != 200 {
		t.Fatalf("admin writes an automation: %d %v", code, out)
	}
	// Call details: each member's own; another member never sees them.
	cctx := base + "/api/cc/api/v0/mobile/call-context"
	detail := map[string]any{"fields": []any{map[string]any{"label": "Rewards number", "value": "9982-4471", "category": "dining", "tier": "if_asked"}}}
	if code, out := call(t, "PUT", cctx, detail, bearer(m1)...); code != 200 {
		t.Fatalf("own call details: %d %v", code, out)
	}
	if code, out := call(t, "GET", cctx, nil, bearer(p1)...); code != 200 || strings.Contains(fmt.Sprint(out), "9982-4471") {
		t.Fatalf("another member's call details: %d %v", code, out)
	}
	// Recent commands: the caller's own transcripts.
	if code, out := call(t, "GET", base+"/api/cc/api/v0/transcripts/recent?limit=5", nil, bearer(m1)...); code != 200 {
		t.Fatalf("own recent commands: %d %v", code, out)
	}
	// Rooms: a member changes their own household's; another household's are refused.
	rooms := base + "/api/cc/api/v0/households/"
	rc, rout := call(t, "POST", rooms+h1+"/rooms", map[string]any{"name": "Pantry"}, bearer(m1)...)
	if rc != 200 && rc != 201 {
		t.Fatalf("member adds a room: %d %v", rc, rout)
	}
	roomID := fmt.Sprint(obj(t, rout)["id"])
	if code, out := call(t, "POST", rooms+h2+"/rooms", map[string]any{"name": "Pantry"}, bearer(m1)...); code != 403 {
		t.Fatalf("member adds a room to another household: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", rooms+h2+"/rooms/"+roomID, map[string]any{"name": "Larder"}, bearer(m2)...); code != 404 {
		t.Fatalf("another household renames the room: %d %v", code, out)
	}
	if code, out := call(t, "DELETE", rooms+h1+"/rooms/"+roomID, nil, bearer(m1)...); code != 200 && code != 204 {
		t.Fatalf("member deletes the room: %d %v", code, out)
	}
	// Memories and the phonebook: the member's own household works; another household's are
	// refused (cc: not a member).
	mem := base + "/api/cc/api/v0/mobile/memories"
	if code, out := call(t, "GET", mem+"?household_id="+h1, nil, bearer(m1)...); code != 200 {
		t.Fatalf("own memories: %d %v", code, out)
	}
	if code, out := call(t, "POST", mem+"?household_id="+h1, map[string]any{"content": "likes oat milk"}, bearer(m1)...); code != 200 && code != 201 {
		t.Fatalf("create own memory: %d %v", code, out)
	}
	if code, out := call(t, "GET", mem+"?household_id="+h2, nil, bearer(m1)...); code != 403 {
		t.Fatalf("another household's memories: %d %v", code, out)
	}
	if code, out := call(t, "POST", mem+"?household_id="+h2, map[string]any{"content": "x"}, bearer(m1)...); code != 403 {
		t.Fatalf("create a memory in another household: %d %v", code, out)
	}
	pc := base + "/api/cc/api/v0/mobile/household/"
	if code, out := call(t, "GET", pc+h1+"/phone-contacts", nil, bearer(m1)...); code != 200 {
		t.Fatalf("own phonebook: %d %v", code, out)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/phone-contacts"}, {"POST", "/phone-contacts"}, {"PATCH", "/phone-contacts/c1"}, {"DELETE", "/phone-contacts/c1"},
	} {
		var body any
		if c.method == "POST" || c.method == "PATCH" {
			body = map[string]any{"name": "Tony's", "number": "+15551234567"}
		}
		if code, out := call(t, c.method, pc+h2+c.path, body, bearer(m1)...); code != 403 {
			t.Fatalf("%s another household's %s: %d %v", c.method, c.path, code, out)
		}
	}

	// Interactive lists: another household's node's records are refused (cc: not a member), and
	// tab matches only answer the member who paired the node (an admin-key node has no owner).
	cd := base + "/api/cc/api/v0/mobile/command-data/nodes/node-h2/commands/export_shopping_list/records"
	if code, out := call(t, "GET", cd, nil, bearer(m1)...); code != 403 {
		t.Fatalf("another household's node records: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", cd+"/milk", map[string]any{"walmart_item_id": "1"}, bearer(m1)...); code != 403 {
		t.Fatalf("patch another household's node record: %d %v", code, out)
	}
	for _, n := range []string{"node-h1", "node-h2"} {
		if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/nodes/"+n+"/browser/matches?pattern=(x)", nil, bearer(m1)...); code != 404 {
			t.Fatalf("tab matches on %s, not the member's: %d %v", n, code, out)
		}
	}

	// G10: a member's own account.
	if code, out := call(t, "GET", base+"/api/auth/me", nil, bearer(m1)...); code != 200 || obj(t, out)["email"] != "m1@example.com" {
		t.Fatalf("member me: %d %v", code, out)
	}
	if code, out := call(t, "PATCH", base+"/api/auth/me", map[string]any{"display_name": "Emm"}, bearer(m1)...); code != 200 {
		t.Fatalf("member display name: %d %v", code, out)
	}
	code, out := call(t, "GET", base+"/api/households", nil, bearer(m1)...)
	// Registration gave the member a "My Home" of their own as well; never H2.
	if code != 200 || !strings.Contains(jsonNum(out), h1) || strings.Contains(jsonNum(out), h2) {
		t.Fatalf("member households: %d %v", code, out)
	}
	if code, out := call(t, "POST", base+"/api/auth/switch-household", map[string]any{"household_id": h2}, bearer(m1)...); code != 403 {
		t.Fatalf("switch into a foreign household: %d %v", code, out)
	}
	code, out = call(t, "POST", base+"/api/auth/switch-household", map[string]any{"household_id": h1}, bearer(m1)...)
	if code != 200 {
		t.Fatalf("switch into own household: %d %v", code, out)
	}
	m1 = obj(t, out)["access_token"].(string) // scoped to H1 from here on (the inbox follows it)

	// G9: nodes are the caller's households'; the picker's ?household_id= scopes a superuser too.
	nodeIDs := func(tok, query string) (int, []string) {
		code, out := call(t, "GET", base+"/api/cc/api/v0/admin/nodes"+query, nil, bearer(tok)...)
		var ids []string
		if l, ok := out.([]any); ok {
			for _, n := range l {
				ids = append(ids, obj(t, n)["node_id"].(string))
			}
		}
		return code, ids
	}
	if code, ids := nodeIDs(m1, ""); code != 200 || strings.Join(ids, ",") != "node-h1" {
		t.Fatalf("member nodes: %d %v", code, ids)
	}
	if code, ids := nodeIDs(m2, ""); code != 200 || strings.Join(ids, ",") != "node-h2" {
		t.Fatalf("H2 member nodes: %d %v", code, ids)
	}
	if code, _ := nodeIDs(m1, "?household_id="+h2); code != 403 {
		t.Fatalf("member nodes of a foreign household: %d", code)
	}
	if code, ids := nodeIDs(root, ""); code != 200 || len(ids) != 2 {
		t.Fatalf("superuser nodes: %d %v", code, ids)
	}
	if code, ids := nodeIDs(root, "?household_id="+h1); code != 200 || strings.Join(ids, ",") != "node-h1" {
		t.Fatalf("superuser nodes for the picker: %d %v", code, ids)
	}

	// Cross-household chat: an H2 node from H1 is 404; claiming H2 is 403 (not a member).
	for _, path := range []string{"/api/cc/api/v0/mobile/chat", "/api/cc/api/v0/mobile/chat/warmup"} {
		if code, out := call(t, "POST", base+path, map[string]any{"message": "hi", "node_id": "node-h2", "household_id": h1}, bearer(m1)...); code != 404 {
			t.Errorf("%s to an H2 node as an H1 member: %d %v", path, code, out)
		}
		if code, out := call(t, "POST", base+path, map[string]any{"message": "hi", "node_id": "node-h2", "household_id": h2}, bearer(m1)...); code != 403 {
			t.Errorf("%s claiming H2 as an H1 member: %d %v", path, code, out)
		}
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/chat/capabilities", nil, bearer(m1)...); code != 200 {
		t.Errorf("capabilities: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/mobile/nodes/node-h2/tools", nil, bearer(m1)...); code != 403 && code != 404 {
		t.Errorf("tools of an H2 node: %d %v", code, out)
	}
	if code, out := call(t, "POST", base+"/api/cc/api/v0/nodes/node-h2/actions", map[string]any{"command_name": "x", "action_name": "y"}, bearer(m1)...); code != 403 && code != 404 {
		t.Errorf("action on an H2 node: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/households/"+h1+"/smart-home/config", nil, bearer(m1)...); code != 200 {
		t.Errorf("own smart-home config: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/households/"+h2+"/smart-home/config", nil, bearer(m1)...); code != 403 {
		t.Errorf("foreign smart-home config: %d %v", code, out)
	}
	// An admin key riding along with a member's token does not bypass the household check.
	if code, out := call(t, "GET", base+"/api/cc/api/v0/households/"+h2+"/smart-home/config", nil,
		append(bearer(m1), "X-API-Key", tierCCAdminKey)...); code != 403 {
		t.Errorf("foreign smart-home config with the admin key alongside: %d %v", code, out)
	}

	// The household prompt: cc lets only an admin of that household in.
	if code, out := call(t, "GET", base+"/api/cc/api/v0/households/"+h1+"/prompt", nil, bearer(a1)...); code != 200 {
		t.Errorf("own prompt as a household admin: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/cc/api/v0/households/"+h1+"/prompt", nil, bearer(m1)...); code != 403 {
		t.Errorf("own prompt as a member: %d %v", code, out)
	}
	if code, out := call(t, "PUT", base+"/api/cc/api/v0/households/"+h2+"/prompt", map[string]any{"text": "Hi."}, bearer(a1)...); code != 403 {
		t.Errorf("foreign prompt edit as another household's admin: %d %v", code, out)
	}

	// Household settings: a member reads them, only that household's admin writes them.
	hhSettings := func(hh string) string { return base + "/api/cc/api/v0/mobile/household/" + hh + "/settings" }
	if code, out := call(t, "GET", hhSettings(h1), nil, bearer(m1)...); code != 200 {
		t.Errorf("own household settings as a member: %d %v", code, out)
	}
	if code, out := call(t, "GET", hhSettings(h2), nil, bearer(m1)...); code != 403 {
		t.Errorf("foreign household settings as a member: %d %v", code, out)
	}
	if code, out := call(t, "PUT", hhSettings(h1)+"/watches.enabled", map[string]any{"value": true}, bearer(m1)...); code != 403 {
		t.Errorf("household setting write as a member: %d %v", code, out)
	}
	if code, out := call(t, "PUT", hhSettings(h1)+"/watches.enabled", map[string]any{"value": true}, bearer(a1)...); code != 200 {
		t.Errorf("household setting write as its admin: %d %v", code, out)
	}
	if code, out := call(t, "PUT", hhSettings(h2)+"/watches.enabled", map[string]any{"value": true}, bearer(a1)...); code != 403 {
		t.Errorf("household setting write as another household's admin: %d %v", code, out)
	}

	// Inbox: the caller's own rows.
	it, err := notif.CreateInboxItem(context.Background(), nil, notifmod.NewInboxItem{
		HouseholdID: h1, UserID: &m1ID, Title: "Research done", Summary: "s", Body: "b", Category: "deep_research", SourceService: "test"})
	if err != nil {
		t.Fatal(err)
	}
	code, out = call(t, "GET", base+"/api/notifications/api/v0/inbox", nil, bearer(m1)...)
	if code != 200 || !strings.Contains(jsonNum(out), it.ID) {
		t.Fatalf("member inbox: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/notifications/api/v0/inbox/"+it.ID, nil, bearer(m1)...); code != 200 {
		t.Fatalf("member inbox item: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/notifications/api/v0/inbox/"+it.ID, nil, bearer(m2)...); code != 404 {
		t.Fatalf("another household's inbox item: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/notifications/api/v0/inbox/unread-count", nil, bearer(m1)...); code != 200 {
		t.Fatalf("unread count: %d %v", code, out)
	}

	// G2-G7: no member, whatever their household role, reaches an install-wide route.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/settings"}, {"PUT", "/api/settings/auth/auth.token.access_expire_minutes?household_id=" + h2},
		{"GET", "/api/traces"}, {"GET", "/api/logs"}, {"GET", "/api/connections"}, {"POST", "/api/connections/apps"},
		{"POST", "/api/update/apply"}, {"POST", "/api/system/restart"}, {"POST", "/api/system/stop"},
		{"GET", "/api/llm/v1/hardware"}, {"POST", "/api/llm/v1/models/install"},
		{"GET", "/api/admin/users"}, {"POST", "/api/admin/users/1/temp-password"},
		{"GET", "/api/config/services"}, {"GET", "/api/doctor"}, {"GET", "/api/nope"},
		{"GET", "/api/phone/sip"}, {"POST", "/api/phone/sip/5062/tests/echo"}, {"GET", "/api/phone/sip/trace?format=text"},
	} {
		for email, tok := range users {
			if code, out := call(t, c.method, base+c.path, map[string]any{"value": 1}, bearer(tok)...); code != 403 {
				t.Errorf("%s %s as %s: %d %v", c.method, c.path, email, code, out)
			}
		}
	}
	if code, out := call(t, "GET", base+"/api/admin/users", nil, bearer(root)...); code != 200 {
		t.Fatalf("superuser users: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/settings", nil, bearer(root)...); code != 200 {
		t.Fatalf("superuser settings: %d %v", code, out)
	}
	// A member's reduced setup state: no counts, no labels.
	code, out = call(t, "GET", base+"/api/setup/state", nil, bearer(m1)...)
	if st := obj(t, out); code != 200 || st["households"] != nil || st["superuser"] == true {
		t.Fatalf("member setup state: %d %v", code, out)
	}

	// G8: a temp-password reset. Only me and change-password work until the change.
	_, p1ID := login(t, base, "p1@example.com")
	code, out = call(t, "POST", base+fmt.Sprintf("/api/admin/users/%d/temp-password", p1ID), map[string]any{"temp_password": "temp-pass-1"}, bearer(root)...)
	if code != 200 {
		t.Fatalf("temp-password: %d %v", code, out)
	}
	code, out = call(t, "POST", base+"/api/auth/login", map[string]any{"email": "p1@example.com", "password": "temp-pass-1"})
	if code != 200 || obj(t, out)["must_change_password"] != true {
		t.Fatalf("temp login: %d %v", code, out)
	}
	pending := obj(t, out)["access_token"].(string)
	if code, out := call(t, "GET", base+"/api/auth/me", nil, bearer(pending)...); code != 200 || obj(t, out)["must_change_password"] != true {
		t.Fatalf("pending me: %d %v", code, out)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/households"}, {"PATCH", "/api/auth/me"}, {"GET", "/api/cc/api/v0/admin/nodes"},
		{"POST", "/api/cc/api/v0/mobile/chat"}, {"GET", "/api/notifications/api/v0/inbox"}, {"GET", "/api/settings"},
	} {
		code, out := call(t, c.method, base+c.path, map[string]any{"message": "hi", "node_id": "node-h1", "household_id": h1}, bearer(pending)...)
		if code != 403 || obj(t, out)["code"] != "password_change_required" {
			t.Errorf("pending %s %s: %d %v", c.method, c.path, code, out)
		}
	}
	code, out = call(t, "POST", base+"/api/auth/change-password", map[string]any{"current_password": "temp-pass-1", "new_password": "password3"}, bearer(pending)...)
	if code != 200 {
		t.Fatalf("pending change-password: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/households", nil, bearer(obj(t, out)["access_token"].(string))...); code != 200 {
		t.Fatalf("households after the change: %d %v", code, out)
	}

	// change-password, last: it revokes the member's sessions.
	if code, out := call(t, "POST", base+"/api/auth/change-password",
		map[string]any{"current_password": "password1", "new_password": "password2"}, bearer(m1)...); code != 200 {
		t.Fatalf("member change-password: %d %v", code, out)
	}
}

// TestAddonsDeviceRoutesEndToEnd: device sign-out and "this computer" through the gateway, for
// every principal (decision 2026-10-14; install plan §3.2 Q2 (a)).
func TestAddonsDeviceRoutesEndToEnd(t *testing.T) {
	base, authURL, ccURL, _, home := memberStack(t)
	tok, err := os.ReadFile(authmod.SetupTokenPath(home))
	if err != nil {
		t.Fatal(err)
	}
	if code, out := call(t, "POST", base+"/api/auth/setup", map[string]any{"email": "root@example.com", "password": "password1"},
		authmod.SetupTokenHeader, strings.TrimSpace(string(tok))); code != 201 {
		t.Fatalf("setup: %d %v", code, out)
	}
	root, _ := login(t, base, "root@example.com")
	mkHousehold := func(name string) string {
		code, out := call(t, "POST", authURL+"/households", map[string]any{"name": name}, bearer(root)...)
		if code != 201 {
			t.Fatalf("household %s: %d %v", name, code, out)
		}
		return obj(t, out)["id"].(string)
	}
	h1, h2 := mkHousehold("H1"), mkHousehold("H2")
	openRegistration(t, authURL, root)
	users := map[string]string{}
	for _, u := range []struct{ email, hh, role string }{
		{"m1@example.com", h1, "member"}, {"o1@example.com", h1, "member"}, {"p1@example.com", h1, "power_user"},
		{"a1@example.com", h1, "admin"}, {"m2@example.com", h2, "member"},
	} {
		code, out := call(t, "POST", authURL+"/auth/register", map[string]any{"email": u.email, "password": "password1"})
		if code != 201 && code != 200 {
			t.Fatalf("register %s: %d %v", u.email, code, out)
		}
		id := obj(t, obj(t, out)["user"])["id"]
		if code, out := call(t, "POST", authURL+"/households/"+u.hh+"/members", map[string]any{"user_id": id, "role": u.role}, bearer(root)...); code != 201 && code != 200 {
			t.Fatalf("add %s: %d %v", u.email, code, out)
		}
		users[u.email], _ = login(t, base, u.email)
	}
	m1, o1, p1, a1, m2 := users["m1@example.com"], users["o1@example.com"], users["p1@example.com"], users["a1@example.com"], users["m2@example.com"]

	// A desktop agent paired by m1, as the setup page's sign-in does: a token, then register.
	pair := func(tok string) string {
		code, out := call(t, "POST", ccURL+"/api/v0/provisioning/token", map[string]any{"household_id": h1, "room": "desktop"}, bearer(tok)...)
		if code != 201 {
			t.Fatalf("token: %d %v", code, out)
		}
		pt := obj(t, out)
		code, out = call(t, "POST", ccURL+"/api/v0/nodes/register", map[string]any{"node_id": pt["node_id"], "provisioning_token": pt["token"]})
		if code != 201 {
			t.Fatalf("register: %d %v", code, out)
		}
		reg := obj(t, out)
		id := reg["node_id"].(string)
		if code, out := call(t, "POST", ccURL+"/api/v0/admin/nodes/heartbeat", map[string]any{
			"version_info": map[string]any{"install_mode": "desktop-agent"}, "node_kind": "desktop",
		}, "X-API-Key", id+":"+reg["node_key"].(string)); code != 200 {
			t.Fatalf("heartbeat: %d %v", code, out)
		}
		return id
	}
	agent := pair(m1)

	// This computer: the agent heartbeats from loopback, as this page asks from loopback.
	if code, _ := call(t, "GET", base+"/api/addons/this-device", nil); code != 401 {
		t.Errorf("this-device anonymous: %d", code)
	}
	for who, tok := range map[string]string{"member": m1, "other member": o1, "power user": p1, "household admin": a1, "superuser": root} {
		code, out := call(t, "GET", base+"/api/addons/this-device?household_id="+h1, nil, bearer(tok)...)
		if code != 200 || obj(t, out)["node_id"] != agent {
			t.Errorf("this-device as %s: %d %v", who, code, out)
		}
	}
	code, out := call(t, "GET", base+"/api/addons/this-device", nil, bearer(m2)...)
	if code != 200 || obj(t, out)["node_id"] != nil {
		t.Errorf("this-device as another household's member: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/addons/this-device?household_id="+h1, nil, bearer(m2)...); code != 403 {
		t.Errorf("this-device for a foreign household: %d %v", code, out)
	}
	// G11: X-Forwarded-For never makes a match.
	code, out = call(t, "GET", base+"/api/addons/this-device", nil, append(bearer(m1), "X-Forwarded-For", "10.0.0.9")...)
	if code != 200 || obj(t, out)["node_id"] != nil || obj(t, out)["reason"] != "proxied" {
		t.Errorf("this-device behind a forwarded header: %d %v", code, out)
	}

	// The agent download (install plan 3.1): every signed-in user reaches the handler (a dev
	// build answers 409 with the release page), nobody anonymous.
	if code, _ := call(t, "GET", base+"/api/addons/agent/linux/amd64", nil); code != 401 {
		t.Errorf("agent download anonymous: %d", code)
	}
	for who, tok := range map[string]string{"member": m1, "another household's member": m2, "superuser": root} {
		if code, out := call(t, "GET", base+"/api/addons/agent/linux/amd64", nil, bearer(tok)...); code != 409 {
			t.Errorf("agent download as %s: %d %v", who, code, out)
		}
	}

	// Sign-out: the pairer or a household admin; nobody else.
	signOut := func(id, tok string) int {
		h := []string{}
		if tok != "" {
			h = bearer(tok)
		}
		code, _ := call(t, "POST", base+"/api/cc/api/v0/nodes/"+id+"/sign-out", nil, h...)
		return code
	}
	for who, c := range map[string]struct {
		tok  string
		want int
	}{"anonymous": {"", 401}, "another member": {o1, 403}, "power user": {p1, 403}, "another household's member": {m2, 403}} {
		if got := signOut(agent, c.tok); got != c.want {
			t.Errorf("sign-out as %s: %d, want %d", who, got, c.want)
		}
	}
	if got := signOut(agent, m1); got != 200 {
		t.Fatalf("sign-out by the pairer: %d", got)
	}
	if got := signOut(pair(m1), a1); got != 200 {
		t.Errorf("sign-out by a household admin: %d", got)
	}
	if got := signOut(pair(m1), root); got != 200 {
		t.Errorf("sign-out by the superuser (an admin of H1, its creator): %d", got)
	}
}
