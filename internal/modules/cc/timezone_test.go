package cc

import (
	"bytes"
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestValidateTimezoneSetting(t *testing.T) {
	for _, ok := range []string{"", "  ", "UTC", "America/New_York", "Asia/Kolkata", " Europe/London "} {
		if err := validateTimezoneSetting(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []any{"Local", "Mars/Olympus", "../etc/passwd", 5.0, true} {
		if err := validateTimezoneSetting(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// HouseholdClock > household.timezone > most recently seen node > "".
func TestHouseholdTimezonePrecedence(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	e.createNode("n1", "hh1")
	set := func(v any) {
		t.Helper()
		if err := e.m.Settings().Set(ctx, settingHouseholdTimezone, v, settings.Scope{HouseholdID: "hh1"}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(tz, source, node string) {
		t.Helper()
		gtz, gsrc, gnode := e.m.resolveHouseholdTimezone(ctx, "hh1")
		if gtz != tz || gsrc != source || gnode != node || e.m.householdTimezone(ctx, "hh1") != tz {
			t.Fatalf("got %q %q %q, want %q %q %q", gtz, gsrc, gnode, tz, source, node)
		}
	}
	expect("", tzSourceDefault, "")
	set("Asia/Tokyo")
	expect("Asia/Tokyo", tzSourceSetting, "")
	e.m.recordNodeTimezone(ctx, "n1", "America/Chicago")
	expect("Asia/Tokyo", tzSourceSetting, "America/Chicago")
	set("") // automatic
	expect("America/Chicago", tzSourceNode, "America/Chicago")
	set(nil)
	expect("America/Chicago", tzSourceNode, "America/Chicago")
	// A stored value that no longer loads (written around the validator) is ignored.
	if _, err := e.d.Write.Exec(`UPDATE cc_settings SET value = 'Mars/Olympus' WHERE key = ? AND household_id = 'hh1'`,
		settingHouseholdTimezone); err != nil {
		t.Fatal(err)
	}
	expect("America/Chicago", tzSourceNode, "America/Chicago")
	// The setting is per household.
	set("Asia/Tokyo")
	if tz := e.m.householdTimezone(ctx, "hh2"); tz != "" {
		t.Fatalf("leaked to hh2: %q", tz)
	}
	// Attention's location follows.
	if loc := e.m.householdLocation(ctx, "hh1"); loc.String() != "Asia/Tokyo" {
		t.Fatal(loc)
	}
	e.m.HouseholdClock = fixedClock("Europe/Paris")
	if tz := e.m.householdTimezone(ctx, "hh1"); tz != "Europe/Paris" {
		t.Fatalf("override %q", tz)
	}
}

// A turn uses the node-reported zone unless the household set one; the node's own zone is
// recorded either way.
func TestTurnTimezoneUsesHouseholdSetting(t *testing.T) {
	ve := newVoiceEnv(t, prompts.Qwen3_8B)
	ctx := context.Background()
	hh := settings.Scope{HouseholdID: voiceHH}
	ve.set(settingAmbientContext, true, hh)

	// Unset: exactly the reported zone (12:00 UTC is 8:00 in New York).
	ve.start("t0", weatherTool)
	if c := ve.m.convs.get("t0"); c.timezone != "America/New_York" || c.ambient != "As of 8:00 AM, Tuesday, Oct 6." {
		t.Fatalf("%q %q", c.timezone, c.ambient)
	}
	ve.set(settingHouseholdTimezone, "Asia/Tokyo", hh)
	ve.start("t1", weatherTool)
	if c := ve.m.convs.get("t1"); c.timezone != "Asia/Tokyo" || c.ambient != "As of 9:00 PM, Tuesday, Oct 6." {
		t.Fatalf("%q %q", c.timezone, c.ambient)
	}
	if node := ve.m.nodeTimezone(ctx, voiceHH); node != "America/New_York" {
		t.Fatalf("node zone not recorded: %q", node)
	}
	// The node's date-context endpoint follows the same rule.
	dc := ve.do("GET", "/api/v0/generate/date-context?timezone=UTC", nil, ve.node.h()).want(200).json()
	if tz := dc["timezone"].(map[string]any)["user_timezone"]; tz != "Asia/Tokyo" {
		t.Fatalf("date-context %v", tz)
	}
	ve.set(settingHouseholdTimezone, "", hh)
	dc = ve.do("GET", "/api/v0/generate/date-context?timezone=Europe/London", nil, ve.node.h()).want(200).json()
	if tz := dc["timezone"].(map[string]any)["user_timezone"]; tz != "Europe/London" {
		t.Fatalf("date-context %v", tz)
	}
}

func TestHouseholdTimezoneSettingPut(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(5, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(6, "hh1", authn.RoleMember)
	put := func(tok, body string) *resp {
		return e.do("PUT", "/api/v0/mobile/household/hh1/settings/household.timezone", body, bearer(tok))
	}
	put(member, `{"value": "Asia/Tokyo"}`).detail(403, "User has member role, requires admin or higher")
	cases := []struct {
		body, want string
		status     int
	}{
		{`{"value": " Asia/Tokyo "}`, `{"success":true,"key":"household.timezone","value":"Asia/Tokyo"}`, 200},
		{`{"value": "Local"}`, `{"detail":"Invalid value for household.timezone: 'Local' is not a known IANA time zone"}`, 400},
		{`{"value": "Mars/Olympus"}`, `{"detail":"Invalid value for household.timezone: 'Mars/Olympus' is not a known IANA time zone"}`, 400},
		{`{"value": 5}`, `{"detail":"Invalid value for household.timezone: expected a string"}`, 400},
	}
	for _, c := range cases {
		r := put(admin, c.body)
		if r.status != c.status || string(bytes.TrimSpace(r.body)) != c.want {
			t.Fatalf("%s: %d %s", c.body, r.status, r.body)
		}
	}
	s := e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["household.timezone"] != "Asia/Tokyo" {
		t.Fatal(s)
	}
	// "" clears it back to automatic.
	put(admin, `{"value": ""}`).want(200)
	s = e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["household.timezone"] != "" {
		t.Fatal(s)
	}
}

func TestHouseholdTimezoneEndpoint(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	ctx := context.Background()
	member := e.auth.addUser(5, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(6, "hh2", authn.RoleOwner)
	e.createNode("n1", "hh1")
	e.createNode("n2", "hh2")
	e.m.recordNodeTimezone(ctx, "n2", "Europe/London")
	const path = "/api/v0/mobile/household/hh1/timezone"
	get := func() string {
		return string(bytes.TrimSpace(e.do("GET", path, nil, bearer(member)).want(200).body))
	}
	if got := get(); got != `{"household_id":"hh1","timezone":"","source":"default","node_timezone":""}` {
		t.Fatal(got)
	}
	e.m.recordNodeTimezone(ctx, "n1", "America/New_York")
	if got := get(); got != `{"household_id":"hh1","timezone":"America/New_York","source":"node","node_timezone":"America/New_York"}` {
		t.Fatal(got)
	}
	if err := e.m.Settings().Set(ctx, settingHouseholdTimezone, "Asia/Tokyo", settings.Scope{HouseholdID: "hh1"}); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != `{"household_id":"hh1","timezone":"Asia/Tokyo","source":"setting","node_timezone":"America/New_York"}` {
		t.Fatal(got)
	}
	e.do("GET", path, nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("GET", path, nil, nil).want(401)
}
