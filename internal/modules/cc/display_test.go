package cc

import (
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"strings"
	"testing"
)

func TestDisplaySettingsDeliveryAndIsolation(t *testing.T) {
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh2")
	admin := e.auth.addUser(1, "hh1", authn.RoleOwner)
	u := e.auth.users[admin]
	u.IsSuperuser = true
	e.auth.users[admin] = u
	member := e.auth.addUser(2, "hh1", authn.RoleMember)
	p := "/api/v0/display/config/"
	e.do("PUT", p+"defaults", map[string]any{"overrides": map[string]any{"enabled": true}}, bearer(admin)).want(400)
	e.do("GET", p+"defaults", nil, bearer(member)).want(403)
	saved := e.do("PUT", p+"defaults", map[string]any{"overrides": map[string]any{"idle_seconds": 90, "waveform_colour": "#11aaff", "waveform_emphasis": 175}, "pin": "1234"}, bearer(admin)).want(200).json()
	if _, ok := saved["verifier"]; ok {
		t.Fatal("verifier exposed")
	}
	if saved["pin_configured"] != true {
		t.Fatal(saved)
	}
	e.do("PUT", p+"n1", map[string]any{"overrides": map[string]any{"enabled": true, "clock": "combined", "waveform_colour": "#ffaa11"}}, bearer(admin)).want(200)
	d := e.do("GET", "/api/v0/node/display", nil, n.h()).want(200).json()
	c := d["configuration"].(map[string]any)
	if c["waveform_colour"] != "#ffaa11" || c["waveform_emphasis"] != 175.0 || c["idle_seconds"] != 90.0 || c["clock"] != "combined" || !strings.HasPrefix(d["verifier"].(string), "$2") {
		t.Fatal(d)
	}
	e.do("POST", "/api/v0/node/display/ack", map[string]any{"revision": d["revision"]}, n.h()).want(200)
	s := e.do("GET", p+"n1", nil, bearer(admin)).want(200).json()
	if s["revision"] != s["applied_revision"] {
		t.Fatal(s)
	}
	e.do("POST", "/api/v0/node/display/ack", map[string]any{"revision": d["revision"]}, other.h()).want(409)
	e.do("PUT", p+"defaults", map[string]any{"overrides": map[string]any{}, "pin": ""}, bearer(admin)).want(400)
	e.do("PUT", p+"n1", map[string]any{"overrides": map[string]any{"clock": nil}}, bearer(admin)).want(200)
	s = e.do("GET", p+"n1", nil, bearer(admin)).want(200).json()
	if s["configuration"].(map[string]any)["clock"] != "digital" {
		t.Fatal(s)
	}
	e.do("GET", "/api/v0/node/display/assets/"+strings.Repeat("a", 64), nil, other.h()).want(404)
}
func TestDisplayDeviceBoundaries(t *testing.T) {
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.do("GET", "/api/v0/node/display/devices", nil, n.h()).want(403)
	_, err := e.m.deps.DB.Write.Exec(`UPDATE cc_nodes SET node_kind='desktop',owner_user_id=1 WHERE node_id='n1'`)
	if err != nil {
		t.Fatal(err)
	}
	e.importDevices(tok, map[string]any{"entity_id": "lock.front", "name": "Front", "domain": "lock"})
	dev := e.deviceByEntity(tok, "lock.front")
	id := dev["id"].(string)
	p := "/api/v0/node/display/devices/" + id + "/control"
	e.do("POST", p, map[string]any{"action": "unlock"}, n.h()).want(409)
	e.do("POST", p, map[string]any{"action": "run_script", "confirmed": true}, n.h()).want(400)
	e.do("POST", p, map[string]any{"action": "lock", "data": map[string]any{"entity_id": "lock.other"}}, n.h()).want(400)
	e.do("POST", "/api/v0/node/display/devices/nope/control", map[string]any{"action": "lock"}, n.h()).want(404)
}
