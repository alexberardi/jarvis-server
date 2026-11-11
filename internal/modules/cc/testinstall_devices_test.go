package cc

import (
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

// protocolTI inserts a finished test install of the zigbee2mqtt protocol draft on node.
func protocolTI(t *testing.T, e *env, id, node string) {
	t.Helper()
	at := e.clock()
	if _, err := e.d.Write.Exec(`INSERT INTO cc_test_install_requests (id, node_id, household_id, share_code,
		package_name, status, results_json, created_at, expires_at, completed_at) VALUES (?, ?, 'hh1', 'ABC123',
		'zigbee2mqtt', 'completed', '{"package_name":"zigbee2mqtt","commands":[],"protocols":["zigbee2mqtt"]}', ?, ?, ?)`,
		id, node, dbTime(at), dbTime(at), dbTime(at)); err != nil {
		t.Fatal(err)
	}
}

func (e *env) listedEntities(tok string) []string {
	e.t.Helper()
	var out []string
	for _, d := range e.do("GET", hhPath+"/devices", nil, bearer(tok)).want(200).list() {
		out = append(out, d.(map[string]any)["entity_id"].(string))
	}
	return out
}

// A device imported through a Forge test's device protocol goes when the test ends (user
// 2026-11-10): nothing could control it after. A Reinstall keeps it; another protocol's devices
// stay; re-imported through a protocol no test provides (the real install), it stays for good.
func TestTestInstallDevicesGoWithTheTest(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("n1", "hh1")
	protocolTI(t, e, "ti1", "n1")
	plug := map[string]any{"entity_id": "switch.plug", "name": "Plug", "domain": "switch", "source": "direct", "protocol": "zigbee2mqtt"}
	lamp := map[string]any{"entity_id": "light.lamp", "name": "Lamp", "domain": "light", "source": "direct", "protocol": "lifx"}
	e.importDevices(tok, plug, lamp)

	e.advance(15 * time.Minute)
	protocolTI(t, e, "ti2", "n1") // Reinstall after a Forge fix
	e.advance(10 * time.Minute)   // the first install's window is over, not the reinstall's
	if got := e.listedEntities(tok); len(got) != 2 {
		t.Fatalf("listed %v during the reinstalled test", got)
	}

	e.advance(15 * time.Minute) // the test is over
	if got := e.listedEntities(tok); len(got) != 1 || got[0] != "light.lamp" {
		t.Fatalf("listed %v after the test", got)
	}

	// The published package, installed for real, and the plug added again: it stays.
	e.importDevices(tok, plug)
	protocolTI(t, e, "ti3", "n1") // even with a later test of the same draft running
	e.advance(time.Hour)
	if err := e.m.cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.listedEntities(tok); len(got) != 2 {
		t.Fatalf("listed %v: a real install's device went", got)
	}
}
