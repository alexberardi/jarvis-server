package cc

import (
	"net/url"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestBrowserMatchesOnlyForTheNodesOwner(t *testing.T) {
	e := newEnv(t, envOpts{})
	owner := e.auth.addUser(7, "hh1", authn.RoleMember)
	admin := e.auth.addUser(8, "hh1", authn.RoleAdmin)
	n := e.createNode("pc1", "hh1")
	if _, err := e.m.deps.DB.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = 7 WHERE node_id = 'pc1'`); err != nil {
		t.Fatal(err)
	}
	f := e.respondingNode(n, "browser")
	f.set(func(op string, p map[string]any) any {
		return map[string]any{"ok": true, "matches": []any{map[string]any{"value": "10450114", "title": "Great Value Milk"}}}
	})
	pattern := `/ip/(?:[^/]+/)?(\d{5,})`
	path := "/api/v0/mobile/nodes/pc1/browser/matches?pattern=" + url.QueryEscape(pattern)

	got := e.do("GET", path, nil, bearer(owner)).want(200).json()
	m, _ := got["matches"].([]any)
	if got["ok"] != true || len(m) != 1 || m[0].(map[string]any)["value"] != "10450114" {
		t.Fatalf("matches: %v", got)
	}
	if op, p := f.last(); op != "match-tabs" || p["pattern"] != pattern {
		t.Fatalf("node got %s %v", op, p)
	}

	// Tabs are personal: a household admin is told no more than about a node that isn't there.
	e.do("GET", path, nil, bearer(admin)).detail(404, "Node not found")
	e.do("GET", "/api/v0/mobile/nodes/ghost/browser/matches?pattern=(x)", nil, bearer(owner)).detail(404, "Node not found")
	e.do("GET", path, nil, nil).want(401)
	e.do("GET", "/api/v0/mobile/nodes/pc1/browser/matches", nil, bearer(owner)).want(400)
	if f.count() != 1 {
		t.Fatalf("the node was asked %d times", f.count())
	}
}

func TestBrowserMatchesWhenTheAgentDoesNotAnswer(t *testing.T) {
	old := browserMatchTimeout
	browserMatchTimeout = 200 * time.Millisecond
	t.Cleanup(func() { browserMatchTimeout = old })
	e := newEnv(t, envOpts{})
	owner := e.auth.addUser(7, "hh1", authn.RoleMember)
	e.createNode("pc1", "hh1")
	if _, err := e.m.deps.DB.Write.Exec(`UPDATE cc_nodes SET owner_user_id = 7 WHERE node_id = 'pc1'`); err != nil {
		t.Fatal(err)
	}
	got := e.do("GET", "/api/v0/mobile/nodes/pc1/browser/matches?pattern=(x)", nil, bearer(owner)).want(200).json()
	if errObj, _ := got["error"].(map[string]any); got["ok"] != false || errObj["code"] != "no_answer" {
		t.Fatalf("got %v", got)
	}
}
