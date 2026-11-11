//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// Node binding is additive and jarvisd-only. Older imports remain valid, but cannot
// clear a new client's binding or replace its connection through an unspecified node.
func TestCCDeviceNodeBinding(t *testing.T) {
	if !Jarvisd() {
		t.Skip("contract: device node binding is jarvisd-only; set " + EnvImpl + "=jarvisd")
	}
	tg := T(t)
	u := NewUser(t)
	n, other := NewCCNode(t, u), NewCCNode(t, u)
	path := "/api/v0/households/" + u.HouseholdID + "/devices"
	dev := map[string]any{"entity_id": "light.bound", "name": "Bound lamp", "domain": "light", "source": "direct", "protocol": "fixture"}
	imported := Obj{"created": Eq(1), "updated": Eq(0)}
	tg.Post(t, CommandCenter, path+"/import", map[string]any{"node_id": n.ID, "devices": []any{dev}}, u.H()).Expect(201, imported)
	shape := Obj{
		"id": UUID, "household_id": Eq(u.HouseholdID), "entity_id": Eq("light.bound"), "name": Eq("Bound lamp"), "domain": Eq("light"),
		"room_id": Null, "room_name": Null, "source": Eq("direct"), "protocol": Eq("fixture"), "node_id": Eq(n.ID),
		"device_class": Null, "manufacturer": Null, "model": Null, "local_ip": Null, "mac_address": Null, "cloud_id": Null, "ha_device_id": Null,
		"is_controllable": Eq(true), "is_active": Eq(true), "created_at": TimestampNaive, "updated_at": TimestampNaive,
		"supported_actions": ArrayOf(Obj{"button_text": String, "button_action": String, "button_type": String, "button_icon": String}),
	}
	rows := tg.Get(t, CommandCenter, path, u.H()).Expect(200, All(ArrayOf(shape), ccArrayLen(1))).JSON().([]any)
	id := rows[0].(map[string]any)["id"].(string)
	tg.Post(t, CommandCenter, path+"/import", map[string]any{"node_id": other.ID, "devices": []any{dev}}, u.H()).ExpectError(409, "Device is already assigned to another node")
	tg.Post(t, CommandCenter, path+"/import", map[string]any{"devices": []any{dev}}, u.H()).Expect(201, Obj{"created": Eq(0), "updated": Eq(1)})
	tg.Get(t, CommandCenter, path, u.H()).Expect(200, ArrayOf(shape))
	dev["source"] = "home_assistant"
	tg.Post(t, CommandCenter, path+"/import", map[string]any{"devices": []any{dev}}, u.H()).ExpectError(409, "Rediscover this device using its assigned node to update its connection")
	tg.Post(t, CommandCenter, path+"/"+id+"/control", map[string]any{"action": "pair_finish", "data": map[string]any{"session_id": "missing", "pin": "0123"}}, u.H()).ExpectError(409, "Pairing session expired or invalid; start pairing again")
	tg.Do(t, CommandCenter, http.MethodDelete, path+"/"+id, nil, u.H()).ExpectStatus(http.StatusNoContent)
}
