package cc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// TestLatestReleaseByNodeKind (#487): a Go node is offered this server's channel of jarvis-server
// releases, never the Python node's jarvis-node-setup release, and never a version it already runs.
func TestLatestReleaseByNodeKind(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/alexberardi/jarvis-node-setup/releases/latest":
			w.Write([]byte(`{"tag_name":"v0.3.6","published_at":"2026-09-01T00:00:00Z"}`))
		case "/repos/alexberardi/jarvis-server/releases":
			// Newest first, as GitHub lists them; rc20 is published but newer than this server.
			w.Write([]byte(`[
				{"tag_name":"v0.1.0-rc20","prerelease":true,"published_at":"2026-10-23T00:00:00Z"},
				{"tag_name":"v0.1.0-rc19","prerelease":true,"published_at":"2026-10-21T00:00:00Z"},
				{"tag_name":"v0.1.0-rc18","prerelease":true,"published_at":"2026-10-14T00:00:00Z"},
				{"tag_name":"v0.0.9","published_at":"2026-08-01T00:00:00Z"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	e := newEnv(t, envOpts{github: gh.URL})
	e.m.Version = "v0.1.0-rc19"
	if err := e.m.settings.Set(context.Background(), settingUpdatesAllowCheck, true, settingsScope("")); err != nil {
		t.Fatal(err)
	}
	owner := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleMember)
	goNode, pyNode := e.createNode("n1", "hh1"), e.createNode("n2", "hh1")
	hb := func(n testNode, version, runtime string) {
		vi := map[string]any{"version": version, "install_mode": "tarball"}
		if runtime != "" {
			vi["runtime"] = runtime
		}
		e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"version_info": vi}, n.h()).want(200)
	}
	latest := func(query string, h hdr) string {
		return strings.TrimSpace(string(e.do("GET", "/api/v0/releases/latest"+query, nil, h).want(200).body))
	}
	hb(goNode, "v0.1.0-rc18", "go")
	hb(pyNode, "0.3.5", "")

	// A Go node at rc18 is offered rc19 (the server's own release, not rc20 past it), never 0.3.6.
	got := latest("?node_id=n1", bearer(owner))
	if !strings.Contains(got, `"tag":"v0.1.0-rc19"`) || !strings.Contains(got, `"kind":"go"`) || strings.Contains(got, "0.3.6") {
		t.Fatalf("Go node at rc18 offered %s, want v0.1.0-rc19 (kind go)", got)
	}
	// The legacy Python node still follows jarvis-node-setup.
	if got := latest("?node_id=n2", bearer(owner)); !strings.Contains(got, `"tag":"v0.3.6"`) || !strings.Contains(got, `"kind":"python"`) {
		t.Fatalf("Python node at 0.3.5 offered %s, want v0.3.6 (kind python)", got)
	}
	// Without a node: the old answer, so an app that sends none keeps working.
	if got := latest("", nil); !strings.Contains(got, `"tag":"v0.3.6"`) {
		t.Fatalf("no node_id: %s", got)
	}
	// A node's kind and version are its household's: a node_id needs a member's token.
	e.do("GET", "/api/v0/releases/latest?node_id=n1", nil, nil).want(401)
	e.do("GET", "/api/v0/releases/latest?node_id=n1", nil, bearer(stranger)).want(403)
	e.do("GET", "/api/v0/releases/latest?node_id=nope", nil, bearer(owner)).want(404)

	// Never a version the node already runs (or newer than it): null.
	hb(goNode, "v0.1.0-rc19", "go")
	if got := latest("?node_id=n1", bearer(owner)); got != "null" {
		t.Fatalf("Go node at rc19 offered %s, want null", got)
	}
	hb(goNode, "v0.1.0", "go")
	if got := latest("?node_id=n1", bearer(owner)); got != "null" {
		t.Fatalf("Go node at 0.1.0 offered %s, want null", got)
	}
	hb(pyNode, "0.3.6", "")
	if got := latest("?node_id=n2", bearer(owner)); got != "null" {
		t.Fatalf("Python node at 0.3.6 offered %s, want null", got)
	}

	// A release server follows the release channel: no prereleases.
	hb(goNode, "v0.0.8", "go")
	e.m.Version = "v0.1.0"
	e.m.serverReleases = &serverReleaseCache{}
	if got := latest("?node_id=n1", bearer(owner)); !strings.Contains(got, `"tag":"v0.0.9"`) {
		t.Fatalf("Go node at v0.0.8 under a v0.1.0 server offered %s, want v0.0.9 (no prereleases)", got)
	}
	// A development build has no release for its agents to follow.
	e.m.Version = "dev"
	e.m.serverReleases = &serverReleaseCache{}
	if got := latest("?node_id=n1", bearer(owner)); got != "null" {
		t.Fatalf("dev server offered %s, want null", got)
	}
}
