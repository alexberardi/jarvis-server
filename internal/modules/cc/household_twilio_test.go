package cc

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// AD6: a household admin sets the household's own Twilio account from the app; the SID and
// auth token are write-only, nothing reads back the system default, and the phone service
// resolves the household's account.
func TestHouseholdTwilioCredentialsWriteOnly(t *testing.T) {
	e := newEnv(t)
	admin := e.auth.addUser(5, "hh1", authn.RoleAdmin)
	member := e.auth.addUser(6, "hh1", authn.RoleMember)
	ctx := context.Background()
	// A system default exists; members of hh1 must not see it as theirs.
	for k, v := range map[string]string{phone.SettingTwilioAccountSID: "AC-sys", phone.SettingTwilioAuthToken: "token-sys",
		phone.SettingTwilioFromNumber: "+15550000009"} {
		if err := e.m.Settings().Set(ctx, k, v, settings.Scope{}); err != nil {
			t.Fatal(err)
		}
	}
	get := func() map[string]any {
		return e.do("GET", "/api/v0/mobile/household/hh1/settings", nil, bearer(member)).want(200).json()["settings"].(map[string]any)
	}
	if s := get(); s[phone.SettingTwilioAccountSID] != nil || s[phone.SettingTwilioAuthToken] != nil || s[phone.SettingTwilioFromNumber] != nil {
		t.Fatalf("system default shown as the household's: %v", s)
	}

	put := func(tok, key, body string) *resp {
		return e.do("PUT", "/api/v0/mobile/household/hh1/settings/"+key, body, bearer(tok))
	}
	put(member, phone.SettingTwilioAuthToken, `{"value": "x"}`).detail(403, "User has member role, requires admin or higher")
	r := put(admin, phone.SettingTwilioAuthToken, `{"value": " token-A "}`)
	if got := string(bytes.TrimSpace(r.body)); r.status != 200 || got != `{"success":true,"key":"phone.twilio_auth_token","value":"********"}` {
		t.Fatalf("put token: %d %s", r.status, got)
	}
	if r := put(admin, phone.SettingTwilioAccountSID, `{"value": "AC-A"}`); strings.Contains(string(r.body), "AC-A") || r.status != 200 {
		t.Fatalf("put sid echoed: %s", r.body)
	}
	r = put(admin, phone.SettingTwilioFromNumber, `{"value": "555-1234"}`)
	if r.status != 400 || strings.Contains(string(r.body), "555-1234") {
		t.Fatalf("bad from number: %d %s", r.status, r.body)
	}
	if r := put(admin, phone.SettingTwilioFromNumber, `{"value": "+15550000001"}`); r.status != 200 ||
		!strings.Contains(string(r.body), `"value":"+15550000001"`) {
		t.Fatalf("from number: %d %s", r.status, r.body)
	}
	s := get()
	if s[phone.SettingTwilioAccountSID] != "********" || s[phone.SettingTwilioAuthToken] != "********" ||
		s[phone.SettingTwilioFromNumber] != "+15550000001" {
		t.Fatalf("after writes: %v", s)
	}
	// Stored trimmed at household scope; the system default is untouched.
	if v, ok, _ := e.m.Settings().GetExact(ctx, phone.SettingTwilioAuthToken, settings.Scope{HouseholdID: "hh1"}); !ok || v != "token-A" {
		t.Fatalf("stored token: %v %v", v, ok)
	}
	if v, _, _ := e.m.Settings().GetExact(ctx, phone.SettingTwilioAuthToken, settings.Scope{}); v != "token-sys" {
		t.Fatalf("system token: %v", v)
	}
	// The phone service now resolves the household's own account.
	tel, err := e.m.PhoneService().Telephony(ctx, "hh1")
	if err != nil || tel.Source != phone.SourceHousehold || tel.SigningKey != "token-A" {
		t.Fatalf("telephony: %+v %v", tel, err)
	}
	// The module's /settings API masks them too, at household and system scope.
	for _, sc := range []settings.Scope{{HouseholdID: "hh1"}, {}} {
		list, err := e.m.Settings().List(ctx, sc, "phone_calls")
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range list {
			if (row.Key == phone.SettingTwilioAccountSID || row.Key == phone.SettingTwilioAuthToken) && row.Value != "********" {
				t.Fatalf("%s unmasked at %+v: %v", row.Key, sc, row.Value)
			}
		}
	}
	// "" clears the household's value (back to null; the system default applies again).
	if r := put(admin, phone.SettingTwilioAuthToken, `{"value": ""}`); r.status != 200 ||
		string(bytes.TrimSpace(r.body)) != `{"success":true,"key":"phone.twilio_auth_token","value":null}` {
		t.Fatalf("clear: %s", r.body)
	}
	if s := get(); s[phone.SettingTwilioAuthToken] != nil {
		t.Fatalf("cleared token still shown: %v", s)
	}
}
