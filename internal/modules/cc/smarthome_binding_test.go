package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

func TestDeviceImportNodeBinding(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.createNode("scan", "hh1")
	e.createNode("primary", "hh1")
	e.createNode("foreign", "hh2")
	dev := map[string]any{"entity_id": "light.desk", "name": "Desk", "domain": "light", "source": "direct", "protocol": "demo"}
	body := func(node string) map[string]any { return map[string]any{"node_id": node, "devices": []any{dev}} }
	e.do("POST", hhPath+"/devices/import", body("foreign"), bearer(tok)).want(403)
	e.do("POST", hhPath+"/devices/import", body("missing"), bearer(tok)).want(404)
	e.do("POST", hhPath+"/devices/import", body("scan"), bearer(tok)).want(201)
	d := e.deviceByEntity(tok, "light.desk")
	if d["node_id"] != "scan" {
		t.Fatalf("binding = %v", d["node_id"])
	}
	// An ambiguous/repeated import cannot move credentials to a different node.
	e.do("POST", hhPath+"/devices/import", body("primary"), bearer(tok)).want(409)
	e.do("POST", hhPath+"/devices/import", body("scan"), bearer(tok)).want(201)
	e.importDevices(tok, dev) // older clients preserve the binding
	if e.deviceByEntity(tok, "light.desk")["node_id"] != "scan" {
		t.Fatal("old import cleared binding")
	}
	dev["local_ip"] = "192.0.2.200"
	e.do("POST", hhPath+"/devices/import", map[string]any{"devices": []any{dev}}, bearer(tok)).want(409)
	delete(dev, "local_ip")
	ctx := context.Background()
	e.m.settings.Set(ctx, settingPrimaryNode, "primary", scopeHN("hh1", ""))
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ?`, dbTime(e.now))
	row, err := e.m.device(ctx, "hh1", d["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	n, err := e.m.pickDeviceNode(ctx, "hh1", row)
	if err != nil || n.nodeID != "scan" {
		t.Fatalf("picked %v: %v", n, err)
	}
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ? WHERE node_id = 'scan'`, dbTime(e.now.Add(-time.Hour)))
	if _, err := e.m.pickDeviceNode(ctx, "hh1", row); err == nil {
		t.Fatal("offline binding fell back")
	}
	e.d.Write.Exec(`UPDATE cc_nodes SET household_id = 'hh2', last_seen = ? WHERE node_id = 'scan'`, dbTime(e.now))
	if _, err := e.m.pickDeviceNode(ctx, "hh1", row); err == nil {
		t.Fatal("foreign binding was accepted")
	}
}

func TestDevicePairingStaysOnItsNode(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	otherUser := e.auth.addUser(2, "hh1", authn.RoleMember)
	n := e.createNode("pairer", "hh1")
	e.createNode("other", "hh1")
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ?`, dbTime(e.now))
	e.m.settings.Set(context.Background(), settingPrimaryNode, "pairer", scopeHN("hh1", ""))
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/pairer/commands")
	e.importDevices(tok, map[string]any{"entity_id": "climate.hall", "name": "Hall", "domain": "climate", "source": "direct"})
	c.next() // invalidate
	d := e.deviceByEntity(tok, "climate.hall")
	path := hhPath + "/devices/" + d["id"].(string) + "/control"
	ch := make(chan *resp, 1)
	go func() { ch <- e.do("POST", path, map[string]any{"action": "pair_start"}, bearer(tok)) }()
	_, msg := command(t, c.next())
	e.do("POST", "/api/v0/device-control-results/"+msg["request_id"].(string), map[string]any{
		"success": false, "input_required": map[string]any{"type": "pin", "prompt": "Code", "session_id": "session-1", "follow_up_action": "pair_finish"},
	}, n.h()).want(200)
	(<-ch).want(200)
	if e.deviceByEntity(tok, "climate.hall")["node_id"] != "pairer" {
		t.Fatal("pairing did not bind node")
	}
	e.m.settings.Set(context.Background(), settingPrimaryNode, "other", scopeHN("hh1", ""))
	follow := map[string]any{"action": "pair_finish", "data": map[string]any{"session_id": "session-1", "pin": "012-345", "entity_id": "climate.foreign", "protocol": "other"}}
	e.do("POST", path, follow, bearer(otherUser)).want(409)
	go func() { ch <- e.do("POST", path, follow, bearer(tok)) }()
	_, msg = command(t, c.next())
	if msg["context"].(map[string]any)["pin"] != "012-345" {
		t.Fatal(msg)
	}
	if msg["context"].(map[string]any)["entity_id"] != "climate.hall" || msg["context"].(map[string]any)["protocol"] != nil {
		t.Fatal("pairing answer overrode the registered identity", msg)
	}
	e.do("POST", "/api/v0/device-control-results/"+msg["request_id"].(string), map[string]any{"success": true}, n.h()).want(200)
	if !(<-ch).want(200).json()["success"].(bool) {
		t.Fatal("pair failed")
	}
	e.do("POST", path, follow, bearer(tok)).want(409) // consumed, never replayed
}

func TestDeviceInputScopeAndExpiry(t *testing.T) {
	var s deviceInputStore
	now := time.Now()
	d := &deviceRow{id: "d1", householdID: "h1"}
	prompt := map[string]any{"session_id": "s1", "follow_up_action": "pair_finish"}
	s.remember(now, d, "n1", 1, prompt)
	for _, tc := range []struct {
		device          deviceRow
		user            int64
		action, session string
	}{
		{deviceRow{id: "d2", householdID: "h1"}, 1, "pair_finish", "s1"},
		{deviceRow{id: "d1", householdID: "h2"}, 1, "pair_finish", "s1"},
		{*d, 2, "pair_finish", "s1"}, {*d, 1, "other", "s1"}, {*d, 1, "pair_finish", "wrong"},
	} {
		if _, ok := s.take(now, &tc.device, tc.user, tc.action, tc.session); ok {
			t.Fatal("accepted a mismatched answer")
		}
	}
	if n, ok := s.take(now, d, 1, "pair_finish", "s1"); !ok || n != "n1" {
		t.Fatal("valid answer lost")
	}
	if _, ok := s.take(now, d, 1, "pair_finish", "s1"); ok {
		t.Fatal("accepted replay")
	}
	// A repeated prompt can reuse its opaque ID; expiry still starts anew.
	s.remember(now, d, "n1", 1, prompt)
	if _, ok := s.take(now.Add(5*time.Minute), d, 1, "pair_finish", "s1"); ok {
		t.Fatal("accepted expired session")
	}
}

func TestDeviceRemovalUsesBoundNode(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("owner", "hh1")
	other := e.createNode("primary", "hh1")
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ?`, dbTime(e.now))
	e.m.settings.Set(context.Background(), settingPrimaryNode, "primary", scopeHN("hh1", ""))
	c, c2 := e.dialNode(n), e.dialNode(other)
	c.subscribe("jarvis/nodes/owner/commands")
	c2.subscribe("jarvis/nodes/primary/commands")
	for _, offline := range []bool{false, true} {
		e.do("POST", hhPath+"/devices/import", map[string]any{"node_id": "owner", "devices": []any{map[string]any{
			"entity_id": "media.bound", "name": "Bound", "domain": "media_player", "source": "direct", "protocol": "apple",
		}}}, bearer(tok)).want(201)
		c.next()
		c2.next()
		d := e.deviceByEntity(tok, "media.bound")
		if offline {
			e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = NULL WHERE node_id = 'owner'`)
		}
		e.do("DELETE", hhPath+"/devices/"+d["id"].(string), nil, bearer(tok)).want(204)
		if !offline {
			if verb, _ := command(t, c.next()); verb != "device_removed" {
				t.Fatal("owner not notified", verb)
			}
		}
		if verb, _ := command(t, c.next()); verb != "invalidate_device_cache" {
			t.Fatal(verb)
		}
		if verb, _ := command(t, c2.next()); verb != "invalidate_device_cache" {
			t.Fatal("removal reached another node", verb)
		}
	}
}
