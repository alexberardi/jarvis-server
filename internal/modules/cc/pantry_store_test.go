package cc

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const newsCommit = "29257531cfa7e0cf14d0e05021cdb480a4dd7e84"

// The admin Store page's Pantry reads (decision 2026-10-30-admin-store-page).
func TestPantryStoreReads(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(2, "hh2", authn.RoleOwner)
	pantry.set(func() {
		pantry.commands[""] = `{"commands":[{"command_name":"get_news"}],"total":1,"page":1,"per_page":20}`
		pantry.commands["get_news"] = `{"command_name":"get_news","github_repo_url":"https://github.com/acme/news","apt_packages":[]}`
		// The git_tag in /versions need not exist in the repository; the commit in /download does.
		pantry.commands["get_news/versions"] = `{"versions":[{"git_tag":"v1.1.0"}]}`
		pantry.commands["get_news/download"] = `{"git_tag":"` + newsCommit + `","git_commit_sha":"` + newsCommit + `"}`
		pantry.commands["untagged"] = `{"command_name":"untagged","github_repo_url":"https://github.com/acme/u"}`
	})
	const base = "/api/v0/households/hh1/pantry/commands"

	// The list is Pantry's, with only the known query parameters passed on.
	b := e.do("GET", base+"?q=news&page=2&evil=1", nil, bearer(member)).want(200).json()
	if b["total"] != float64(1) {
		t.Fatal(b)
	}
	if got := pantry.lastAsked(); got != "?page=2&q=news" {
		t.Fatal(got)
	}

	// The detail carries the latest version's commit.
	d := e.do("GET", base+"/get_news", nil, bearer(member)).want(200).json()
	if d["github_repo_url"] != "https://github.com/acme/news" || d["git_tag"] != newsCommit {
		t.Fatal(d)
	}
	if d = e.do("GET", base+"/untagged", nil, bearer(member)).want(200).json(); d["git_tag"] != nil {
		t.Fatal(d)
	}
	if b = e.do("GET", base+"/nope", nil, bearer(member)).want(404).json(); b["code"] != packageNotFoundCode {
		t.Fatal(b)
	}

	// Another household's member learns nothing, and Pantry isn't asked.
	pantry.set(func() { pantry.asked = nil })
	e.do("GET", base, nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("GET", base+"/get_news", nil, bearer(outsider)).detail(403, "User is not a member of this household")
	if pantry.lastAsked() != "" {
		t.Fatal("asked Pantry for an outsider")
	}

	// Pantry down is a 503 with its code.
	pantry.set(func() { pantry.status = 500 })
	if b = e.do("GET", base, nil, bearer(member)).want(503).json(); b["code"] != pantryUnavailableCode {
		t.Fatal(b)
	}

	// The household's Pantry switch applies.
	enablePantry(t, e, "hh1", false)
	if b = e.do("GET", base, nil, bearer(member)).want(403).json(); b["code"] != "pantry_disabled" {
		t.Fatal(b)
	}
}

func TestPantryDefaultBundleSetting(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(5, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(6, "hh1", authn.RoleMember)
	const put = "/api/v0/mobile/household/hh1/settings/pantry.default_bundle"

	e.do("PUT", put, map[string]any{"value": "get_news,home-assistant"}, bearer(member)).want(403)
	e.do("PUT", put, map[string]any{"value": "get_news,home-assistant"}, bearer(admin)).want(200)
	s := e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["pantry.default_bundle"] != "get_news,home-assistant" {
		t.Fatal(s)
	}
	e.do("PUT", put, map[string]any{"value": ""}, bearer(admin)).want(200)
	for _, bad := range []string{"get_news,", "Get_News", "a b", "../x", ",x"} {
		e.do("PUT", put, map[string]any{"value": bad}, bearer(admin)).want(400)
	}
}
