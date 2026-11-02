package cc

import (
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
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

// A node's device control keeps the display's guard rails: only advertised actions, access
// devices only once confirmed, and only value fields (no identity or routing ones) in data.
// Any node of the household passes them (a Pi display too); another household's is 404.
func TestNodeDeviceControlGuard(t *testing.T) {
	controlWait = 50 * time.Millisecond
	defer func() { controlWait = 10 * time.Second }()
	e := newEnv(t)
	n := e.createNode("n1", "hh1")
	stranger := e.createNode("n2", "hh2")
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.importDevices(tok,
		map[string]any{"entity_id": "lock.front", "name": "Front", "domain": "lock"},
		map[string]any{"entity_id": "media_player.tv", "name": "TV", "domain": "media_player", "source": "home_assistant"},
		map[string]any{"entity_id": "sensor.temp", "name": "Temp", "domain": "sensor"},
	)
	path := func(entity string) string {
		return "/api/v0/node/devices/" + e.deviceByEntity(tok, entity)["id"].(string) + "/control"
	}
	lock, tv := path("lock.front"), path("media_player.tv")
	e.do("POST", lock, map[string]any{"action": "unlock"}, n.h()).detail(409, "Confirm opening this access device")
	e.do("POST", lock, map[string]any{"action": "run_script", "confirmed": true}, n.h()).detail(400, "Action is not advertised by this device")
	e.do("POST", lock, map[string]any{"action": "lock", "data": map[string]any{"entity_id": "lock.other"}}, n.h()).detail(400, `Unsupported action field "entity_id"`)
	e.do("POST", path("sensor.temp"), map[string]any{"action": "turn_on"}, n.h()).detail(400, "Action is not advertised by this device")
	e.do("POST", tv, map[string]any{"action": "play_media", "data": map[string]any{"url": "file:///etc/passwd"}}, n.h()).detail(400, "Use a direct HTTP(S) media link")
	e.do("POST", tv, map[string]any{"action": "seek", "data": map[string]any{"seconds": -1}}, n.h()).detail(400, "Invalid device action value")
	e.do("POST", tv, map[string]any{"action": "set_mode", "data": map[string]any{"mode": "a\nb"}}, n.h()).detail(400, "Invalid device mode")
	// Past the guard: the request goes to a node (none answers here, so it times out).
	for _, ok := range []struct {
		path   string
		action string
		data   map[string]any
	}{
		{lock, "unlock", nil}, {tv, "play_media", map[string]any{"url": "https://example.org/v.mp4"}}, {tv, "seek", map[string]any{"seconds": 30}},
	} {
		body := map[string]any{"action": ok.action, "data": ok.data, "confirmed": true}
		if r := e.do("POST", ok.path, body, n.h()).want(200).json(); r["error"] != errNodeTimeout {
			t.Fatalf("%s: %v", ok.action, r)
		}
	}
	e.do("POST", tv, map[string]any{"action": "pause"}, stranger.h()).detail(404, "Device not found")
	e.do("POST", "/api/v0/node/devices/nope/control", map[string]any{"action": "lock"}, n.h()).detail(404, "Device not found")
}
