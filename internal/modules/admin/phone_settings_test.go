package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func phoneSettingsAdmin(t *testing.T) (*http.ServeMux, *settings.Service) {
	t.Helper()
	d, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	cc := newSettings(t, d, "cc", ccmod.Definitions())
	m := &Module{
		UI: builtUI(), Verify: fakeVerify,
		SettingsSources: []SettingsSource{fakeSettings{"cc", cc}},
		Households:      fakeHouseholds{"hh-home": "Home", "hh-cabin": "Cabin"},
	}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	return mux, cc
}

func TestInboundHouseholdSettingsAdminRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		key     string
		value   any
		initial any
	}{
		{phone.SettingInboundEnabled, true, false},
		{phone.SettingInboundAnswerDelay, float64(10), float64(0)},
		{phone.SettingInboundMaxSeconds, float64(90), float64(180)},
		{phone.SettingInboundCallsPerDay, float64(5), float64(20)},
		{phone.SettingInboundMaxConcurrent, float64(2), float64(1)},
		{phone.SettingInboundTransfers, []any{map[string]any{"label": "Demo desk", "number": "101", "when": "the caller is reporting a delivery"}}, []any{}},
	} {
		t.Run(tc.key, func(t *testing.T) {
			mux, svc := phoneSettingsAdmin(t)
			ctx := context.Background()
			path := "/api/settings/cc/" + tc.key + "?household_id=hh-home"
			hdr := append([]string{"Content-Type", "application/json"}, root...)
			row := settingOf(t, decode(t, send(mux, "GET", "/api/settings/?service=cc", "", root...)), "cc", tc.key)
			if row["household_scoped"] != true {
				t.Fatalf("inbound setting cannot be configured per household: %v", row)
			}
			if !reflect.DeepEqual(row["value"], tc.initial) || len(row["household_values"].([]any)) != 0 || row["households_using_default"] != 2.0 {
				t.Fatalf("initial default: %v", row)
			}

			body, err := json.Marshal(map[string]any{"value": tc.value})
			if err != nil {
				t.Fatal(err)
			}
			if w := send(mux, "PUT", path, string(body), hdr...); w.Code != 200 {
				t.Fatalf("create first household override: %d %s", w.Code, w.Body.String())
			}
			row = settingOf(t, decode(t, send(mux, "GET", "/api/settings/?service=cc", "", root...)), "cc", tc.key)
			hv := row["household_values"].([]any)
			if !reflect.DeepEqual(row["value"], tc.initial) || row["from_db"] != false || len(hv) != 1 || row["households_using_default"] != 1.0 {
				t.Fatalf("override changed system default or missing from GET: %v", row)
			}
			value := hv[0].(map[string]any)
			if value["household_id"] != "hh-home" || value["household_name"] != "Home" || !reflect.DeepEqual(value["value"], tc.value) {
				t.Fatalf("household override: %v", value)
			}
			for _, sc := range []settings.Scope{{}, {HouseholdID: "hh-cabin"}} {
				if _, found, err := svc.GetExact(ctx, tc.key, sc); err != nil || found {
					t.Fatalf("write leaked into %+v: found=%v err=%v", sc, found, err)
				}
			}

			if w := send(mux, "DELETE", path, "", root...); w.Code != 200 {
				t.Fatalf("use default: %d %s", w.Code, w.Body.String())
			}
			if _, found, err := svc.GetExact(ctx, tc.key, settings.Scope{HouseholdID: "hh-home"}); err != nil || found {
				t.Fatalf("use default left a household override: found=%v err=%v", found, err)
			}
			row = settingOf(t, decode(t, send(mux, "GET", "/api/settings/?service=cc", "", root...)), "cc", tc.key)
			if !reflect.DeepEqual(row["value"], tc.initial) || len(row["household_values"].([]any)) != 0 || row["households_using_default"] != 2.0 {
				t.Fatalf("default not restored: %v", row)
			}
		})
	}
}

func TestInboundHouseholdSettingsAdminRejectsInvalidValues(t *testing.T) {
	mux, svc := phoneSettingsAdmin(t)
	hdr := append([]string{"Content-Type", "application/json"}, root...)
	for _, tc := range []struct{ key, value string }{
		{phone.SettingInboundEnabled, `"yes"`},
		{phone.SettingInboundAnswerDelay, `-1`},
		{phone.SettingInboundAnswerDelay, `61`},
		{phone.SettingInboundMaxSeconds, `29`},
		{phone.SettingInboundMaxSeconds, `601`},
		{phone.SettingInboundMaxSeconds, `90.5`},
		{phone.SettingInboundCallsPerDay, `0`},
		{phone.SettingInboundCallsPerDay, `501`},
		{phone.SettingInboundMaxConcurrent, `0`},
		{phone.SettingInboundMaxConcurrent, `6`},
		{phone.SettingInboundTransfers, `{}`},
		{phone.SettingInboundTransfers, `[{"label":"Demo desk","number":"sip:desk@example.test","when":"delivery"}]`},
		{phone.SettingInboundTransfers, `[{"label":"Demo desk","number":"101","when":"delivery","unexpected":true}]`},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			path := "/api/settings/cc/" + tc.key + "?household_id=hh-home"
			if w := send(mux, "PUT", path, `{"value":`+tc.value+`}`, hdr...); w.Code != 422 {
				t.Fatalf("invalid setting must return 422: %d %s", w.Code, w.Body.String())
			}
			if _, found, err := svc.GetExact(context.Background(), tc.key, settings.Scope{HouseholdID: "hh-home"}); err != nil || found {
				t.Fatalf("invalid value was stored: found=%v err=%v", found, err)
			}
		})
	}
}

func TestInboundHouseholdSettingsAdminUseDefault(t *testing.T) {
	mux, svc := phoneSettingsAdmin(t)
	ctx := context.Background()
	path := "/api/settings/cc/" + phone.SettingInboundEnabled
	hdr := append([]string{"Content-Type", "application/json"}, root...)
	for _, write := range []struct{ path, body string }{
		{path, `{"value":true}`},
		{path + "?household_id=hh-home", `{"value":false}`},
	} {
		if w := send(mux, "PUT", write.path, write.body, hdr...); w.Code != 200 {
			t.Fatalf("save setting: %d %s", w.Code, w.Body.String())
		}
	}
	if svc.Bool(ctx, phone.SettingInboundEnabled, settings.Scope{HouseholdID: "hh-home"}) ||
		!svc.Bool(ctx, phone.SettingInboundEnabled, settings.Scope{HouseholdID: "hh-cabin"}) {
		t.Fatal("explicit false must override the true system default only for its household")
	}
	if w := send(mux, "DELETE", path+"?household_id=hh-home", "", root...); w.Code != 200 {
		t.Fatalf("use default: %d %s", w.Code, w.Body.String())
	}
	if !svc.Bool(ctx, phone.SettingInboundEnabled, settings.Scope{HouseholdID: "hh-home"}) {
		t.Fatal("use default must inherit the current system value")
	}
	row := settingOf(t, decode(t, send(mux, "GET", "/api/settings/?service=cc", "", root...)), "cc", phone.SettingInboundEnabled)
	if row["value"] != true || row["from_db"] != true || len(row["household_values"].([]any)) != 0 || row["households_using_default"] != 2.0 {
		t.Fatalf("current system default not shown: %v", row)
	}
}

func TestInboundHouseholdSettingsAdminRequiresSuperuser(t *testing.T) {
	mux, svc := phoneSettingsAdmin(t)
	path := "/api/settings/cc/" + phone.SettingInboundEnabled + "?household_id=hh-home"
	for _, method := range []string{"PUT", "DELETE"} {
		if w := send(mux, method, path, `{"value":true}`, "Content-Type", "application/json", "Authorization", "Bearer member"); w.Code != 403 {
			t.Errorf("member %s: %d %s", method, w.Code, w.Body.String())
		}
		if w := send(mux, method, path, `{"value":true}`, "Content-Type", "application/json"); w.Code != 401 {
			t.Errorf("anonymous %s: %d %s", method, w.Code, w.Body.String())
		}
	}
	if _, found, err := svc.GetExact(context.Background(), phone.SettingInboundEnabled, settings.Scope{HouseholdID: "hh-home"}); err != nil || found {
		t.Fatalf("unauthorized write landed: found=%v err=%v", found, err)
	}
}
