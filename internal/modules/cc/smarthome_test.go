package cc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

const hhPath = "/api/v0/households/hh1"

func (e *env) createRoom(tok, name string, parent any) map[string]any {
	e.t.Helper()
	return e.do("POST", hhPath+"/rooms", map[string]any{"name": name, "parent_room_id": parent}, bearer(tok)).want(201).json()
}

func (e *env) importDevices(tok string, devs ...map[string]any) map[string]any {
	e.t.Helper()
	return e.do("POST", hhPath+"/devices/import", map[string]any{"devices": devs}, bearer(tok)).want(201).json()
}

func (e *env) deviceByEntity(tok, entity string) map[string]any {
	e.t.Helper()
	for _, d := range e.do("GET", hhPath+"/devices", nil, bearer(tok)).want(200).list() {
		if dm := d.(map[string]any); dm["entity_id"] == entity {
			return dm
		}
	}
	e.t.Fatalf("device %s not listed", entity)
	return nil
}

func TestRooms(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(2, "hh2", authn.RoleOwner)

	up := e.createRoom(tok, " Upstairs ", nil)
	if up["name"] != "Upstairs" || up["normalized_name"] != "upstairs" || up["parent_room_id"] != nil ||
		up["device_count"] != 0.0 || up["node_count"] != 0.0 || up["created_at"] != "2026-10-06T12:00:00" {
		t.Fatal(up)
	}
	e.do("POST", hhPath+"/rooms", map[string]any{"name": "UPSTAIRS"}, bearer(tok)).detail(409, "Room 'UPSTAIRS' already exists")
	e.do("POST", hhPath+"/rooms", map[string]any{"name": "x", "parent_room_id": "nope"}, bearer(tok)).
		detail(400, "Parent room not found in this household")
	e.do("POST", hhPath+"/rooms", map[string]any{}, bearer(tok)).want(400)
	bed := e.createRoom(tok, "Bedroom", up["id"])
	closet := e.createRoom(tok, "Closet", bed["id"])

	// Cycles are refused; null clears the parent; absent leaves it.
	e.do("PATCH", hhPath+"/rooms/"+up["id"].(string), map[string]any{"parent_room_id": closet["id"]}, bearer(tok)).
		detail(400, "Cannot set parent: would create a cycle")
	e.do("PATCH", hhPath+"/rooms/"+up["id"].(string), map[string]any{"parent_room_id": up["id"]}, bearer(tok)).
		detail(400, "Cannot set parent: would create a cycle")
	r := e.do("PATCH", hhPath+"/rooms/"+closet["id"].(string), map[string]any{"icon": "door"}, bearer(tok)).want(200).json()
	if r["parent_room_id"] != bed["id"] || r["icon"] != "door" {
		t.Fatal(r)
	}
	e.do("PATCH", hhPath+"/rooms/"+closet["id"].(string), map[string]any{"name": "bedroom"}, bearer(tok)).
		detail(409, "Room 'bedroom' already exists")
	r = e.do("PATCH", hhPath+"/rooms/"+closet["id"].(string), map[string]any{"parent_room_id": nil}, bearer(tok)).want(200).json()
	if r["parent_room_id"] != nil {
		t.Fatal(r)
	}
	e.do("PATCH", hhPath+"/rooms/nope", map[string]any{}, bearer(tok)).detail(404, "Room not found")

	// Counts: active devices and nodes.
	e.importDevices(tok, map[string]any{"entity_id": "light.a", "name": "A", "domain": "light", "room_id": bed["id"]})
	e.createNode("n1", "hh1")
	e.d.Write.Exec(`UPDATE cc_nodes SET room_id = ? WHERE node_id = 'n1'`, bed["id"])
	rooms := e.do("GET", hhPath+"/rooms", nil, bearer(tok)).want(200).list()
	if len(rooms) != 3 || rooms[1].(map[string]any)["device_count"] != 1.0 || rooms[1].(map[string]any)["node_count"] != 1.0 {
		t.Fatal(rooms)
	}

	// Outsiders are blocked; the admin key bypasses.
	e.do("GET", hhPath+"/rooms", nil, bearer(stranger)).detail(403, "User is not a member of this household")
	e.do("GET", hhPath+"/rooms", nil, adminH()).want(200)
	e.do("GET", hhPath+"/rooms", nil, hdr{"X-API-Key": "wrong"}).detail(401, "Invalid API key")
	e.do("GET", hhPath+"/rooms", nil, nil).detail(401, "Authentication required")

	// Q7: node auth on GET rooms, scoped to the node's own household.
	n1 := testNode{id: "n1", key: e.auth.nodes["n1"].key}
	if got := e.do("GET", hhPath+"/rooms", nil, n1.h()).want(200).list(); len(got) != 3 {
		t.Fatal(got)
	}
	e.do("GET", "/api/v0/households/hh2/rooms", nil, n1.h()).detail(403, "Not authorized")
	e.do("POST", hhPath+"/rooms", map[string]any{"name": "z"}, n1.h()).detail(401, "Invalid API key")

	// Delete takes the subtree (legacy cascade="all"); devices and nodes keep existing, unassigned.
	e.do("PATCH", hhPath+"/rooms/"+closet["id"].(string), map[string]any{"parent_room_id": bed["id"]}, bearer(tok)).want(200)
	e.do("DELETE", hhPath+"/rooms/"+up["id"].(string), nil, bearer(tok)).want(204)
	if got := e.do("GET", hhPath+"/rooms", nil, bearer(tok)).want(200).list(); len(got) != 0 {
		t.Fatal(got)
	}
	if d := e.deviceByEntity(tok, "light.a"); d["room_id"] != nil || d["room_name"] != nil {
		t.Fatal(d)
	}
	e.do("DELETE", hhPath+"/rooms/"+up["id"].(string), nil, bearer(tok)).detail(404, "Room not found")
}

func TestDeviceRegistry(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	room := e.createRoom(tok, "Kitchen", nil)
	other := e.do("POST", "/api/v0/households/hh2/rooms", map[string]any{"name": "Theirs"}, adminH()).want(201).json()

	r := e.importDevices(tok,
		map[string]any{"entity_id": "light.k", "name": "Lamp", "domain": "light", "room_id": room["id"], "source": "direct", "protocol": "lifx"},
		map[string]any{"entity_id": "light.k2", "name": "lamp", "domain": "light"},
		map[string]any{"entity_id": "sensor.t", "name": "Lamp", "domain": "sensor", "mac_address": "AA:BB"},
	)
	if r["created"] != 3.0 || r["updated"] != 0.0 {
		t.Fatal(r)
	}
	if verb, _ := command(t, c.next()); verb != "invalidate_device_cache" {
		t.Fatal(verb)
	}
	lamp := e.deviceByEntity(tok, "light.k")
	acts := lamp["supported_actions"].([]any)
	if lamp["name"] != "Lamp" || lamp["room_name"] != "Kitchen" || lamp["source"] != "direct" || len(acts) != 3 ||
		acts[2].(map[string]any)["button_action"] != "toggle" {
		t.Fatal(lamp)
	}
	if d := e.deviceByEntity(tok, "light.k2"); d["name"] != "lamp (2)" || d["source"] != "home_assistant" {
		t.Fatal(d)
	}
	if d := e.deviceByEntity(tok, "sensor.t"); d["name"] != "Lamp (3)" || d["is_controllable"] != false || d["supported_actions"] != nil {
		t.Fatal(d)
	}
	// A re-import renames within the batch only; the room stays when none is given.
	r = e.importDevices(tok, map[string]any{"entity_id": "light.k", "name": "Lamp", "domain": "light"})
	if r["created"] != 0.0 || r["updated"] != 1.0 {
		t.Fatal(r)
	}
	c.next()
	if d := e.deviceByEntity(tok, "light.k"); d["name"] != "Lamp" || d["room_id"] != room["id"] || d["protocol"] != nil {
		t.Fatal(d)
	}
	e.do("POST", hhPath+"/devices/import", map[string]any{"devices": []any{map[string]any{"entity_id": "x", "name": "x", "domain": "light", "room_id": other["id"]}}}, bearer(tok)).
		detail(400, "Room not found in this household")
	e.do("POST", hhPath+"/devices/import", map[string]any{"devices": []any{map[string]any{"name": "x"}}}, bearer(tok)).want(400)

	// Filters, recursive rooms.
	sub := e.createRoom(tok, "Pantry", room["id"])
	e.importDevices(tok, map[string]any{"entity_id": "switch.p", "name": "Fan", "domain": "switch", "room_id": sub["id"]})
	c.next()
	if got := e.do("GET", hhPath+"/devices?room_id="+room["id"].(string), nil, bearer(tok)).want(200).list(); len(got) != 1 {
		t.Fatal(got)
	}
	if got := e.do("GET", hhPath+"/devices?recursive=true&room_id="+room["id"].(string), nil, bearer(tok)).want(200).list(); len(got) != 2 {
		t.Fatal(got)
	}
	if got := e.do("GET", hhPath+"/devices?domain=sensor", nil, bearer(tok)).want(200).list(); len(got) != 1 {
		t.Fatal(got)
	}
	if got := e.do("GET", hhPath+"/devices?source=direct", nil, bearer(tok)).want(200).list(); len(got) != 0 {
		t.Fatal(got) // the re-import reset source to the default
	}

	// PATCH: 422 empty, 409 case-insensitive clash, null room clears, foreign room 400.
	id := lamp["id"].(string)
	e.do("PATCH", hhPath+"/devices/"+id, map[string]any{"name": "  "}, bearer(tok)).detail(422, "Device name cannot be empty")
	e.do("PATCH", hhPath+"/devices/"+id, map[string]any{"name": "FAN"}, bearer(tok)).detail(409, "A device named 'FAN' already exists in this household")
	e.do("PATCH", hhPath+"/devices/"+id, map[string]any{"room_id": other["id"]}, bearer(tok)).detail(400, "Room not found in this household")
	d := e.do("PATCH", hhPath+"/devices/"+id, map[string]any{"name": " Big Lamp ", "room_id": nil, "is_active": false}, bearer(tok)).want(200).json()
	if d["name"] != "Big Lamp" || d["room_id"] != nil || d["is_active"] != false {
		t.Fatal(d)
	}
	c.next()
	e.do("PATCH", hhPath+"/devices/nope", map[string]any{}, bearer(tok)).detail(404, "Device not found")

	// /node/devices: the node's household, active only, legacy's empty-string fields.
	nd := e.do("GET", "/api/v0/node/devices", nil, n.h()).want(200).list()
	if len(nd) != 3 {
		t.Fatal(nd)
	}
	first := nd[0].(map[string]any)
	if first["entity_id"] != "light.k2" || first["local_ip"] != "" || first["room_name"] != "" || first["source"] != "home_assistant" {
		t.Fatal(first)
	}
	e.do("GET", "/api/v0/node/devices", nil, nil).want(400)

	// DELETE: device_removed to the protocol node for direct devices, then invalidate.
	e.importDevices(tok, map[string]any{"entity_id": "media.atv", "name": "ATV", "domain": "media_player", "source": "direct", "protocol": "apple"})
	c.next()
	atv := e.deviceByEntity(tok, "media.atv")
	e.do("DELETE", hhPath+"/devices/"+atv["id"].(string), nil, bearer(tok)).want(204)
	verb, det := command(t, c.next())
	if verb != "device_removed" || det["entity_id"] != "media.atv" || det["protocol"] != "apple" || det["name"] != "ATV" {
		t.Fatal(verb, det)
	}
	if verb, _ := command(t, c.next()); verb != "invalidate_device_cache" {
		t.Fatal(verb)
	}
	e.do("DELETE", hhPath+"/devices/"+atv["id"].(string), nil, bearer(tok)).detail(404, "Device not found")

	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	e.do("GET", hhPath+"/devices", nil, bearer(stranger)).want(403)
	e.do("DELETE", hhPath+"/devices/"+id, nil, bearer(stranger)).want(403)
}

func TestSupportedActions(t *testing.T) {
	cases := []struct {
		domain, protocol string
		ctl              bool
		want             []string
	}{
		{"light", "lifx", true, []string{"turn_on", "turn_off", "toggle"}},
		{"light", "", true, []string{"turn_on", "turn_off"}},
		{"lock", "unknownproto", true, []string{"lock", "unlock"}},
		{"thing", "", true, []string{"turn_on", "turn_off"}},
		{"climate", "nest", true, []string{"set_temperature", "set_mode", "turn_off"}},
		{"dishwasher", "homeconnect", true, []string{"start_auto", "start_eco", "start_quick", "start_glass", "start_prerinse", "stop"}},
		{"light", "lifx", false, nil},
	}
	for _, c := range cases {
		got := supportedActions(c.domain, c.protocol, c.ctl)
		var acts []string
		for _, a := range got {
			acts = append(acts, a["button_action"].(string))
		}
		if fmt.Sprint(acts) != fmt.Sprint(c.want) || (c.want == nil) != (got == nil) {
			t.Errorf("%s/%s: %v, want %v", c.domain, c.protocol, acts, c.want)
		}
	}
	if b := supportedActions("x", "homeconnect", true)[1]; b["button_text"] != "Eco 50°" || b["button_icon"] != "leaf" {
		t.Fatal(b)
	}
}

func TestPickNode(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.createNode("a", "hh1")
	e.createNode("b", "hh1")
	e.createNode("c", "hh1")
	set := func(id, protocols string, ago time.Duration) {
		e.d.Write.Exec(`UPDATE cc_nodes SET protocols = ?, last_seen = ? WHERE node_id = ?`, protocols, dbTime(e.now.Add(-ago)), id)
	}
	set("a", `["kasa"]`, time.Minute)
	set("b", `["kasa","lifx"]`, 2*time.Minute)
	set("c", ``, 0)
	pick := func(p string) string {
		n, err := e.m.pickNode(ctx, "hh1", p)
		if err != nil {
			return err.Error()
		}
		return n.nodeID
	}
	if got := pick("kasa"); got != "a" {
		t.Fatal(got) // most recent among matches
	}
	if got := pick("lifx"); got != "b" {
		t.Fatal(got)
	}
	if got := pick("zwave"); got != "c" {
		t.Fatal(got) // no match: every node, most recent
	}
	e.m.settings.Set(ctx, settingPrimaryNode, "b", scopeHN("hh1", ""))
	if got := pick("kasa"); got != "b" {
		t.Fatal(got) // primary among matches
	}
	if got := pick("zwave"); got != "b" {
		t.Fatal(got)
	}
	// M9: offline nodes are skipped; none online fails fast.
	set("b", `["kasa","lifx"]`, time.Hour)
	if got := pick("lifx"); got != "No online node with protocol 'lifx'" {
		t.Fatal(got)
	}
	if got := pick("kasa"); got != "a" {
		t.Fatal(got)
	}
	if _, err := e.m.pickNode(ctx, "hh-empty", ""); err == nil || err.(*statusErr).status != 400 {
		t.Fatal(err)
	}
}

func TestDeviceControlAndState(t *testing.T) {
	controlWait, controlPairWait, stateWait = 300*time.Millisecond, 600*time.Millisecond, 300*time.Millisecond
	defer func() { controlWait, controlPairWait, stateWait = 10*time.Second, 20*time.Second, 10*time.Second }()
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	e.do("POST", "/api/v0/admin/nodes/heartbeat", map[string]any{"protocols": []string{"kasa"}}, n.h()).want(200)
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ? WHERE node_id = 'n2'`, dbTime(e.now.Add(-time.Hour)))
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	e.importDevices(tok,
		map[string]any{"entity_id": "switch.plug", "name": "Plug", "domain": "switch", "source": "direct", "protocol": "kasa", "local_ip": "10.0.0.5"},
		map[string]any{"entity_id": "sensor.t", "name": "T", "domain": "sensor"},
	)
	c.next() // invalidate
	plug := e.deviceByEntity(tok, "switch.plug")["id"].(string)
	sensor := e.deviceByEntity(tok, "sensor.t")["id"].(string)
	ctl := hhPath + "/devices/" + plug + "/control"

	ch := make(chan *resp, 1)
	go func() {
		ch <- e.do("POST", ctl, map[string]any{"action": "turn_on", "data": map[string]any{"brightness": 50}}, bearer(tok))
	}()
	verb, d := command(t, c.next())
	actx := d["context"].(map[string]any)
	if verb != "action" || d["command_name"] != "control_device" || d["action_name"] != "turn_on" || d["reply_request_id"] != d["request_id"] ||
		actx["entity_id"] != "switch.plug" || actx["local_ip"] != "10.0.0.5" || actx["brightness"] != 50.0 || actx["source"] != "direct" {
		t.Fatal(verb, d)
	}
	if _, ok := d["trusted"]; ok {
		t.Fatal("trusted must not be published (D4/D7)")
	}
	rid := d["request_id"].(string)
	// The node verifies the action (no trusted flag), then answers on the node-bound sink.
	if e.do("POST", "/api/v0/commands/"+rid+"/verify", nil, n.h()).json()["valid"] != true {
		t.Fatal("verify")
	}
	e.do("POST", "/api/v0/device-control-results/"+rid, map[string]any{"success": true}, other.h()).want(403)
	e.do("POST", "/api/v0/device-control-results/"+rid, map[string]any{"success": false, "error": "pin", "input_required": map[string]any{"type": "pin"}}, n.h()).want(200)
	r := (<-ch).want(200).json()
	if r["success"] != false || r["entity_id"] != "switch.plug" || r["action"] != "turn_on" || r["error"] != "pin" ||
		r["input_required"].(map[string]any)["type"] != "pin" {
		t.Fatal(r)
	}

	// Timeout: 200 with success false; pair actions get the longer window.
	start := time.Now()
	r = e.do("POST", ctl, map[string]any{"action": "pair_start"}, bearer(tok)).want(200).json()
	if r["success"] != false || r["error"] != "Timed out waiting for node response" || r["input_required"] != nil || time.Since(start) < 500*time.Millisecond {
		t.Fatal(r, time.Since(start))
	}
	c.next()

	e.do("POST", hhPath+"/devices/"+sensor+"/control", map[string]any{"action": "turn_on"}, bearer(tok)).detail(400, "Device is not controllable")
	e.do("POST", hhPath+"/devices/nope/control", map[string]any{"action": "turn_on"}, bearer(tok)).detail(404, "Device not found")
	e.do("POST", ctl, map[string]any{}, bearer(tok)).want(400)

	// State round trip on the device-state topic.
	go func() { ch <- e.do("GET", hhPath+"/devices/"+plug+"/state", nil, bearer(tok)) }()
	pk := c.next()
	p := payload(t, pk)
	if pk.TopicName != "jarvis/nodes/n1/device-state" || p["entity_id"] != "switch.plug" || p["domain"] != "switch" || p["protocol"] != "kasa" {
		t.Fatal(pk.TopicName, p)
	}
	e.do("POST", "/api/v0/device-state-results/"+p["request_id"].(string), map[string]any{"state": map[string]any{"on": true}, "ui_hints": map[string]any{"x": 1}}, n.h()).want(200)
	r = (<-ch).want(200).json()
	if r["entity_id"] != "switch.plug" || r["domain"] != "switch" || r["state"].(map[string]any)["on"] != true || r["error"] != nil {
		t.Fatal(r)
	}
	r = e.do("GET", hhPath+"/devices/"+plug+"/state", nil, bearer(tok)).want(200).json()
	if r["error"] != "Timed out waiting for node response" || r["state"] != nil {
		t.Fatal(r)
	}
	c.next()

	// M9: no online node with the protocol fails fast.
	e.d.Write.Exec(`UPDATE cc_nodes SET last_seen = ?`, dbTime(e.now.Add(-time.Hour)))
	e.do("POST", ctl, map[string]any{"action": "turn_on"}, bearer(tok)).detail(503, "No online node with protocol 'kasa'")

	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	e.do("POST", ctl, map[string]any{"action": "turn_on"}, bearer(stranger)).want(403)
}

func TestSmartHomeConfig(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	e.createNode("x1", "hh2")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	path := hhPath + "/smart-home/config"
	r := e.do("GET", path, nil, bearer(tok)).want(200).json()
	nodes := r["nodes"].([]any)
	if r["device_manager"] != "jarvis_direct" || r["primary_node_id"] != "" || r["use_external_devices"] != false ||
		len(nodes) != 1 || nodes[0].(map[string]any)["online"] != true || nodes[0].(map[string]any)["room"] != "kitchen" {
		t.Fatal(r)
	}
	e.do("PUT", path, map[string]any{"primary_node_id": "x1"}, bearer(tok)).detail(400, "Node not in this household")
	r = e.do("PUT", path, map[string]any{"primary_node_id": "n1", "device_manager": "home_assistant", "use_external_devices": true}, bearer(tok)).want(200).json()
	if r["device_manager"] != "home_assistant" || r["primary_node_id"] != "n1" || r["use_external_devices"] != true || r["nodes"] != nil {
		t.Fatal(r)
	}
	verb, d := command(t, c.next())
	if verb != "toggle_command" || d["command_name"] != "control_device" || d["enabled"] != false {
		t.Fatal(verb, d)
	}
	e.do("PUT", path, map[string]any{"primary_node_id": ""}, bearer(tok)).want(200)
	c.nothing(100 * time.Millisecond)
	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	e.do("GET", path, nil, bearer(stranger)).want(403)
}

func TestDeviceScanAndList(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	room := e.createRoom(tok, "Den", nil)
	e.importDevices(tok,
		map[string]any{"entity_id": "light.a", "name": "A", "domain": "light", "room_id": room["id"]},
		map[string]any{"entity_id": "light.b", "name": "B", "domain": "light", "cloud_id": "cloud-b"},
		map[string]any{"entity_id": "light.c", "name": "C", "domain": "light", "mac_address": "AA:BB:CC"},
	)
	c.next()
	ids := map[string]string{}
	for _, d := range e.do("GET", hhPath+"/devices", nil, bearer(tok)).list() {
		dm := d.(map[string]any)
		ids[dm["entity_id"].(string)] = dm["id"].(string)
	}
	raw := []any{
		map[string]any{"entity_id": "light.a", "name": "A", "domain": "light", "protocol": "lifx"},
		map[string]any{"entity_id": "x.b", "cloud_id": "cloud-b", "domain": "light", "supported_actions": []any{map[string]any{"button_action": "z"}}},
		map[string]any{"entity_id": "x.c", "mac_address": "aa:bb:cc", "domain": "switch", "is_controllable": false},
		map[string]any{"entity_id": "x.new", "domain": "fan"},
	}

	for _, kind := range []string{"device-scan", "device-list"} {
		base := "/api/v0/nodes/n1/" + kind
		e.do("POST", base+"/request", nil, bearer(stranger)).want(403)
		e.do("POST", "/api/v0/nodes/nope/"+kind+"/request", nil, bearer(tok)).detail(404, "Node not found")
		req := e.do("POST", base+"/request", nil, bearer(tok)).want(201).json()
		rid := req["id"].(string)
		pk := c.next()
		if pk.TopicName != "jarvis/nodes/n1/"+kind || payload(t, pk)["request_id"] != rid || req["status"] != "pending" {
			t.Fatal(pk.TopicName, req)
		}
		if kind == "device-list" && payload(t, pk)["manager_name"] != "all" {
			t.Fatal(payload(t, pk))
		}
		poll := e.do("GET", base+"/"+rid, nil, bearer(tok)).want(200).json()
		if poll["status"] != "pending" || poll["devices"] != nil {
			t.Fatal(poll)
		}
		e.do("GET", base+"/"+rid, nil, bearer(stranger)).want(403)
		// D4: the upload is bound to the authenticated node.
		e.do("POST", "/api/v0/nodes/n1/"+kind+"/"+rid+"/results", map[string]any{"devices": raw}, other.h()).
			detail(403, "Cannot upload to other node's requests")
		e.do("POST", "/api/v0/nodes/n1/"+kind+"/"+rid+"/results", map[string]any{}, n.h()).want(400)
		up := map[string]any{"devices": raw, "manager_name": "home_assistant", "can_edit_devices": false}
		e.do("POST", "/api/v0/nodes/n1/"+kind+"/"+rid+"/results", up, n.h()).want(200)
		poll = e.do("GET", base+"/"+rid, nil, bearer(tok)).want(200).json()
		devs := poll["devices"].([]any)
		if poll["status"] != "completed" || poll["device_count"] != 4.0 || len(devs) != 4 {
			t.Fatal(poll)
		}
		a, b, cc, nw := devs[0].(map[string]any), devs[1].(map[string]any), devs[2].(map[string]any), devs[3].(map[string]any)
		if a["existing_device_id"] != ids["light.a"] || b["existing_device_id"] != ids["light.b"] || cc["existing_device_id"] != ids["light.c"] ||
			nw["already_registered"] != false || nw["existing_device_id"] != nil || b["name"] != "Unknown" || cc["is_controllable"] != false {
			t.Fatal(devs)
		}
		if kind == "device-scan" {
			if b["supported_actions"].([]any)[0].(map[string]any)["button_action"] != "z" || a["supported_actions"] != nil {
				t.Fatal(devs) // the node's actions pass through
			}
		} else {
			if a["room_id"] != room["id"] || a["room_name"] != "Den" || b["room_id"] != nil || a["source"] != "direct" ||
				poll["manager_name"] != "home_assistant" || poll["can_edit_devices"] != false || cc["supported_actions"] != nil ||
				len(a["supported_actions"].([]any)) != 3 {
				t.Fatal(poll) // CC recomputes actions (lifx: 3)
			}
		}

		// Failed upload, then expiry → 410 on both sides.
		rid2 := e.do("POST", base+"/request", nil, bearer(tok)).want(201).json()["id"].(string)
		c.next()
		e.do("POST", "/api/v0/nodes/n1/"+kind+"/"+rid2+"/results", map[string]any{"devices": []any{}, "error": "boom"}, n.h()).want(200)
		if p := e.do("GET", base+"/"+rid2, nil, bearer(tok)).want(200).json(); p["status"] != "failed" || p["error_message"] != "boom" {
			t.Fatal(p)
		}
		rid3 := e.do("POST", base+"/request", nil, bearer(tok)).want(201).json()["id"].(string)
		c.next()
		e.advance(3 * time.Minute)
		noun := "Scan request"
		if kind == "device-list" {
			noun = "Device list request"
		}
		e.do("POST", "/api/v0/nodes/n1/"+kind+"/"+rid3+"/results", map[string]any{"devices": []any{}}, n.h()).detail(410, noun+" expired")
		e.do("GET", base+"/"+rid3, nil, bearer(tok)).detail(410, noun+" expired")
		e.do("GET", base+"/nope", nil, bearer(tok)).detail(404, noun+" not found")
		e.advance(-3 * time.Minute)
	}
}

func TestBluetooth(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh9", authn.RoleOwner)
	n := e.createNode("n1", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	base := "/api/v0/nodes/n1"

	// Status before any scan.
	if s := e.do("GET", base+"/bluetooth/status", nil, bearer(tok)).want(200).json(); s["available"] != true || len(s["connected"].([]any)) != 0 {
		t.Fatal(s)
	}
	req := e.do("POST", base+"/bluetooth-scan/request", map[string]any{"role": "sink", "source": "voice", "user_id": 1}, bearer(tok)).want(201).json()
	pk := c.next()
	if pk.TopicName != "jarvis/nodes/n1/bluetooth-scan" || payload(t, pk)["role"] != "sink" {
		t.Fatal(pk.TopicName, string(pk.Payload))
	}
	rid := req["id"].(string)
	e.do("GET", base+"/bluetooth-scan/"+rid, nil, bearer(stranger)).want(403) // D4: legacy had no check
	e.do("POST", base+"/bluetooth-scan/"+rid+"/results", map[string]any{"devices": []any{
		map[string]any{"name": "Speaker", "mac_address": "11", "device_type": "audio", "paired": true, "connected": true},
		map[string]any{"mac_address": "22", "paired": true},
		map[string]any{"name": "Phone", "mac_address": "33"},
	}}, n.h()).want(200)
	p := e.do("GET", base+"/bluetooth-scan/"+rid, nil, bearer(tok)).want(200).json()
	if p["status"] != "completed" || p["device_count"] != 3.0 || p["devices"].([]any)[1].(map[string]any)["name"] != "Unknown" {
		t.Fatal(p)
	}
	s := e.do("GET", base+"/bluetooth/status", nil, bearer(tok)).want(200).json()
	if len(s["connected"].([]any)) != 1 || len(s["paired"].([]any)) != 1 || s["paired"].([]any)[0].(map[string]any)["mac_address"] != "22" {
		t.Fatal(s)
	}
	// The request body is optional (defaults).
	e.do("POST", base+"/bluetooth-scan/request", nil, bearer(tok)).want(201)
	if pl := payload(t, c.next()); pl["role"] != "source" {
		t.Fatal(pl)
	}

	// Pair.
	e.do("POST", base+"/bluetooth/pair", map[string]any{}, bearer(tok)).want(400)
	pr := e.do("POST", base+"/bluetooth/pair", map[string]any{"mac_address": "11"}, bearer(tok)).want(201).json()
	if pl := payload(t, c.next()); pl["mac_address"] != "11" || pl["role"] != "source" || pl["request_id"] != pr["id"] {
		t.Fatal(pl)
	}
	prid := pr["id"].(string)
	if p := e.do("GET", base+"/bluetooth/pair/"+prid, nil, bearer(tok)).want(200).json(); p["status"] != "pending" {
		t.Fatal(p)
	}
	e.do("POST", base+"/bluetooth/pair/"+prid+"/results", map[string]any{"device_name": "x"}, n.h()).want(400)
	e.do("POST", base+"/bluetooth/pair/"+prid+"/results", map[string]any{"success": true, "device_name": "Speaker"}, n.h()).want(200)
	if p := e.do("GET", base+"/bluetooth/pair/"+prid, nil, bearer(tok)).want(200).json(); p["status"] != "completed" || p["device_name"] != "Speaker" {
		t.Fatal(p)
	}
	e.do("GET", base+"/bluetooth/pair/"+prid, nil, bearer(stranger)).want(403)
	e.do("GET", base+"/bluetooth/pair/nope", nil, bearer(tok)).detail(404, "Pair request not found")

	// Fire-and-forget commands, including the two new routes (D8).
	for _, tc := range []struct {
		path  string
		body  any
		topic string
		check func(map[string]any) bool
	}{
		{"disconnect", map[string]any{"mac_address": "11"}, "bluetooth-disconnect", func(p map[string]any) bool { return p["mac_address"] == "11" }},
		{"discoverable", nil, "bluetooth-discoverable", func(p map[string]any) bool { return p["timeout"] == 120.0 }},
		{"release", map[string]any{"mac_address": "11", "forget": true}, "bluetooth-release", func(p map[string]any) bool { return p["forget"] == true }},
		{"release", map[string]any{"mac_address": "11"}, "bluetooth-release", func(p map[string]any) bool { return p["forget"] == false }},
		{"auto-connect", map[string]any{"mac_address": "11", "enabled": false}, "bluetooth-auto-connect", func(p map[string]any) bool { return p["enabled"] == false }},
	} {
		r := e.do("POST", base+"/bluetooth/"+tc.path, tc.body, bearer(tok)).want(202).json()
		if r["status"] != "accepted" || (tc.path == "discoverable") != (r["timeout_seconds"] == 120.0) {
			t.Fatal(tc.path, r)
		}
		pk := c.next()
		if pk.TopicName != "jarvis/nodes/n1/"+tc.topic || !tc.check(payload(t, pk)) {
			t.Fatal(tc.path, pk.TopicName, string(pk.Payload))
		}
	}
	e.do("POST", base+"/bluetooth/auto-connect", map[string]any{"mac_address": "11"}, bearer(tok)).want(400)
	e.do("POST", base+"/bluetooth/disconnect", map[string]any{"mac_address": "11"}, bearer(stranger)).want(403)
	e.do("POST", "/api/v0/nodes/nope/bluetooth/discoverable", nil, bearer(tok)).detail(404, "Node not found")
}

func TestCamerasDeferred(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(1, "hh1", authn.RoleMember)
	e.importDevices(tok, map[string]any{"entity_id": "camera.door", "name": "Door", "domain": "camera", "protocol": "nest"})
	cams := e.do("GET", hhPath+"/cameras", nil, bearer(tok)).want(200).list()
	cam := cams[0].(map[string]any)
	if len(cams) != 1 || cam["entity_id"] != "camera.door" || cam["is_streaming"] != false || cam["protocol"] != "nest" {
		t.Fatal(cams)
	}
	e.do("POST", hhPath+"/cameras/"+cam["device_id"].(string)+"/stream", map[string]any{}, bearer(tok)).
		detail(501, "Camera streaming is not available in this version")
	e.do("POST", hhPath+"/cameras/nope/stream", map[string]any{}, bearer(tok)).detail(404, "Camera not found")
	if r := e.do("DELETE", hhPath+"/cameras/x/stream", nil, bearer(tok)).want(200).json(); r["status"] != "not_streaming" {
		t.Fatal(r)
	}
	e.do("GET", "/api/v0/cameras/stream/cam_x/stream.m3u8", nil, bearer(tok)).detail(404, "Stream not found")
}

func TestSmartHomeRetentionAndPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	base := "/api/v0/nodes/n1"
	scan := func() string {
		rid := e.do("POST", base+"/bluetooth-scan/request", map[string]any{"user_id": 7}, bearer(tok)).want(201).json()["id"].(string)
		e.do("POST", base+"/bluetooth-scan/"+rid+"/results", map[string]any{"devices": []any{}}, n.h()).want(200)
		return rid
	}
	scan()
	newest := scan()
	e.do("POST", base+"/device-scan/request", nil, bearer(tok)).want(201)
	e.d.Write.Exec(`INSERT INTO cc_config_pushes (id, node_id, config_type, ciphertext, nonce, tag, status, created_at, consumed_at)
		VALUES ('p1', 'n1', 'node_config', 'c', 'n', 't', 'consumed', ?, ?)`, dbTime(e.now), dbTime(e.now))
	e.d.Write.Exec(`INSERT INTO cc_auth_sessions (id, provider, node_id, user_id, status, state, client_id, created_at, expires_at)
		VALUES ('s1', 'nest', 'n1', 7, 'consumed', 'st', 'cid', ?, ?)`, dbTime(e.now), dbTime(e.now.Add(authSessionTTL)))

	count := func(table string) int {
		var c int
		e.d.Read.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&c)
		return c
	}
	if err := e.m.cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if count("cc_bluetooth_scan_requests") != 2 || count("cc_device_scan_requests") != 1 || count("cc_config_pushes") != 1 || count("cc_auth_sessions") != 1 {
		t.Fatal("swept too early")
	}
	e.advance(25 * time.Hour)
	if err := e.m.cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var kept string
	e.d.Read.QueryRow(`SELECT id FROM cc_bluetooth_scan_requests`).Scan(&kept)
	if count("cc_bluetooth_scan_requests") != 1 || kept != newest || count("cc_device_scan_requests") != 0 ||
		count("cc_config_pushes") != 0 || count("cc_auth_sessions") != 0 {
		t.Fatal("not swept", count("cc_bluetooth_scan_requests"), count("cc_device_scan_requests"), count("cc_config_pushes"), count("cc_auth_sessions"))
	}
	// Bluetooth status still answers from the kept scan.
	e.do("GET", base+"/bluetooth/status", nil, bearer(tok)).want(200)

	// D20: the user's OAuth sessions go, their id leaves scan rows; rooms and devices stay.
	e.d.Write.Exec(`INSERT INTO cc_auth_sessions (id, provider, node_id, user_id, status, state, client_id, created_at, expires_at)
		VALUES ('s2', 'nest', 'n1', 7, 'active', 'st2', 'cid', ?, ?)`, dbTime(e.now), dbTime(e.now.Add(authSessionTTL)))
	e.createRoom(tok, "Kept", nil)
	tx, _ := e.d.Write.Begin()
	if err := e.m.PurgeUser(ctx, tx, 7); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	var uid any
	e.d.Read.QueryRow(`SELECT user_id FROM cc_bluetooth_scan_requests`).Scan(&uid)
	if count("cc_auth_sessions") != 0 || uid != nil || count("cc_rooms") != 1 {
		t.Fatal(count("cc_auth_sessions"), uid, count("cc_rooms"))
	}
}
