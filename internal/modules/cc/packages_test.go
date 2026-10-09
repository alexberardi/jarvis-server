package cc

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- the state machine (§3.2 with D39 and D8), as a table ---

func TestPackageTransitions(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	row := func(status string, expires time.Duration, verified bool) *pkgRequest {
		p := &pkgRequest{status: status, createdAt: t0, expiresAt: t0.Add(expires)}
		if verified {
			p.verifiedAt = sql.NullString{String: dbTime(t0), Valid: true}
		}
		return p
	}
	now := t0.Add(time.Minute)

	verify := []struct {
		name       string
		p          *pkgRequest
		wantCode   int
		wantStatus string
		wantExp    time.Time
		changed    bool
	}{
		{"pending first verify extends to verify+15m", row("pending", 5*time.Minute, false), 200, "pending", now.Add(15 * time.Minute), true},
		{"pending repeat verify is idempotent", row("pending", 15*time.Minute, true), 200, "pending", t0.Add(15 * time.Minute), false},
		{"pending past pickup deadline expires", row("pending", 30*time.Second, false), 410, "expired", t0.Add(30 * time.Second), true},
		{"restarting conflicts", row("restarting", 10*time.Minute, true), 409, "restarting", t0.Add(10 * time.Minute), false},
		{"restarting past expiry expires", row("restarting", 30*time.Second, true), 410, "expired", t0.Add(30 * time.Second), true},
		{"completed is sticky even past expiry", row("completed", -time.Hour, true), 409, "completed", t0.Add(-time.Hour), false},
		{"failed is sticky", row("failed", time.Hour, true), 409, "failed", t0.Add(time.Hour), false},
		{"expired stays expired", row("expired", -time.Hour, false), 410, "expired", t0.Add(-time.Hour), false},
	}
	for _, c := range verify {
		out := verifyTransition(c.p, now)
		if out.status != c.wantCode || c.p.status != c.wantStatus || !c.p.expiresAt.Equal(c.wantExp) || out.changed != c.changed {
			t.Errorf("verify %s: got %d %s exp %v changed %v", c.name, out.status, c.p.status, c.p.expiresAt, out.changed)
		}
	}

	errMsg := "boom"
	empty := ""
	results := []struct {
		name       string
		p          *pkgRequest
		res        pkgResult
		wantCode   int
		wantStatus string
		wantExp    time.Time
		wantErr    string
		completed  bool
	}{
		{"restarting extends 120s from the old expiry", row("pending", 10*time.Minute, true), pkgResult{success: true, restarting: true},
			200, "restarting", t0.Add(10*time.Minute + 120*time.Second), "", false},
		{"restarting is repeatable", row("restarting", 10*time.Minute, true), pkgResult{success: true, restarting: true},
			200, "restarting", t0.Add(10*time.Minute + 120*time.Second), "", false},
		{"success completes", row("restarting", 10*time.Minute, true), pkgResult{success: true}, 200, "completed", t0.Add(10 * time.Minute), "", true},
		{"failure with message", row("pending", 10*time.Minute, true), pkgResult{err: &errMsg}, 200, "failed", t0.Add(10 * time.Minute), "boom", true},
		{"failure with empty message", row("pending", 10*time.Minute, true), pkgResult{err: &empty}, 200, "failed", t0.Add(10 * time.Minute), "Unknown error", true},
		{"late result expires", row("restarting", 30*time.Second, true), pkgResult{success: true}, 410, "expired", t0.Add(30 * time.Second), "", false},
		{"completed is sticky", row("completed", time.Hour, true), pkgResult{err: &errMsg}, 200, "completed", t0.Add(time.Hour), "", false},
		{"failed is sticky against restarting", row("failed", time.Hour, true), pkgResult{success: true, restarting: true}, 200, "failed", t0.Add(time.Hour), "", false},
	}
	for _, c := range results {
		out := resultTransition(c.p, c.res, opInstall, now)
		if out.status != c.wantCode || c.p.status != c.wantStatus || !c.p.expiresAt.Equal(c.wantExp) ||
			c.p.errorMessage.String != c.wantErr || c.p.completedAt.Valid != c.completed {
			t.Errorf("results %s: got %d %s exp %v err %q completed %v", c.name, out.status, c.p.status, c.p.expiresAt,
				c.p.errorMessage.String, c.p.completedAt.Valid)
		}
	}
	if out := resultTransition(row("pending", -time.Second, false), pkgResult{}, opRevert, now); out.detail != "Revert request expired" {
		t.Fatal(out)
	}

	// The poll flips only a live row past its expiry.
	for _, c := range []struct {
		p    *pkgRequest
		want string
	}{
		{row("pending", -time.Second, false), "expired"},
		{row("restarting", -time.Second, true), "expired"},
		{row("pending", time.Minute, false), "pending"},
		{row("completed", -time.Hour, true), "completed"},
		{row("failed", -time.Hour, true), "failed"},
	} {
		pollTransition(c.p, t0)
		if c.p.status != c.want {
			t.Errorf("poll: %s, want %s", c.p.status, c.want)
		}
	}
}

// --- HTTP ---

func pkgRow(t *testing.T, e *env, id string) *pkgRequest {
	t.Helper()
	p, err := e.m.loadPkg(context.Background(), e.d.Read, id, "n1")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPackageInstallFlow(t *testing.T) {
	e := newEnv(t)
	enablePantry(t, e, "hh1", true)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	const base = "/api/v0/nodes/n1/package-install"
	body := map[string]any{"command_name": "weather", "github_repo_url": "https://example.com/r.git", "git_tag": "abc123"}
	e.do("POST", base, body, nil).detail(401, "Authentication required")
	e.do("POST", base, body, hdr{"X-API-Key": "nope"}).detail(401, "Invalid API key")
	e.do("POST", base, body, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("POST", "/api/v0/nodes/ghost/package-install", body, bearer(member)).detail(404, "Node not found")
	e.do("POST", base, map[string]any{"command_name": "x"}, bearer(member)).want(400)

	cr := e.do("POST", base, body, bearer(member)).want(201).json()
	rid := cr["id"].(string)
	if cr["status"] != "pending" || cr["created_at"] != "2026-10-06T12:00:00" || len(cr) != 3 {
		t.Fatal(cr)
	}
	pk := c.next()
	if pk.TopicName != "jarvis/nodes/n1/package-install" {
		t.Fatal(pk.TopicName)
	}
	if p := payload(t, pk); p["request_id"] != rid || p["command_name"] != "weather" || p["github_repo_url"] != "https://example.com/r.git" ||
		p["git_tag"] != "abc123" || p["pantry_url"] != defaultPantryBaseURL || len(p) != 5 {
		t.Fatal(p)
	}
	// Admin key bypasses the household check.
	e.do("POST", base, body, adminH()).want(201)
	_ = c.next()

	poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "pending" || poll["request_id"] != rid || poll["command_name"] != "weather" || len(poll) != 5 ||
		poll["error_message"] != nil || poll["details"] != nil {
		t.Fatal(poll)
	}
	e.do("GET", base+"/"+rid, nil, bearer(stranger)).want(403)
	e.do("GET", base+"/nope", nil, bearer(member)).detail(404, "Install request not found")

	// Verify: node auth bound to the path node (D4).
	verify := base + "/" + rid + "/verify"
	e.do("GET", verify, nil, nil).want(400)
	e.do("GET", verify, nil, other.h()).detail(403, "Node mismatch")
	e.do("GET", "/api/v0/nodes/n2/package-install/"+rid+"/verify", nil, other.h()).detail(404, "Package install request not found")
	e.advance(4 * time.Minute)
	v := e.do("GET", verify, nil, n.h()).want(200).json()
	if v["confirmed"] != true || v["command_name"] != "weather" || v["github_repo_url"] != "https://example.com/r.git" || v["git_tag"] != "abc123" || v["pantry_url"] != defaultPantryBaseURL || len(v) != 5 {
		t.Fatal(v)
	}
	// D39: the pickup deadline became verify + 15 min; a repeat verify doesn't move it.
	if p := pkgRow(t, e, rid); !p.expiresAt.Equal(e.clock().Add(15*time.Minute)) || !p.verifiedAt.Valid {
		t.Fatal(p.expiresAt, p.verifiedAt)
	}
	e.advance(time.Minute)
	e.do("GET", verify, nil, n.h()).want(200)
	if p := pkgRow(t, e, rid); !p.expiresAt.Equal(e.clock().Add(14 * time.Minute)) {
		t.Fatal(p.expiresAt)
	}

	// A slow install (past the old 5-minute absolute expiry) still lands (D39).
	e.advance(8 * time.Minute)
	results := base + "/" + rid + "/results"
	e.do("POST", results, map[string]any{"success": true}, other.h()).detail(403, "Node mismatch")
	e.do("POST", results, map[string]any{}, n.h()).want(400)
	e.do("POST", base+"/nope/results", map[string]any{"success": true}, n.h()).detail(404, "Install request not found")
	e.do("POST", results, map[string]any{"success": true, "restarting": true, "details": map[string]any{"v": "1"}}, n.h()).want(200)
	if poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json(); poll["status"] != "restarting" || poll["details"] != nil {
		t.Fatal(poll)
	}
	e.do("GET", verify, nil, n.h()).detail(409, "Request already restarting")
	e.advance(7 * time.Minute) // past verify+15 but inside the +120 s extension
	r := e.do("POST", results, map[string]any{"success": true, "details": map[string]any{"version": "1.2.0"}}, n.h()).want(200).json()
	if r["status"] != "ok" {
		t.Fatal(r)
	}
	poll = e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "completed" || poll["details"].(map[string]any)["version"] != "1.2.0" || poll["error_message"] != nil {
		t.Fatal(poll)
	}
	// D8: terminal is sticky; a late failure is acknowledged and ignored.
	e.do("POST", results, map[string]any{"success": false, "error": "late"}, n.h()).want(200)
	if p := pkgRow(t, e, rid); p.status != "completed" || p.errorMessage.Valid {
		t.Fatal(p.status, p.errorMessage)
	}
	e.do("GET", verify, nil, n.h()).detail(409, "Request already completed")
}

func TestPackageExpiry(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/package-install"
	body := map[string]any{"command_name": "weather", "github_repo_url": "u"}

	// The poll flips an unverified row past the pickup deadline, with the install message.
	rid := e.do("POST", base, body, bearer(member)).want(201).json()["id"].(string)
	e.advance(5*time.Minute + time.Second)
	poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "expired" || poll["error_message"] != "Install request expired — node may be offline" {
		t.Fatal(poll)
	}
	if p := pkgRow(t, e, rid); p.status != "expired" {
		t.Fatal(p.status)
	}
	e.do("GET", base+"/"+rid+"/verify", nil, n.h()).detail(410, "Package install request expired")

	// Verify and results expire a row themselves.
	rid = e.do("POST", base, body, bearer(member)).want(201).json()["id"].(string)
	if v := e.do("GET", base+"/"+rid+"/verify", nil, n.h()).want(200).json(); v["git_tag"] != nil {
		t.Fatal(v)
	}
	e.advance(16 * time.Minute)
	e.do("POST", base+"/"+rid+"/results", map[string]any{"success": true}, n.h()).detail(410, "Install request expired")
	if p := pkgRow(t, e, rid); p.status != "expired" {
		t.Fatal(p.status)
	}
	// The uninstall poll reports a bare expiry with a null message (invariant 6).
	poll = e.do("GET", "/api/v0/nodes/n1/package-uninstall/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "expired" || poll["error_message"] != nil {
		t.Fatal(poll)
	}
}

func TestPackageUninstallAndRevert(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	// Uninstall: component_type is required, and forwarded as a hint.
	e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "weather"}, bearer(member)).want(400)
	e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "weather", "component_type": "command"}, bearer(stranger)).want(403)
	rid := e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "weather", "component_type": "command"}, bearer(member)).want(201).json()["id"].(string)
	pk := c.next()
	if p := payload(t, pk); pk.TopicName != "jarvis/nodes/n1/package-uninstall" || p["request_id"] != rid || p["command_name"] != "weather" || p["component_type"] != "command" || len(p) != 3 {
		t.Fatal(pk.TopicName, p)
	}
	// It verifies through the install route and gets an empty URL.
	if v := e.do("GET", "/api/v0/nodes/n1/package-install/"+rid+"/verify", nil, n.h()).want(200).json(); v["github_repo_url"] != "" {
		t.Fatal(v)
	}
	e.do("POST", "/api/v0/nodes/n1/package-uninstall/"+rid+"/results", map[string]any{"success": true, "restarting": true, "details": map[string]any{"a": 1}}, n.h()).want(200)
	// The uninstall poll returns details even while restarting.
	poll := e.do("GET", "/api/v0/nodes/n1/package-uninstall/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "restarting" || poll["details"].(map[string]any)["a"] != 1.0 {
		t.Fatal(poll)
	}
	e.do("POST", "/api/v0/nodes/n1/package-uninstall/"+rid+"/results", map[string]any{"success": false}, n.h()).want(200)
	poll = e.do("GET", "/api/v0/nodes/n1/package-uninstall/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "failed" || poll["error_message"] != "Unknown error" {
		t.Fatal(poll)
	}
	e.do("GET", "/api/v0/nodes/n1/package-uninstall/nope", nil, bearer(member)).detail(404, "Uninstall request not found")
	e.do("POST", "/api/v0/nodes/n1/package-uninstall/nope/results", map[string]any{"success": true}, n.h()).detail(404, "Uninstall request not found")

	// Revert: command_name or package_name; neither is a string-detail 422.
	e.do("POST", "/api/v0/nodes/n1/package-revert", map[string]any{}, bearer(member)).detail(http.StatusUnprocessableEntity, "command_name or package_name is required")
	e.do("POST", "/api/v0/nodes/n1/package-revert", map[string]any{"command_name": ""}, bearer(member)).want(422)
	rid = e.do("POST", "/api/v0/nodes/n1/package-revert", map[string]any{"package_name": "weather"}, bearer(member)).want(201).json()["id"].(string)
	pk = c.next()
	if p := payload(t, pk); pk.TopicName != "jarvis/nodes/n1/package-revert" || p["request_id"] != rid || p["command_name"] != "weather" || p["package_name"] != "weather" || len(p) != 3 {
		t.Fatal(pk.TopicName, p)
	}
	e.do("POST", "/api/v0/nodes/n1/package-revert/"+rid+"/results", map[string]any{"success": true}, n.h()).want(200)
	poll = e.do("GET", "/api/v0/nodes/n1/package-revert/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "completed" || poll["command_name"] != "weather" || poll["details"] != nil || poll["error_message"] != nil {
		t.Fatal(poll)
	}
	e.do("GET", "/api/v0/nodes/n1/package-revert/nope", nil, bearer(member)).detail(404, "Revert request not found")
}

func TestPackageHouseholdRules(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	enablePantry(t, e, "", true) // the system scope: the household-less node below reads it
	// D5: membership of the target household among all of the caller's memberships.
	multi := e.auth.addUser(1, "hh2", authn.RoleMember)
	e.auth.roles["hh1"] = map[int64]authn.Role{1: authn.RoleMember}
	super := e.auth.addUser(9, "", "")
	e.auth.users[super] = authn.User{ID: 9, IsSuperuser: true}
	e.createNode("n1", "hh1")
	body := map[string]any{"command_name": "weather", "github_repo_url": "u"}
	e.do("POST", "/api/v0/nodes/n1/package-install", body, bearer(multi)).want(201)

	// D40 12.Q5: a node with no household fails closed for members; a superuser and the admin
	// key can still manage it (as on the other node routes).
	if _, err := e.d.Write.Exec(`INSERT INTO cc_nodes (node_id, room) VALUES ('loose', 'attic')`); err != nil {
		t.Fatal(err)
	}
	e.do("POST", "/api/v0/nodes/loose/package-install", body, bearer(multi)).detail(403, "Not authorized")
	e.do("POST", "/api/v0/nodes/loose/package-install", body, bearer(super)).want(201)
	rid := e.do("POST", "/api/v0/nodes/loose/package-install", body, adminH()).want(201).json()["id"].(string)
	var hh string
	e.d.Read.QueryRow(`SELECT household_id FROM cc_package_install_requests WHERE id = ?`, rid).Scan(&hh)
	if hh != "" {
		t.Fatal(hh)
	}
	e.do("GET", "/api/v0/nodes/loose/package-install/"+rid, nil, bearer(multi)).detail(403, "Not authorized")
	e.do("GET", "/api/v0/nodes/loose/package-install/"+rid, nil, adminH()).want(200)
}

func TestPackageSweepAndSchemaInvalidation(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	body := map[string]any{"command_name": "weather", "github_repo_url": "u"}
	old := e.do("POST", "/api/v0/nodes/n1/package-install", body, bearer(member)).want(201).json()["id"].(string)

	e.m.cmdData.put("n1", "weather", map[string]any{"mode": "enabled"})
	e.m.cmdData.put("n2", "weather", map[string]any{"mode": "enabled"})
	e.do("GET", "/api/v0/nodes/n1/package-install/"+old+"/verify", nil, n.h()).want(200)
	e.do("POST", "/api/v0/nodes/n1/package-install/"+old+"/results", map[string]any{"success": true, "restarting": true}, n.h()).want(200)
	if e.m.cmdData.get("n1", "weather") == nil {
		t.Fatal("restarting must not invalidate")
	}
	e.do("POST", "/api/v0/nodes/n1/package-install/"+old+"/results", map[string]any{"success": true}, n.h()).want(200)
	if e.m.cmdData.get("n1", "weather") != nil || e.m.cmdData.get("n2", "weather") == nil {
		t.Fatal("completion must invalidate exactly the node's schemas (D40 12.Q10)")
	}

	e.advance(29 * 24 * time.Hour)
	fresh := e.do("POST", "/api/v0/nodes/n1/package-install", body, bearer(member)).want(201).json()["id"].(string)
	e.advance(2 * 24 * time.Hour)
	if err := e.m.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_package_install_requests WHERE id = ?`, old).Scan(&count)
	if count != 0 {
		t.Fatal("30-day-old row survived the sweep")
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_package_install_requests WHERE id = ?`, fresh).Scan(&count)
	if count != 1 {
		t.Fatal("fresh row swept")
	}
}

func TestPantryBaseURL(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	if got := e.m.PantryBaseURL(ctx, "hh1"); got != defaultPantryBaseURL {
		t.Fatal(got)
	}
	if err := e.m.Settings().Set(ctx, settingPantryBaseURL, "http://pantry.lan:7721", settings.Scope{HouseholdID: "hh1"}); err != nil {
		t.Fatal(err)
	}
	if got := e.m.PantryBaseURL(ctx, "hh1"); got != "http://pantry.lan:7721" {
		t.Fatal(got)
	}
	if got := e.m.PantryBaseURL(ctx, "hh2"); got != defaultPantryBaseURL {
		t.Fatal(got)
	}
}
