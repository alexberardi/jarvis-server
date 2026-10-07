package phone

import (
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

const contactsPath = "/api/v0/mobile/household/" + hh + "/phone-contacts"

func TestContactsCRUD(t *testing.T) {
	e := newEnv(t)
	r := e.do("POST", contactsPath, 1, map[string]any{"name": " Tony's Pizzeria ", "number": "(732) 592-4183", "address": "1 Main St"})
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	c := r.json(t)
	if c["name"] != "Tony's Pizzeria" || c["number"] != "+17325924183" || c["source"] != "manual" ||
		c["do_not_call"] != false || c["address"] != "1 Main St" || c["line_type"] != nil || c["notes"] != nil {
		t.Fatalf("create body: %v", c)
	}
	for _, k := range []string{"id", "verified_at", "created_at"} {
		if _, ok := c[k]; !ok {
			t.Fatalf("missing %s", k)
		}
	}
	id := c["id"].(string)

	// Duplicate normalized name → 409; invalid number → 400 with the validator's message.
	if r := e.do("POST", contactsPath, 2, map[string]any{"name": "tonys pizzeria!!", "number": "732-592-4184"}); r.status != 409 ||
		r.json(t)["detail"] != "'tonys pizzeria!!' is already in this household's phonebook" {
		t.Fatalf("dup: %d %s", r.status, r.body)
	}
	if r := e.do("POST", contactsPath, 1, map[string]any{"name": "Psychic", "number": "900-555-1234"}); r.status != 400 ||
		r.json(t)["detail"] != "Premium-rate numbers can't be called." {
		t.Fatalf("premium: %d %s", r.status, r.body)
	}
	if r := e.do("POST", contactsPath, 1, map[string]any{"name": "!!!", "number": "732-592-4183"}); r.status != 400 ||
		r.json(t)["detail"] != "Name must contain letters or digits" {
		t.Fatalf("name: %d %s", r.status, r.body)
	}
	if r := e.do("POST", contactsPath, 1, map[string]any{"number": "732-592-4183"}); r.status != 400 ||
		r.json(t)["details"].([]any)[0] != "body -> name: Field required" {
		t.Fatalf("missing name: %d %s", r.status, r.body)
	}

	// Outsiders: a non-member gets 403; a member of another household probing an id gets 404.
	if r := e.do("GET", contactsPath, 9, nil); r.status != 403 {
		t.Fatalf("outsider list: %d", r.status)
	}
	if r := e.do("PATCH", "/api/v0/mobile/household/"+otherHH+"/phone-contacts/"+id, 9, map[string]any{"notes": "x"}); r.status != 404 {
		t.Fatalf("foreign id: %d %s", r.status, r.body)
	}

	r = e.do("PATCH", contactsPath+"/"+id, 2, map[string]any{"do_not_call": true, "number": "908 555 1234", "notes": "rude"})
	if r.status != 200 {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if p := r.json(t); p["do_not_call"] != true || p["number"] != "+19085551234" || p["notes"] != "rude" || p["source"] != "manual" {
		t.Fatalf("patch body: %v", p)
	}
	e.do("POST", contactsPath, 1, map[string]any{"name": "Apex Auto", "number": "732-555-0000"})
	list := e.do("GET", contactsPath, 1, nil).json(t)["contacts"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["name"] != "Apex Auto" {
		t.Fatalf("list not sorted by name: %v", list)
	}
	if r := e.do("DELETE", contactsPath+"/"+id, 1, nil); r.status != 204 {
		t.Fatalf("delete: %d", r.status)
	}
	if r := e.do("DELETE", contactsPath+"/"+id, 1, nil); r.status != 404 {
		t.Fatalf("delete again: %d", r.status)
	}
}

func TestCallContextPutCanonicalises(t *testing.T) {
	e := newEnv(t)
	r := e.do("PUT", "/api/v0/mobile/call-context", 1, map[string]any{"fields": []any{
		map[string]any{"key": "full_name", "value": "Alex Berardi"},
		map[string]any{"label": "Gate Code!", "value": "4321", "tier": "shout"},
		map[string]any{"label": "Blank", "value": "   "},
		map[string]any{"key": "full_name", "value": "Second wins? no"},
		map[string]any{"key": "insurance_member_id", "value": "XZ-9912345", "category": "bogus"},
	}})
	if r.status != 200 {
		t.Fatalf("put: %d %s", r.status, r.body)
	}
	fields := r.json(t)["fields"].([]any)
	if len(fields) != 3 {
		t.Fatalf("fields: %v", fields)
	}
	f0, f1, f2 := fields[0].(map[string]any), fields[1].(map[string]any), fields[2].(map[string]any)
	if f0["label"] != "Full name" || f0["tier"] != "state" || f0["value"] != "Alex Berardi" {
		t.Fatalf("well-known: %v", f0)
	}
	if f1["key"] != "gate_code" || f1["tier"] != "if_asked" || f1["category"] != "general" {
		t.Fatalf("custom: %v", f1)
	}
	if f2["category"] != "medical" || f2["tier"] != "if_asked" {
		t.Fatalf("well-known coercion: %v", f2)
	}
	// Stored as a JSON string in the user's scope, readable by the plan path; nobody else sees it.
	v, err := e.s.Settings.Get(context.Background(), SettingCallContext, settings.Scope{UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := v.Value.(string)
	if !ok || !strings.HasPrefix(raw, `{"fields": [{"key": "full_name", "label": "Full name"`) {
		t.Fatalf("stored: %#v", v.Value)
	}
	other := e.do("GET", "/api/v0/mobile/call-context", 2, nil).json(t)
	if len(other["fields"].([]any)) != 0 {
		t.Fatalf("user 2 sees user 1's context: %v", other)
	}
	got := e.do("GET", "/api/v0/mobile/call-context", 1, nil).json(t)
	if len(got["fields"].([]any)) != 3 {
		t.Fatalf("get: %v", got)
	}
	cat := got["catalog"].(map[string]any)
	if len(cat["well_known"].([]any)) != 9 || len(cat["categories"].([]any)) != 6 || len(cat["tiers"].([]any)) != 2 {
		t.Fatalf("catalog: %v", cat)
	}
}

func TestBuildContextBlock(t *testing.T) {
	block := BuildContextBlock(ParseCallContext(`{"fields":[{"key":"full_name","value":"Alex"},` +
		`{"key":"pharmacy","value":"CVS"},{"key":"callback_number","value":"908-555-1234"}]}`))
	want := "The details below belong to the person you are calling ON BEHALF OF. You are their assistant: " +
		"never claim to be them, never introduce yourself with their name, and never ask to speak to them.\n\n" +
		"You may state these if it helps:\n- Preferred pharmacy: CVS\n\n" +
		"If the business asks for one of these details, give it directly and accurately — refusing one listed " +
		"here fails the call. Never volunteer them otherwise:\n- Full name: Alex\n- Callback number: 908-555-1234"
	if block != want {
		t.Fatalf("block:\n%s", block)
	}
	if BuildContextBlock(nil) != "" {
		t.Fatal("empty context renders a block")
	}
}
