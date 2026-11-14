package cc

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Several pantries, slice 2: docs/decisions/2026-11-11-several-pantries.md decisions 4, 5, 8, 11, 14.

const (
	storeA = "aaaaaaaa-0000-4000-8000-000000000001"
	storeB = "bbbbbbbb-0000-4000-8000-000000000002"
)

func setStores(t *testing.T, e *env, hh string, stores ...PantryStore) {
	t.Helper()
	v := []any{}
	for _, s := range stores {
		v = append(v, map[string]any{"id": s.ID, "url": s.URL, "name": s.Name})
	}
	if err := e.m.Settings().Set(context.Background(), settingPantryStores, v, settings.Scope{HouseholdID: hh}); err != nil {
		t.Fatal(err)
	}
}

func (r *resp) code(status int, code string) {
	r.t.Helper()
	r.want(status)
	if got := r.json()["code"]; got != code {
		r.t.Fatalf("code %v, want %q: %s", got, code, r.body)
	}
}

func recordedStore(t *testing.T, e *env, node, name string) string {
	t.Helper()
	var id string
	_ = e.d.Read.QueryRow(`SELECT pantry_store_id FROM cc_node_package_stores WHERE node_id = ? AND command_name = ?`,
		node, name).Scan(&id)
	return id
}

func TestPackageProvenance(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	a := PantryStore{storeA, "http://a.lan:7721/", "Store A"}
	b := PantryStore{storeB, "http://b.lan:7721", "Store B"}
	setStores(t, e, "hh1", a, b)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/package-install"
	install := func(pick string) *resp {
		body := map[string]any{"command_name": "weather", "github_repo_url": "https://example.com/r.git"}
		if pick != "" {
			body["pantry_store_id"] = pick
		}
		return e.do("POST", base, body, bearer(member))
	}
	complete := func(path, rid string) {
		t.Helper()
		e.do("POST", path+"/"+rid+"/results", map[string]any{"success": true}, n.h()).want(200)
	}
	verifyURL := func(rid string) any {
		t.Helper()
		return e.do("GET", base+"/"+rid+"/verify", nil, n.h()).want(200).json()["pantry_url"]
	}

	// Decision 14: only a store in the household's list.
	install("cccccccc-0000-4000-8000-000000000003").code(422, pantryStoreUnknownCode)

	// No pick and nothing recorded: the default, the first (decision 2).
	rid := install("").want(201).json()["id"].(string)
	if got := verifyURL(rid); got != "http://a.lan:7721" {
		t.Fatalf("default store: %v", got)
	}
	// A failed install records nothing.
	e.do("POST", base+"/"+rid+"/results", map[string]any{"success": false}, n.h()).want(200)
	if got := recordedStore(t, e, "n1", "weather"); got != "" {
		t.Fatalf("failed install recorded %q", got)
	}

	// The picked store: the node is sent there, and a completed install records it (decision 5).
	rid = install(storeB).want(201).json()["id"].(string)
	if got := verifyURL(rid); got != "http://b.lan:7721" {
		t.Fatalf("picked store: %v", got)
	}
	complete(base, rid)
	if got := recordedStore(t, e, "n1", "weather"); got != storeB {
		t.Fatalf("recorded %q, want B", got)
	}

	// An update (no pick) goes back to B; a pick of another store is refused naming B (decision 8).
	rid = install("").want(201).json()["id"].(string)
	if p := pkgRow(t, e, rid); p.storeID.String != storeB {
		t.Fatalf("update went to %q", p.storeID.String)
	}
	r := install(storeA)
	r.code(409, pantryStoreConflictCode)
	if d := r.json()["detail"]; d != "weather is already installed from Store B. Uninstall it first to install it from another store." {
		t.Fatal(d)
	}

	// Editing B's URL moves the pending request with it: the store is its id (decision 11).
	b.URL = "http://b2.lan:7721"
	setStores(t, e, "hh1", a, b)
	if got := verifyURL(rid); got != "http://b2.lan:7721" {
		t.Fatalf("edited store: %v", got)
	}

	// B removed: the package has no updates until uninstalled (decisions 5, 11).
	setStores(t, e, "hh1", a)
	install("").code(409, pantryStoreRemovedCode)
	install(storeA).code(409, pantryStoreRemovedCode)
	pkgs := []any{map[string]any{"name": "weather"}, map[string]any{"name": "clock"}}
	if err := e.m.annotatePackageStores(context.Background(), "n1", "hh1", pkgs); err != nil {
		t.Fatal(err)
	}
	if w := pkgs[0].(map[string]any); w["pantry_store_id"] != storeB || w["pantry_store_removed"] != true || w["pantry_store_name"] != nil {
		t.Fatal(w)
	}
	if c := pkgs[1].(map[string]any); len(c) != 1 { // nothing recorded: as the node reported it
		t.Fatal(c)
	}

	// Uninstall forgets it; then it installs from A, which is named in the node tools view.
	urid := e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "weather", "component_type": "command"},
		bearer(member)).want(201).json()["id"].(string)
	complete("/api/v0/nodes/n1/package-uninstall", urid)
	if got := recordedStore(t, e, "n1", "weather"); got != "" {
		t.Fatalf("uninstall left %q", got)
	}
	complete(base, install(storeA).want(201).json()["id"].(string))
	pkgs = []any{map[string]any{"name": "weather"}}
	if err := e.m.annotatePackageStores(context.Background(), "n1", "hh1", pkgs); err != nil {
		t.Fatal(err)
	}
	if w := pkgs[0].(map[string]any); w["pantry_store_id"] != storeA || w["pantry_store_name"] != "Store A" || w["pantry_store_removed"] != nil {
		t.Fatal(w)
	}
}

func TestTestInstallUsesPickedStore(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	pa, pb := newFakePantry(t), newFakePantry(t)
	setStores(t, e, "hh1", PantryStore{storeA, pa.URL, "Store A"}, PantryStore{storeB, pb.URL, "Store B"})
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	const base = "/api/v0/nodes/n1/test-install"

	e.do("POST", base, map[string]any{"share_code": "ABC123", "pantry_store_id": "nope"}, bearer(member)).
		code(422, pantryStoreUnknownCode)
	// The code goes to the picked store only (decision 4).
	rid := e.do("POST", base, map[string]any{"share_code": "ABC123", "pantry_store_id": storeB}, bearer(member)).
		want(201).json()["id"].(string)
	pa.set(func() {
		if len(pa.asked) != 0 {
			t.Fatalf("store A was asked %v", pa.asked)
		}
	})
	v := e.do("GET", base+"/"+rid+"/verify", nil, n.h()).want(200).json()
	if v["pantry_download_url"] != pb.URL+"/v1/forge/drafts/ABC123" {
		t.Fatal(v)
	}
	complete := e.do("POST", base+"/"+rid+"/results", map[string]any{"success": true}, n.h())
	complete.want(200)
	// Send to Forge goes back to the same store.
	e.do("POST", base+"/"+rid+"/feedback", map[string]any{}, bearer(member)).want(200)
	pb.set(func() {
		if pb.feedback["ABC123"] == "" {
			t.Fatal("feedback did not reach store B")
		}
	})
	pa.set(func() {
		if len(pa.feedback) != 0 {
			t.Fatal("feedback reached store A")
		}
	})
	// A test install doesn't make the package installed from a store.
	if got := recordedStore(t, e, "n1", "weather_test"); got != "" {
		t.Fatal(got)
	}
}

func TestStoreBrowsePicksStore(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	enablePantry(t, e, "hh1", true)
	pa, pb := newFakePantry(t), newFakePantry(t)
	pb.set(func() { pb.commands[""] = `{"commands":[]}` })
	setStores(t, e, "hh1", PantryStore{storeA, pa.URL, "Store A"}, PantryStore{storeB, pb.URL, "Store B"})
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	const base = "/api/v0/households/hh1/pantry/commands"

	e.do("GET", base+"?pantry_store_id=nope", nil, bearer(member)).code(422, pantryStoreUnknownCode)
	e.do("GET", base+"?pantry_store_id="+storeB, nil, bearer(member)).want(200)
	pa.set(func() {
		if len(pa.asked) != 0 {
			t.Fatalf("store A was asked %v", pa.asked)
		}
	})
	// No pick: the default, which names itself when it can't answer.
	e.do("GET", base+"/weather", nil, bearer(member)).detail(404, "The Pantry store Store A has no package weather")
}

// A package with a daemon (a chat channel) runs on one node of the household: once a node's
// install result names a daemon, installing it on another node is refused, naming the host.
// Updating it where it is, another household's node and an uninstall-then-move are not.
func TestADaemonPackageRunsOnOneNode(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	for _, hh := range []string{"hh1", "hh2"} {
		enablePantry(t, e, hh, true)
		setStores(t, e, hh, PantryStore{storeA, "http://a.lan:7721", "Store A"})
	}
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	other := e.auth.addUser(2, "hh2", authn.RoleMember)
	nodes := map[string]testNode{"n1": e.createNode("n1", "hh1"), "n2": e.createNode("n2", "hh1"), "n3": e.createNode("n3", "hh2")}
	install := func(node, pkg string, as string) *resp {
		return e.do("POST", "/api/v0/nodes/"+node+"/package-install",
			map[string]any{"command_name": pkg, "github_repo_url": "https://example.com/r.git"}, bearer(as))
	}
	finish := func(op, node, rid string, details map[string]any) {
		t.Helper()
		e.do("GET", "/api/v0/nodes/"+node+"/package-install/"+rid+"/verify", nil, nodes[node].h()).want(200) // shared by every op
		e.do("POST", "/api/v0/nodes/"+node+"/"+op+"/"+rid+"/results", map[string]any{"success": true, "details": details}, nodes[node].h()).want(200)
	}
	daemon := map[string]any{"package_name": "channel_discord", "version": "1.0.0", "components": 1, "daemons": []any{"discord"}}

	// A package without a daemon installs on both nodes.
	for _, node := range []string{"n1", "n2"} {
		rid := install(node, "weather", member).want(201).json()["id"].(string)
		finish("package-install", node, rid, map[string]any{"package_name": "weather", "version": "1.0.0", "components": 1})
	}

	rid := install("n1", "channel_discord", member).want(201).json()["id"].(string)
	finish("package-install", "n1", rid, daemon)
	r := install("n2", "channel_discord", member)
	r.code(409, daemonHostConflictCode)
	if d, _ := r.json()["detail"].(string); !strings.Contains(d, "already on kitchen") {
		t.Fatalf("detail = %q", d)
	}
	install("n1", "channel_discord", member).want(201) // an update where it runs
	install("n3", "channel_discord", other).want(201)  // another household's own bot

	// Uninstalled from n1, it can move to n2.
	rid = e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "channel_discord", "component_type": ""},
		bearer(member)).want(201).json()["id"].(string)
	finish("package-uninstall", "n1", rid, nil)
	install("n2", "channel_discord", member).want(201)
}
