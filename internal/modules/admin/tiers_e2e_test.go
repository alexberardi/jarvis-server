package admin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
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

func TestMemberTiersEndToEnd(t *testing.T) {
	base, authURL, ccURL, notif, home := memberStack(t)

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

	// change-password, last: it revokes the member's sessions.
	if code, out := call(t, "POST", base+"/api/auth/change-password",
		map[string]any{"current_password": "password1", "new_password": "password2"}, bearer(m1)...); code != 200 {
		t.Fatalf("member change-password: %d %v", code, out)
	}
}
