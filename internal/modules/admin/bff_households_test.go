package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// settingOf finds one setting in a GET /api/settings reply.
func settingOf(t *testing.T, out map[string]any, service, key string) map[string]any {
	t.Helper()
	for _, svc := range out["services"].([]any) {
		svc := svc.(map[string]any)
		if svc["service_name"] != service {
			continue
		}
		for _, s := range svc["settings"].([]any) {
			if s := s.(map[string]any); s["key"] == key {
				return s
			}
		}
	}
	t.Fatalf("%s/%s not in %v", service, key, out)
	return nil
}

func TestSettingsHouseholdValues(t *testing.T) {
	e := newBFF(t)
	ctx := context.Background()
	e.cc.Set(ctx, "pantry.enabled", true, settings.Scope{HouseholdID: "hh-home"})
	e.cc.Set(ctx, "pantry.enabled", false, settings.Scope{HouseholdID: "hh-cabin"})
	e.cc.Set(ctx, "pantry.enabled", true, settings.Scope{HouseholdID: "hh-gone"}) // no such household
	e.cc.Set(ctx, "pantry.enabled", true, settings.Scope{HouseholdID: "hh-flat", NodeID: "n1"})
	e.cc.Set(ctx, "phone.twilio_auth_token", "tok", settings.Scope{HouseholdID: "hh-flat"})

	out := decode(t, send(e.mux, "GET", "/api/settings/", "", root...))
	pantry := settingOf(t, out, "cc", "pantry.enabled")
	// The existing fields are unchanged: value is the system default.
	if pantry["value"] != false || pantry["from_db"] != false || pantry["household_scoped"] != true {
		t.Fatalf("pantry: %v", pantry)
	}
	hv := pantry["household_values"].([]any)
	// Sorted by name (case-insensitive); the deleted household and the node row are left out.
	if len(hv) != 2 || pantry["households_using_default"] != 1.0 {
		t.Fatalf("household values: %v (using default %v)", hv, pantry["households_using_default"])
	}
	cabin, home := hv[0].(map[string]any), hv[1].(map[string]any)
	if cabin["household_id"] != "hh-cabin" || cabin["household_name"] != "cabin" || cabin["value"] != false ||
		home["household_id"] != "hh-home" || home["household_name"] != "Home" || home["value"] != true ||
		home["updated_at"] == nil {
		t.Fatalf("household values: %v", hv)
	}
	// A household key nobody has set: an empty list, every household on the default.
	tz := settingOf(t, out, "cc", "household.timezone")
	if tz["household_scoped"] != true || len(tz["household_values"].([]any)) != 0 || tz["households_using_default"] != 3.0 {
		t.Fatalf("timezone: %v", tz)
	}
	// Secrets stay masked at household scope.
	tok := settingOf(t, out, "cc", "phone.twilio_auth_token")["household_values"].([]any)
	if len(tok) != 1 || tok[0].(map[string]any)["value"] != "********" {
		t.Fatalf("secret household value: %v", tok)
	}
	// A system-only key carries no household fields.
	mgr := settingOf(t, out, "cc", "smarthome.manager")
	if _, has := mgr["household_values"]; has || mgr["household_scoped"] != false {
		t.Fatalf("system-only key: %v", mgr)
	}
}

func TestSettingsHouseholdPutDelete(t *testing.T) {
	e := newBFF(t)
	ctx := context.Background()
	hdr := append([]string{"Content-Type", "application/json"}, root...)
	put := func(path, body string, h ...string) *httptest.ResponseRecorder {
		return send(e.mux, "PUT", path, body, h...)
	}
	household := func(hh string) any {
		v, _ := e.cc.Get(ctx, "pantry.enabled", settings.Scope{HouseholdID: hh})
		return v.Value
	}

	w := put("/api/settings/cc/pantry.enabled?household_id=hh-home", `{"value":true}`, hdr...)
	out := decode(t, w)
	if w.Code != 200 || out["success"] != true || out["household_id"] != "hh-home" || out["key"] != "pantry.enabled" {
		t.Fatalf("put: %d %v", w.Code, out)
	}
	if household("hh-home") != true || household("hh-cabin") != false {
		t.Fatalf("household write leaked: home %v cabin %v", household("hh-home"), household("hh-cabin"))
	}
	if v, _ := e.cc.Get(ctx, "pantry.enabled", settings.Scope{}); v.Value != false || v.FromDB {
		t.Fatalf("the default changed: %+v", v)
	}

	for _, c := range []struct {
		method, path, body string
		code               int
		detail             string
	}{
		{"PUT", "/api/settings/cc/pantry.enabled?household_id=hh-nope", `{"value":true}`, 404, "Household not found"},
		{"DELETE", "/api/settings/cc/pantry.enabled?household_id=hh-nope", "", 404, "Household not found"},
		{"PUT", "/api/settings/cc/smarthome.manager?household_id=hh-home", `{"value":"home_assistant"}`, 404,
			"Setting is not household-controllable: smarthome.manager"},
		{"DELETE", "/api/settings/cc/smarthome.manager?household_id=hh-home", "", 404,
			"Setting is not household-controllable: smarthome.manager"},
		{"PUT", "/api/settings/cc/nope?household_id=hh-home", `{"value":1}`, 404, "Setting not found: nope"},
		{"PUT", "/api/settings/cc/pantry.enabled?household_id=", `{"value":true}`, 422, ""},
		{"DELETE", "/api/settings/cc/pantry.enabled", "", 422, ""},
		// Type and the setting's own validator, as for the default.
		{"PUT", "/api/settings/cc/pantry.enabled?household_id=hh-home", `{"value":"yes"}`, 422, ""},
		{"PUT", "/api/settings/cc/household.timezone?household_id=hh-home", `{"value":"Mars/Olympus"}`, 422, ""},
		{"PUT", "/api/settings/cc/pantry.enabled?household_id=hh-home", `{}`, 422, ""},
	} {
		w := send(e.mux, c.method, c.path, c.body, hdr...)
		if w.Code != c.code || (c.detail != "" && detailOf(w) != c.detail) {
			t.Errorf("%s %s %s: %d %s", c.method, c.path, c.body, w.Code, w.Body.String())
		}
	}
	// The validator message reaches the client, and the default rejects it too.
	if w := put("/api/settings/cc/household.timezone", `{"value":"Mars/Olympus"}`, hdr...); w.Code != 422 {
		t.Fatalf("system-scope validator: %d %s", w.Code, w.Body.String())
	}
	if household("hh-home") != true {
		t.Fatal("a rejected write changed the value")
	}

	// Superuser only.
	for _, method := range []string{"PUT", "DELETE"} {
		path := "/api/settings/cc/pantry.enabled?household_id=hh-home"
		if w := send(e.mux, method, path, `{"value":false}`, "Content-Type", "application/json", "Authorization", "Bearer member"); w.Code != 403 {
			t.Errorf("member %s: %d", method, w.Code)
		}
		if w := send(e.mux, method, path, `{"value":false}`, "Content-Type", "application/json"); w.Code != 401 {
			t.Errorf("anonymous %s: %d", method, w.Code)
		}
	}
	if household("hh-home") != true {
		t.Fatal("a non-superuser write landed")
	}

	// "Use default": DELETE removes the household's row; the household reads the default again.
	e.cc.Set(ctx, "pantry.enabled", true, settings.Scope{})
	w = send(e.mux, "DELETE", "/api/settings/cc/pantry.enabled?household_id=hh-home", "", root...)
	if out := decode(t, w); w.Code != 200 || out["success"] != true || out["household_id"] != "hh-home" {
		t.Fatalf("delete: %d %v", w.Code, out)
	}
	if vals, _ := e.cc.HouseholdValues(ctx, "pantry.enabled"); len(vals) != 0 {
		t.Fatalf("still has household values: %+v", vals)
	}
	if household("hh-home") != true {
		t.Fatal("household doesn't follow the default after DELETE")
	}
	// Deleting again succeeds (idempotent); PUT null does the same as DELETE.
	if w := send(e.mux, "DELETE", "/api/settings/cc/pantry.enabled?household_id=hh-home", "", root...); w.Code != 200 {
		t.Fatalf("second delete: %d", w.Code)
	}
	put("/api/settings/cc/pantry.enabled?household_id=hh-cabin", `{"value":false}`, hdr...)
	if w := put("/api/settings/cc/pantry.enabled?household_id=hh-cabin", `{"value":null}`, hdr...); w.Code != 200 {
		t.Fatalf("put null: %d %s", w.Code, w.Body.String())
	}
	if household("hh-cabin") != true {
		t.Fatal("put null left a row hiding the default")
	}
}

// The user's report (2026-10-09): pantry.enabled turned on for a household in the mobile app
// (cc's PUT /api/v0/mobile/household/{hh}/settings/pantry.enabled writes it at household
// scope) showed as false on the admin settings page. With cc's real definitions, that
// household's value now appears beside the default.
func TestMobileHouseholdValueShowsInAdmin(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cc := newSettings(t, d, "cc", ccmod.Definitions())
	// What the mobile route stores (household_settings.go: Set at Scope{HouseholdID}).
	if err := cc.Set(ctx, "pantry.enabled", true, settings.Scope{HouseholdID: "hh1"}); err != nil {
		t.Fatal(err)
	}
	m := &Module{UI: builtUI(), Verify: fakeVerify, SettingsSources: []SettingsSource{fakeSettings{"cc", cc}},
		Households: fakeHouseholds{"hh1": "Berardi", "hh2": "Guests"}}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})

	out := decode(t, send(mux, "GET", "/api/settings/?service=cc", "", root...))
	pantry := settingOf(t, out, "cc", "pantry.enabled")
	hv := pantry["household_values"].([]any)
	if pantry["value"] != false || len(hv) != 1 || hv[0].(map[string]any)["household_name"] != "Berardi" ||
		hv[0].(map[string]any)["value"] != true || pantry["households_using_default"] != 1.0 {
		t.Fatalf("pantry.enabled: %v", pantry)
	}
	// Every allowlisted key is household-scoped in the admin; a system-only key is not.
	for _, k := range []string{"web_search.enabled", "memory.enabled", "household.timezone", "persona.household_prompt"} {
		if settingOf(t, out, "cc", k)["household_scoped"] != true {
			t.Errorf("%s not household-scoped", k)
		}
	}
	if settingOf(t, out, "cc", "llm.prompt_provider")["household_scoped"] != false {
		t.Error("llm.prompt_provider is household-scoped")
	}
}
