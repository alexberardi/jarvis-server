package cc

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The phone subsystem is wired into the CC listener: mobile routes behind user auth, the
// make_phone_call tool always registered, its settings declared, and its D20 purge.
func TestPhoneWiring(t *testing.T) {
	e := newEnv(t, envOpts{noMQTT: true})
	tok := e.auth.addUser(5, "hh-p", authn.RoleMember)
	e.auth.addUser(6, "hh-q", authn.RoleMember)

	e.do("GET", "/api/v0/mobile/household/hh-p/phone-contacts", nil, nil).want(401)
	r := e.do("POST", "/api/v0/mobile/household/hh-p/phone-contacts",
		map[string]any{"name": "Tony's", "number": "732-592-4183"}, bearer(tok)).want(201).json()
	if r["number"] != "+17325924183" {
		t.Fatalf("contact: %v", r)
	}
	e.do("GET", "/api/v0/mobile/household/hh-p/phone-contacts", nil, bearer("tok-6")).
		detail(403, "User is not a member of this household")
	e.do("PUT", "/api/v0/mobile/call-context", map[string]any{"fields": []any{
		map[string]any{"key": "full_name", "value": "Pat"}}}, bearer(tok)).want(200)

	if _, ok := e.m.ServerTools().Get(phone.ToolName); !ok {
		t.Fatal("make_phone_call not registered")
	}
	if def, ok := e.m.Settings().Definition(phone.SettingEnabled); !ok || def.Default != false {
		t.Fatal("phone_calls.enabled not declared off")
	}
	if e.m.PhoneService().Enabled(context.Background(), "hh-p") {
		t.Fatal("phone on by default")
	}

	ctx := context.Background()
	tx, err := e.d.Write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.PurgeUser(ctx, tx, 5); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	v, _ := e.m.Settings().Get(ctx, phone.SettingCallContext, settings.Scope{UserID: 5})
	if v.FromDB {
		t.Fatal("call context survived account deletion")
	}
}
