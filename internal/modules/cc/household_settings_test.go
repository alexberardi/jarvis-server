package cc

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestHouseholdSettingsAllowlistDeclared(t *testing.T) {
	defs := map[string]settings.Type{}
	for _, d := range Definitions() {
		defs[d.Key] = d.Type
	}
	if len(householdControllable) != 18 {
		t.Fatal(len(householdControllable))
	}
	for _, s := range householdControllable {
		ty, ok := defs[s.key]
		if !ok || string(ty) != s.typ {
			t.Fatalf("%s: declared %v as %q, allowlisted as %s", s.key, ok, ty, s.typ)
		}
	}
}

func TestHouseholdSettingsGet(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(5, "hh1", authn.RoleMember)
	outsider := e.auth.addUser(6, "hh2", authn.RoleOwner)
	r := e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200)
	want := `{"household_id":"hh1","settings":{"web_search.enabled":false,"web_scraping.allow_external":false,` +
		`"proposals.enabled":false,"phone_calls.enabled":false,"phone_calls.plan_ttl_minutes":20,` +
		`"phone_calls.audio_retention_days":30,"phone_calls.max_call_seconds":600,"phone_calls.calls_per_day":10,` +
		`"phone_calls.monthly_minutes_cap":60,"phone_calls.max_concurrent_calls":1,"household.location":"",` +
		`"persona.household_prompt":` + jsonStr(prompts.DefaultPersona) + `,"memory.enabled":true,"memory.extraction_enabled":true,` +
		`"household.timezone":"","phone.twilio_account_sid":null,"phone.twilio_auth_token":null,"phone.twilio_from_number":null}}`
	if got := string(bytes.TrimSpace(r.body)); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(outsider)).detail(403, "User is not a member of this household")
	e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, nil).want(401)

	// An uncoercible stored value reads as null; another household's value doesn't leak.
	ctx := context.Background()
	_, _ = e.d.Write.Exec(`INSERT INTO cc_settings (key, value, value_type, household_id) VALUES ('phone_calls.calls_per_day', 'lots', 'string', 'hh1')`)
	_ = e.m.Settings().Set(ctx, settingWebSearch, true, settings.Scope{HouseholdID: "hh2"})
	s := e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["phone_calls.calls_per_day"] != nil || s["web_search.enabled"] != false {
		t.Fatal(s)
	}
}

func TestHouseholdSettingsPut(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(5, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(6, "hh1", authn.RoleMember)
	put := func(tok, key string, body any) *resp {
		return e.do("PUT", "/api/v0/mobile/household/hh1/settings/"+key, body, bearer(tok))
	}
	// Allowlist before role: a non-admin probing an unlisted key gets 404, not 403.
	put(member, "llm.prompt_provider", map[string]any{"value": "x"}).detail(404, "Setting is not household-controllable: llm.prompt_provider")
	put(member, "web_search.enabled", map[string]any{"value": true}).detail(403, "User has member role, requires admin or higher")
	put(admin, "web_search.enabled", map[string]any{}).want(400)

	cases := []struct {
		key, body, want string
		status          int
	}{
		{"web_search.enabled", `{"value": "YES"}`, `{"success":true,"key":"web_search.enabled","value":true}`, 200},
		{"web_search.enabled", `{"value": "nope"}`, `{"success":true,"key":"web_search.enabled","value":false}`, 200},
		{"memory.extraction_enabled", `{"value": 0}`, `{"success":true,"key":"memory.extraction_enabled","value":false}`, 200},
		{"phone_calls.calls_per_day", `{"value": " 7 "}`, `{"success":true,"key":"phone_calls.calls_per_day","value":7}`, 200},
		{"phone_calls.calls_per_day", `{"value": 5.0}`, `{"success":true,"key":"phone_calls.calls_per_day","value":5}`, 200},
		{"phone_calls.calls_per_day", `{"value": true}`, `{"detail":"Invalid value for phone_calls.calls_per_day: expected an integer, got boolean True"}`, 400},
		{"phone_calls.calls_per_day", `{"value": 2.5}`, `{"detail":"Invalid value for phone_calls.calls_per_day: expected an integer, got 2.5"}`, 400},
		{"phone_calls.calls_per_day", `{"value": "abc"}`, `{"detail":"Invalid value for phone_calls.calls_per_day: invalid literal for int() with base 10: 'abc'"}`, 400},
		{"phone_calls.calls_per_day", `{"value": [1]}`, `{"detail":"Invalid value for phone_calls.calls_per_day: expected an integer, got list"}`, 400},
		{"household.location", `{"value": "Springfield, IL"}`, `{"success":true,"key":"household.location","value":"Springfield, IL"}`, 200},
		{"persona.household_prompt", `{"value": "` + strings.Repeat("é", 2001) + `"}`, `{"detail":"Persona is too long (2001 chars); max is 2000."}`, 400},
	}
	for _, c := range cases {
		r := put(admin, c.key, c.body)
		if r.status != c.status || string(bytes.TrimSpace(r.body)) != c.want {
			t.Fatalf("%s %s: %d %s", c.key, c.body, r.status, r.body)
		}
	}
	ctx := context.Background()
	sc := settings.Scope{HouseholdID: "hh1"}
	if v := e.m.Settings().Int(ctx, "phone_calls.calls_per_day", sc); v != 5 {
		t.Fatal(v)
	}
	if v := e.m.Settings().String(ctx, settingHouseholdLocation, sc); v != "Springfield, IL" {
		t.Fatal(v)
	}
	if e.m.Settings().Bool(ctx, settingExtractionEnabled, sc) {
		t.Fatal("extraction should be off")
	}
	// Writes are household-scoped: another household and the system row are untouched.
	if e.m.Settings().Bool(ctx, settingExtractionEnabled, settings.Scope{HouseholdID: "hh2"}) != true {
		t.Fatal("leaked to hh2")
	}
	// The GET reflects the writes.
	s := e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	if s["phone_calls.calls_per_day"] != float64(5) || s["memory.extraction_enabled"] != false || s["web_search.enabled"] != false {
		t.Fatal(s)
	}
	// An owner of another household can't write here.
	other := e.auth.addUser(7, "hh2", authn.RoleOwner)
	put(other, "web_search.enabled", map[string]any{"value": true}).detail(403, "User is not a member of this household")
}

func TestPersonaPresets(t *testing.T) {
	e := newEnv(t)
	member := e.auth.addUser(5, "hh1", authn.RoleMember)
	r := e.do("GET", "/api/v0/mobile/household/hh1/persona/presets", nil, bearer(member)).want(200).json()
	ps := r["presets"].([]any)
	first := ps[0].(map[string]any)
	if len(ps) != len(prompts.PersonaPresets) || first["id"] != "warm_folksy" || first["label"] != "Warm & folksy" ||
		first["text"] != prompts.DefaultPersona || r["default_preset_id"] != "warm_folksy" ||
		r["default_text"] != prompts.DefaultPersona || r["max_chars"] != float64(2000) {
		t.Fatal(r)
	}
	e.do("GET", "/api/v0/mobile/household/hh1/persona/presets", nil, bearer(e.auth.addUser(6, "hh2", authn.RoleMember))).want(403)
}

func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
