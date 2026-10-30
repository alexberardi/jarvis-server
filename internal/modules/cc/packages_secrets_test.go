package cc

import (
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const secretsBase = "/api/v0/households/hh1/package-secrets"

func pullSecrets(e *env, n testNode) map[string]any {
	e.t.Helper()
	return e.do("GET", "/api/v0/nodes/"+n.id+"/package-secrets", nil, n.h()).want(200).json()["secrets"].(map[string]any)
}

func TestPackageSecretsSetPullAndDelete(t *testing.T) {
	e := newEnv(t)
	admin := bearer(e.auth.addUser(1, "hh1", authn.RoleAdmin))
	member := bearer(e.auth.addUser(2, "hh1", authn.RoleMember))
	n1, n2 := e.createNode("n1", "hh1"), e.createNode("n2", "hh1")
	other := e.createNode("n3", "hh2")
	c := e.dialNode(n1)
	c.subscribe("jarvis/nodes/n1/#")

	// Only a household admin sets, lists or deletes; a member cannot even list the names.
	body := map[string]any{"value": "http://10.0.0.9:8123"}
	e.do("PUT", secretsBase+"/HA_URL", body, member).detail(403, "User has member role, requires admin or higher")
	e.do("GET", secretsBase, nil, member).detail(403, "User has member role, requires admin or higher")
	e.do("DELETE", secretsBase+"/HA_URL", nil, member).detail(403, "User has member role, requires admin or higher")
	e.do("PUT", secretsBase+"/ha_url", body, admin).want(400)                        // not a package secret name
	e.do("PUT", secretsBase+"/HA_URL", map[string]any{"value": ""}, admin).want(400) // delete to clear
	e.do("PUT", secretsBase+"/HA_URL", map[string]any{"value": strings.Repeat("x", 9<<10)}, admin).want(400)
	e.do("PUT", secretsBase+"/HA_URL", map[string]any{"value": "v", "node_ids": []string{"n3"}}, admin).want(404) // another household's node

	// Set for every node of the household: the node is nudged (no secret in it) and pulls it.
	e.do("PUT", secretsBase+"/HA_URL", body, admin).want(200)
	if pk := c.next(); pk.TopicName != "jarvis/nodes/n1/package-secrets" || strings.Contains(string(pk.Payload), "10.0.0.9") {
		t.Fatalf("%s %s", pk.TopicName, pk.Payload)
	}
	for _, n := range []testNode{n1, n2} {
		if got := pullSecrets(e, n); got["HA_URL"] != "http://10.0.0.9:8123" || len(got) != 1 {
			t.Fatalf("%s pulled %v", n.id, got)
		}
	}
	if got := pullSecrets(e, other); len(got) != 0 {
		t.Fatalf("another household's node pulled %v", got)
	}

	// Never stored or listed in the clear.
	var enc string
	if err := e.d.Read.QueryRow(`SELECT value_enc FROM cc_package_secrets WHERE secret_key = 'HA_URL'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if enc == "" || strings.Contains(enc, "10.0.0.9") {
		t.Fatalf("stored as %q", enc)
	}
	list := e.do("GET", secretsBase, nil, admin).want(200)
	if strings.Contains(string(list.body), "10.0.0.9") || !strings.Contains(string(list.body), `"HA_URL"`) {
		t.Fatalf("%s", list.body)
	}

	// One node only: n2 stops seeing it, and a node may only pull as itself.
	e.do("PUT", secretsBase+"/KEEP_TOKEN", map[string]any{"value": "tok", "node_ids": []string{"n1"}}, admin).want(200)
	if got := pullSecrets(e, n1); got["KEEP_TOKEN"] != "tok" {
		t.Fatal(got)
	}
	if got := pullSecrets(e, n2); got["KEEP_TOKEN"] != nil {
		t.Fatalf("n2 pulled a secret meant for n1: %v", got)
	}
	e.do("GET", "/api/v0/nodes/n1/package-secrets", nil, n2.h()).want(403)
	e.do("GET", "/api/v0/nodes/n1/package-secrets", nil, hdr{}).want(400) // no node key: the node-auth header check

	// Deleting leaves a tombstone: the node is told "" to clear its copy; the list drops it.
	e.do("DELETE", secretsBase+"/HA_URL", nil, admin).want(200)
	e.do("DELETE", secretsBase+"/HA_URL", nil, admin).want(404)
	if got := pullSecrets(e, n1); got["HA_URL"] != "" {
		t.Fatalf("a deleted secret should pull as empty: %v", got)
	}
	if strings.Contains(string(e.do("GET", secretsBase, nil, admin).want(200).body), "HA_URL") {
		t.Fatal("a deleted secret is listed")
	}

	// The tombstone is swept after 30 days.
	e.advance(31 * 24 * time.Hour)
	if err := e.m.cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := pullSecrets(e, n1)["HA_URL"]; ok {
		t.Fatal("tombstone survived the sweep")
	}
}
