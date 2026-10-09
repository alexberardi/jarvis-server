package cc

import (
	"bytes"
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// enablePantry turns the household's Pantry on (pantry.enabled defaults to false).
func enablePantry(t *testing.T, e *env, hh string, on bool) {
	t.Helper()
	if err := e.m.Settings().Set(context.Background(), settingPantryEnabled, on, settings.Scope{HouseholdID: hh}); err != nil {
		t.Fatal(err)
	}
}

const pantryDisabledBody = `{"code":"pantry_disabled","detail":"` + pantryDisabledDetail + `"}`

func TestPantryEnabledHouseholdSetting(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(5, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(6, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(7, "hh2", authn.RoleOwner)
	const get = "/api/v0/mobile/household/hh1/settings"
	const put = get + "/pantry.enabled"

	// Default off, readable by any member.
	s := e.do("GET", get, nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if v, ok := s["pantry.enabled"]; !ok || v != false {
		t.Fatal(s)
	}

	// Only a household admin writes it.
	e.do("PUT", put, map[string]any{"value": true}, bearer(member)).detail(403, "User has member role, requires admin or higher")
	e.do("PUT", put, map[string]any{"value": true}, bearer(outsider)).detail(403, "User is not a member of this household")
	r := e.do("PUT", put, map[string]any{"value": true}, bearer(admin)).want(200)
	if got := string(bytes.TrimSpace(r.body)); got != `{"success":true,"key":"pantry.enabled","value":true}` {
		t.Fatal(got)
	}
	s = e.do("GET", get, nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["pantry.enabled"] != true {
		t.Fatal(s)
	}
	if e.m.Settings().Bool(context.Background(), settingPantryEnabled, settings.Scope{HouseholdID: "hh2"}) {
		t.Fatal("leaked to hh2")
	}
	e.do("PUT", put, map[string]any{"value": false}, bearer(admin)).want(200)
	if e.m.pantryEnabled(context.Background(), "hh1") {
		t.Fatal("still on")
	}
}

func TestPantryGateRefusesWhenOff(t *testing.T) {
	e := newEnv(t)
	pantry := newFakePantry(t)
	setPantry(t, e, "hh1", pantry.URL)
	enablePantry(t, e, "hh1", false) // back to the default: off
	member := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")

	install := map[string]any{"command_name": "weather", "github_repo_url": "https://example.com/r.git"}
	ti := map[string]any{"share_code": "ABC123"}
	wantOff := func(r *resp) {
		t.Helper()
		if got := string(bytes.TrimSpace(r.body)); r.status != 403 || got != pantryDisabledBody {
			t.Fatalf("%d %s", r.status, got)
		}
	}

	// Auth and membership come first: a stranger learns nothing about the setting.
	e.do("POST", "/api/v0/nodes/n1/package-install", install, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("POST", "/api/v0/nodes/n1/test-install", ti, bearer(stranger)).detail(403, "User is not a member of this household")

	// Off (the default): installs and Forge test installs are refused, nothing is published,
	// Pantry is never asked, no row is written. The admin key doesn't bypass a household's choice.
	wantOff(e.do("POST", "/api/v0/nodes/n1/package-install", install, bearer(member)))
	wantOff(e.do("POST", "/api/v0/nodes/n1/package-install", install, adminH()))
	wantOff(e.do("POST", "/api/v0/nodes/n1/test-install", ti, bearer(member)))
	wantOff(e.do("POST", "/api/v0/nodes/n1/test-install", map[string]any{"share_code": "bad"}, bearer(member)))
	if pantry.lastAsked() != "" {
		t.Fatal("Pantry contacted while disabled")
	}
	var rows int
	_ = e.d.Read.QueryRow(`SELECT (SELECT COUNT(*) FROM cc_package_install_requests) + (SELECT COUNT(*) FROM cc_test_install_requests)`).Scan(&rows)
	if rows != 0 {
		t.Fatal(rows)
	}

	// Uninstall and revert of what's already installed keep working, as does their poll.
	un := e.do("POST", "/api/v0/nodes/n1/package-uninstall", map[string]any{"command_name": "weather", "component_type": "command"}, bearer(member)).want(201).json()
	if pk := c.next(); pk.TopicName != "jarvis/nodes/n1/package-uninstall" {
		t.Fatal(pk.TopicName)
	}
	e.do("GET", "/api/v0/nodes/n1/package-uninstall/"+un["id"].(string), nil, bearer(member)).want(200)
	e.do("POST", "/api/v0/nodes/n1/package-revert", map[string]any{"command_name": "weather"}, bearer(member)).want(201)
	if pk := c.next(); pk.TopicName != "jarvis/nodes/n1/package-revert" {
		t.Fatal(pk.TopicName)
	}

	// On: both go through.
	enablePantry(t, e, "hh1", true)
	e.do("POST", "/api/v0/nodes/n1/package-install", install, bearer(member)).want(201)
	if pk := c.next(); pk.TopicName != "jarvis/nodes/n1/package-install" {
		t.Fatal(pk.TopicName)
	}
	e.do("POST", "/api/v0/nodes/n1/test-install", ti, bearer(member)).want(201)
	if pantry.lastAsked() != "ABC123" {
		t.Fatal(pantry.lastAsked())
	}

	// Per household: hh2 stays off.
	e.createNode("n3", "hh2")
	wantOff(e.do("POST", "/api/v0/nodes/n3/package-install", install, bearer(stranger)))
}
