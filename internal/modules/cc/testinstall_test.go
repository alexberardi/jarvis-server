package cc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// fakePantry serves GET /v1/forge/drafts/{code} (jarvis-pantry forge_drafts.py) and records
// the codes asked for.
type fakePantry struct {
	*httptest.Server
	mu     sync.Mutex
	asked  []string
	drafts map[string]string // code -> raw JSON body
	status int               // when non-zero, every request answers this
}

func newFakePantry(t *testing.T) *fakePantry {
	t.Helper()
	p := &fakePantry{drafts: map[string]string{
		"ABC123": `{"package_name":"weather_test","files":[{"filename":"command.py","content":"x","language":"python"}]}`,
	}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, ok := strings.CutPrefix(r.URL.Path, "/v1/forge/drafts/")
		p.mu.Lock()
		p.asked = append(p.asked, code)
		status := p.status
		body, found := p.drafts[code]
		found = found && ok && r.Method == http.MethodGet
		p.mu.Unlock()
		switch {
		case status != 0:
			w.WriteHeader(status)
		case !found:
			http.Error(w, `{"detail":"Draft not found or expired"}`, http.StatusNotFound)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakePantry) set(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

func (p *fakePantry) lastAsked() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.asked) == 0 {
		return ""
	}
	return p.asked[len(p.asked)-1]
}

// setPantry points the household at a Pantry and turns its Pantry on (pantry.enabled
// defaults to off; pantry_gate_test.go covers the off case).
func setPantry(t *testing.T, e *env, hh, url string) {
	t.Helper()
	if err := e.m.Settings().Set(context.Background(), settingPantryBaseURL, url, settings.Scope{HouseholdID: hh}); err != nil {
		t.Fatal(err)
	}
	enablePantry(t, e, hh, true)
}

func tiRow(t *testing.T, e *env, id string) *tiRequest {
	t.Helper()
	r, err := loadTI(context.Background(), e.d.Read, id, "n1")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTestInstallFlow(t *testing.T) {
	e := newEnv(t)
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL+"/") // a trailing slash is tolerated
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	const base = "/api/v0/nodes/n1/test-install"
	body := map[string]any{"share_code": " abc123 "}
	e.do("POST", base, body, nil).detail(401, "Authentication required")
	e.do("POST", base, body, bearer("nope")).detail(401, "Invalid or expired JWT")
	e.do("POST", base, body, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("POST", "/api/v0/nodes/ghost/test-install", body, bearer(member)).detail(404, "Node not found")
	e.do("POST", base, map[string]any{}, bearer(member)).want(400)
	e.do("POST", base, map[string]any{"share_code": 123456}, bearer(member)).want(400)
	e.do("POST", base, map[string]any{"share_code": "abc12"}, bearer(member)).detail(400, "Invalid share code")
	e.do("POST", base, map[string]any{"share_code": "   "}, bearer(member)).detail(400, "Invalid share code")
	if pantry.lastAsked() != "" {
		t.Fatal("Pantry asked before the code was validated")
	}

	cr := e.do("POST", base, body, bearer(member)).want(201).json()
	rid, _ := cr["id"].(string)
	if rid == "" || cr["status"] != "pending" || cr["package_name"] != "weather_test" ||
		cr["created_at"] != "2026-10-06T12:00:00" || len(cr) != 4 {
		t.Fatal(cr)
	}
	if got := pantry.lastAsked(); got != "ABC123" {
		t.Fatalf("Pantry was asked for %q, want the normalised code", got)
	}
	// The nudge carries only the request_id (invariant 11).
	pk := c.next()
	if p := payload(t, pk); pk.TopicName != "jarvis/nodes/n1/test-install" || p["request_id"] != rid || len(p) != 1 {
		t.Fatal(pk.TopicName, p)
	}
	if r := tiRow(t, e, rid); r.shareCode != "ABC123" || r.householdID != "hh1" || !r.expiresAt.Equal(e.clock().Add(5*time.Minute)) {
		t.Fatal(r.shareCode, r.householdID, r.expiresAt)
	}

	poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "pending" || poll["request_id"] != rid || poll["package_name"] != "weather_test" ||
		poll["error_message"] != nil || poll["details"] != nil || len(poll) != 5 {
		t.Fatal(poll)
	}
	// Legacy skipped the household check on the poll (§8); Go doesn't.
	e.do("GET", base+"/"+rid, nil, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("GET", base+"/"+rid, nil, nil).detail(401, "Authentication required")
	e.do("GET", base+"/nope", nil, bearer(member)).detail(404, "Test install request not found")
	e.do("GET", base+"/"+rid, nil, adminH()).want(200)

	// Verify: node auth bound to the path node (D4); the row must be this node's.
	verify := base + "/" + rid + "/verify"
	e.do("GET", verify, nil, nil).want(400)
	e.do("GET", verify, nil, hdr{"X-API-Key": "n1:wrong"}).want(401)
	e.do("GET", verify, nil, other.h()).detail(403, "Node mismatch")
	e.do("GET", "/api/v0/nodes/n2/test-install/"+rid+"/verify", nil, other.h()).detail(404, "Test install request not found")
	e.advance(4 * time.Minute)
	v := e.do("GET", verify, nil, n.h()).want(200).json()
	if v["confirmed"] != true || v["package_name"] != "weather_test" ||
		v["pantry_download_url"] != pantry.URL+"/v1/forge/drafts/ABC123" || len(v) != 3 {
		t.Fatal(v)
	}
	// D39: the pickup deadline became verify + 15 min; a repeat verify doesn't move it.
	if r := tiRow(t, e, rid); !r.expiresAt.Equal(e.clock().Add(15*time.Minute)) || !r.verifiedAt.Valid {
		t.Fatal(r.expiresAt, r.verifiedAt)
	}
	e.advance(time.Minute)
	e.do("GET", verify, nil, n.h()).want(200)
	if r := tiRow(t, e, rid); !r.expiresAt.Equal(e.clock().Add(14 * time.Minute)) {
		t.Fatal(r.expiresAt)
	}

	// A slow pip install on a Pi Zero (past the old 5-minute absolute expiry) still lands.
	e.advance(8 * time.Minute)
	results := base + "/" + rid + "/results"
	e.do("POST", results, map[string]any{"success": true}, other.h()).detail(403, "Node mismatch")
	e.do("POST", results, map[string]any{}, n.h()).want(400)
	e.do("POST", results, map[string]any{"success": "maybe"}, n.h()).want(400)
	e.do("POST", base+"/nope/results", map[string]any{"success": true}, n.h()).detail(404, "Test install request not found")

	e.m.cmdData.put("n1", "weather_test", map[string]any{"mode": "enabled"})
	// `restarting` is not part of the test-install body: it is ignored, the row completes.
	r := e.do("POST", results, map[string]any{"success": true, "restarting": true,
		"details": map[string]any{"package_name": "weather_test", "pip_packages_added": []any{"requests==2.0"}}}, n.h()).want(200).json()
	if r["status"] != "ok" || len(r) != 1 {
		t.Fatal(r)
	}
	if e.m.cmdData.get("n1", "weather_test") != nil {
		t.Fatal("a finished test install must invalidate the node's command schemas")
	}
	poll = e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	details, _ := poll["details"].(map[string]any)
	if poll["status"] != "completed" || details["package_name"] != "weather_test" || poll["error_message"] != nil {
		t.Fatal(poll)
	}
	if row := tiRow(t, e, rid); !row.completedAt.Valid {
		t.Fatal("completed_at not set")
	}
	// D8: terminal is sticky; a late failure is acknowledged and ignored.
	e.do("POST", results, map[string]any{"success": false, "error": "late"}, n.h()).want(200)
	if row := tiRow(t, e, rid); row.status != "completed" || row.errorMessage.Valid {
		t.Fatal(row.status, row.errorMessage)
	}
	e.do("GET", verify, nil, n.h()).detail(409, "Request already completed")
	// The completed poll survives the row's expiry.
	e.advance(time.Hour)
	if poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json(); poll["status"] != "completed" {
		t.Fatal(poll)
	}
}

func TestTestInstallFailureResults(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/test-install"

	// The node posts a failure straight away when its verify fails (test_install_handler.py).
	for _, c := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"success": false, "error": "Verification failed"}, "Verification failed"},
		{map[string]any{"success": false}, "Unknown error"},
		{map[string]any{"success": false, "error": ""}, "Unknown error"},
		{map[string]any{"success": false, "error": nil, "details": nil}, "Unknown error"},
	} {
		rid := e.do("POST", base, map[string]any{"share_code": "ABC123"}, bearer(member)).want(201).json()["id"].(string)
		e.do("POST", base+"/"+rid+"/results", c.body, n.h()).want(200)
		poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
		if poll["status"] != "failed" || poll["error_message"] != c.want || poll["details"] != nil {
			t.Fatal(c.body, poll)
		}
		// A failed row is sticky against a late success too.
		e.do("POST", base+"/"+rid+"/results", map[string]any{"success": true}, n.h()).want(200)
		if row := tiRow(t, e, rid); row.status != "failed" || !row.completedAt.Valid {
			t.Fatal(row.status)
		}
		e.do("GET", base+"/"+rid+"/verify", nil, n.h()).detail(409, "Request already failed")
	}
}

func TestTestInstallExpiry(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/test-install"
	create := func() string {
		return e.do("POST", base, map[string]any{"share_code": "ABC123"}, bearer(member)).want(201).json()["id"].(string)
	}

	// Never picked up: the poll flips the row, with legacy's message.
	rid := create()
	e.advance(5*time.Minute + time.Second)
	poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json()
	if poll["status"] != "expired" || poll["error_message"] != "Test install request expired — node may be offline" ||
		poll["package_name"] != "weather_test" || poll["details"] != nil {
		t.Fatal(poll)
	}
	if row := tiRow(t, e, rid); row.status != "expired" {
		t.Fatal(row.status)
	}
	e.do("GET", base+"/"+rid+"/verify", nil, n.h()).detail(410, "Test install request expired")
	e.do("POST", base+"/"+rid+"/results", map[string]any{"success": true}, n.h()).detail(410, "Test install request expired")

	// Verify itself expires an unpolled row past the pickup deadline.
	rid = create()
	e.advance(6 * time.Minute)
	e.do("GET", base+"/"+rid+"/verify", nil, n.h()).detail(410, "Test install request expired")
	if row := tiRow(t, e, rid); row.status != "expired" {
		t.Fatal(row.status)
	}

	// Verified, but the result lands after verify + 15 min.
	rid = create()
	e.do("GET", base+"/"+rid+"/verify", nil, n.h()).want(200)
	e.advance(15*time.Minute + time.Second)
	e.do("POST", base+"/"+rid+"/results", map[string]any{"success": true}, n.h()).detail(410, "Test install request expired")
	if poll := e.do("GET", base+"/"+rid, nil, bearer(member)).want(200).json(); poll["status"] != "expired" {
		t.Fatal(poll)
	}
}

func TestTestInstallPantryErrors(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/test-install"
	post := func(code string) *resp {
		return e.do("POST", base, map[string]any{"share_code": code}, bearer(member))
	}
	count := func() int {
		var n int
		if err := e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_test_install_requests`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	post("ZZZ999").detail(404, "Share code not found or expired")

	pantry.set(func() { pantry.drafts["NONAME"] = `{"files":[]}` })
	if cr := post("noname").want(201).json(); cr["package_name"] != "unknown" {
		t.Fatal(cr)
	}
	pantry.set(func() { pantry.drafts["BADJSN"] = `<html>` })
	post("BADJSN").detail(502, "Pantry returned an error")

	pantry.set(func() { pantry.status = http.StatusInternalServerError })
	post("ABC123").detail(502, "Pantry returned an error")
	pantry.set(func() { pantry.status = http.StatusUnauthorized })
	post("ABC123").detail(502, "Pantry returned an error")

	// Unreachable: nothing listens on the port any more.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := "http://" + l.Addr().String()
	l.Close()
	setPantry(t, e, "hh1", dead)
	post("ABC123").detail(502, "Could not reach Pantry service")
	setPantry(t, e, "hh1", "::not a url")
	post("ABC123").detail(502, "Could not reach Pantry service")

	if got := count(); got != 1 {
		t.Fatalf("%d rows; only the successful create may insert one", got)
	}
}

func TestTestInstallPantryTimeout(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); slow.Close() })
	setPantry(t, e, "hh1", slow.URL)
	e.m.pantryHTTP = &http.Client{Timeout: 50 * time.Millisecond}
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	e.do("POST", "/api/v0/nodes/n1/test-install", map[string]any{"share_code": "ABC123"}, bearer(member)).
		detail(502, "Could not reach Pantry service")
	if pantryDraftTimeout != 10*time.Second {
		t.Fatal("legacy's Pantry timeout is 10 s")
	}
}

func TestTestInstallHouseholdRules(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	// D5: membership of the target household among all of the caller's memberships.
	multi := e.auth.addUser(1, "hh2", authn.RoleMember)
	e.auth.roles["hh1"] = map[int64]authn.Role{1: authn.RoleMember}
	super := e.auth.addUser(9, "", "")
	e.auth.users[super] = authn.User{ID: 9, IsSuperuser: true}
	e.createNode("n1", "hh1")
	body := map[string]any{"share_code": "ABC123"}
	rid := e.do("POST", "/api/v0/nodes/n1/test-install", body, bearer(multi)).want(201).json()["id"].(string)
	e.do("GET", "/api/v0/nodes/n1/test-install/"+rid, nil, bearer(multi)).want(200)
	// A request id under another node's path is not found.
	e.createNode("n2", "hh1")
	e.do("GET", "/api/v0/nodes/n2/test-install/"+rid, nil, bearer(multi)).detail(404, "Test install request not found")

	// A node with no household fails closed for members; a superuser and the admin key may
	// still use it (D40 12.Q5). Its Pantry is the default/global one.
	if _, err := e.d.Write.Exec(`INSERT INTO cc_nodes (node_id, room) VALUES ('loose', 'attic')`); err != nil {
		t.Fatal(err)
	}
	setPantry(t, e, "", pantry.URL) // the system scope: a household-less node reads it
	e.do("POST", "/api/v0/nodes/loose/test-install", body, bearer(multi)).detail(403, "Not authorized")
	e.do("POST", "/api/v0/nodes/loose/test-install", body, bearer(super)).want(201)
	rid = e.do("POST", "/api/v0/nodes/loose/test-install", body, adminH()).want(201).json()["id"].(string)
	e.do("GET", "/api/v0/nodes/loose/test-install/"+rid, nil, bearer(multi)).detail(403, "Not authorized")
	e.do("GET", "/api/v0/nodes/loose/test-install/"+rid, nil, adminH()).want(200)
}

func TestTestInstallSweep(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	body := map[string]any{"share_code": "ABC123"}
	old := e.do("POST", "/api/v0/nodes/n1/test-install", body, bearer(member)).want(201).json()["id"].(string)
	e.advance(29 * 24 * time.Hour)
	fresh := e.do("POST", "/api/v0/nodes/n1/test-install", body, bearer(member)).want(201).json()["id"].(string)
	e.advance(2 * 24 * time.Hour)
	if err := e.m.cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{old: 0, fresh: 1} {
		var n int
		e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_test_install_requests WHERE id = ?`, id).Scan(&n)
		if n != want {
			t.Fatalf("row %s: %d, want %d", id, n, want)
		}
	}
	// Deleting the node cascades its requests.
	if _, err := e.d.Write.Exec(`DELETE FROM cc_nodes WHERE node_id = 'n1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM cc_test_install_requests`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}
