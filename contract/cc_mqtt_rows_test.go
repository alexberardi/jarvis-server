//go:build contract

package contract

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// The rest of the MQTT topic catalogue (docs/cc/05 §2.4, §3.5; doc 07 smart home and
// bluetooth; doc 10 signals; doc 12 packages), in the style of TestCCMQTTCatalogue: a fake
// node subscribes, each subtest triggers the publish through its real HTTP route, the exact
// topic, QoS 1, retain=false and payload key set are frozen, and the node's side of the round
// trip (HTTP pull, result POST, poll) is driven and frozen too.
//
// Rows: 5 config/push, 7 device-scan, 8 device-list, 9 device-state, 11 bluetooth-scan,
// 12 bluetooth-pair, 16 package-install, 17 package-uninstall, 18 package-revert,
// 22 jarvis/auth/{provider}/ready. Verbs on the commands topic: report_tools, tool_call,
// toggle_command, invalidate_device_cache, device_removed.
//
// Not covered: row 21 context/query. Its only publisher is the phone-call plan draft
// (phone_call_service.apply_availability_envelope), reached from the make_phone_call server
// tool or an errand plan, i.e. only behind an LLM turn with phone calls enabled; there is no
// deterministic black-box trigger. (10 camera-credentials is deferred by D29; 19 test-install
// was ported 2026-10-08 but needs a live Pantry share code, so it is unit-tested only.)
//
// Everything runs in a throwaway user's solo household with exactly one CC node, so the
// household broadcasts (toggle_command, invalidate_device_cache) and the "pick a node for the
// protocol" fallback all land on the fake node.

// rowsProvAuthErrors are verify_provisioning_auth's answers (provisioning.py), shared by the
// smart-home, bluetooth, package and oauth routes ("prov" auth: admin key or a user JWT).
const (
	rowsProvNoAuth = "Authentication required"
	rowsProvBadJWT = "Invalid or expired JWT"
	rowsProvBadKey = "Invalid API key"
	rowsNotMember  = "User is not a member of this household"
)

// rowsCreatedShape is the {id, status:"pending", created_at} answer every request/poll job
// (device scan/list, bluetooth scan/pair, package install/uninstall/revert) returns on create.
var rowsCreatedShape = Obj{"id": UUID, "status": Eq("pending"), "created_at": TimestampNaive}

func rowsID(t *testing.T, r *Resp) string {
	t.Helper()
	return r.Expect(http.StatusCreated, rowsCreatedShape).Object()["id"].(string)
}

func TestCCMQTTRows(t *testing.T) {
	tg := T(t)
	tg.NeedMQTT(t)
	u := NewUser(t)
	n := NewCCNode(t, u)
	nid, hh := n.ID, u.HouseholdID
	base := "jarvis/nodes/" + nid + "/"
	nodePath := "/api/v0/nodes/" + nid

	c := DialMQTTNode(t, n)
	c.Subscribe(t, base+"#")
	// Row 22 goes to every node; the node filters on node_id (mqtt_tts_listener.py:1485-1491).
	c.Subscribe(t, "jarvis/auth/+/ready")

	stranger := NewUser(t)

	t.Run("prov_auth", func(t *testing.T) {
		path := nodePath + "/device-scan/request"
		tg.Post(t, CommandCenter, path, nil).ExpectError(http.StatusUnauthorized, rowsProvNoAuth)
		tg.Post(t, CommandCenter, path, nil, Bearer("not.a.jwt")).ExpectError(http.StatusUnauthorized, rowsProvBadJWT)
		tg.Post(t, CommandCenter, path, nil, H{"X-API-Key": "wrong-admin-key"}).ExpectError(http.StatusUnauthorized, rowsProvBadKey)
		tg.Post(t, CommandCenter, path, nil, stranger.H()).ExpectError(http.StatusForbidden, rowsNotMember)
		tg.Post(t, CommandCenter, "/api/v0/nodes/contract-nosuch-"+randHex(3)+"/device-scan/request", nil, u.H()).
			ExpectError(http.StatusNotFound, "Node not found")
	})

	t.Run("config_push", func(t *testing.T) {
		blob := map[string]any{"config_type": "contract", "ciphertext": "Y29udHJhY3Q", "nonce": "bm9uY2U", "tag": "dGFn"}
		r := tg.Post(t, CommandCenter, nodePath+"/config/push", blob, u.H()).Expect(http.StatusCreated, Obj{
			"id": UUID, "node_id": Eq(nid), "config_type": Eq("contract"), "status": Eq("pending"), "created_at": TimestampNaive,
		}).Object()
		pid := r["id"].(string)
		// Row 5: a nudge only; the node pulls the ciphertext over HTTP.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"config/push"),
			Obj{"push_id": Eq(pid), "config_type": Eq("contract"), "node_id": Eq(nid)})

		// The real node sends its X-API-Key on the pull and the ack. D6: jarvisd requires
		// it and binds it to the path node.
		pending := tg.Get(t, CommandCenter, nodePath+"/config/pending", n.APIKeyH()).Expect(http.StatusOK, ArrayOf(Obj{
			"id": UUID, "config_type": String, "ciphertext": String, "nonce": String, "tag": String, "created_at": TimestampNaive,
		})).JSON().([]any)
		if findBy(pending, "id", pid) == nil {
			t.Fatalf("push %s not in pending list: %v", pid, pending)
		}
		if !Jarvisd() {
			// LEGACY-BUG: config/pending and config/{id}/ack take no auth at all (doc 05
			// §2.4 row 5, D6). Anyone who knows a node id can read its encrypted pushes.
			tg.Get(t, CommandCenter, nodePath+"/config/pending").ExpectStatus(http.StatusOK)
		}
		ack := nodePath + "/config/" + pid + "/ack"
		tg.Post(t, CommandCenter, ack, nil, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("consumed")})
		tg.Post(t, CommandCenter, ack, nil, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("already_consumed")})
		tg.Post(t, CommandCenter, nodePath+"/config/00000000-0000-4000-8000-000000000000/ack", nil, n.APIKeyH()).
			ExpectError(http.StatusNotFound, "Config push not found")

		// auth:* pushes carry tokens: deleted on ack.
		blob["config_type"] = "auth:contract"
		pid2 := tg.Post(t, CommandCenter, nodePath+"/config/push", blob, u.H()).ExpectStatus(http.StatusCreated).Object()["id"].(string)
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"config/push"),
			Obj{"push_id": Eq(pid2), "config_type": Eq("auth:contract"), "node_id": Eq(nid)})
		tg.Post(t, CommandCenter, nodePath+"/config/"+pid2+"/ack", nil, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("consumed_and_deleted")})
		tg.Post(t, CommandCenter, nodePath+"/config/"+pid2+"/ack", nil, n.APIKeyH()).ExpectError(http.StatusNotFound, "Config push not found")
	})

	t.Run("device_scan", func(t *testing.T) {
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/device-scan/request", nil, u.H()))
		// Row 7.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"device-scan"), Obj{"request_id": Eq(rid)})

		poll := nodePath + "/device-scan/" + rid
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("pending"), "request_id": Eq(rid), "devices": Null, "device_count": Null, "error_message": Null,
		})
		other := NewCCNode(t, u)
		res := map[string]any{"devices": []map[string]any{{
			"name": "Contract Bulb", "domain": "light", "protocol": "contractproto", "entity_id": "light.contract_scan",
			"local_ip": "192.0.2.20", "mac_address": "02:00:00:00:00:20",
		}}}
		tg.Post(t, CommandCenter, "/api/v0/nodes/"+other.ID+"/device-scan/"+rid+"/results", res, other.APIKeyH()).
			ExpectError(http.StatusNotFound, "Scan request not found")
		tg.Post(t, CommandCenter, poll+"/results", res, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("completed"), "request_id": Eq(rid), "device_count": Eq(1), "error_message": Null,
			"devices": All(ArrayOf(Obj{
				"name": Eq("Contract Bulb"), "domain": Eq("light"), "manufacturer": Null, "model": Null,
				"protocol": Eq("contractproto"), "entity_id": Eq("light.contract_scan"), "local_ip": Eq("192.0.2.20"),
				"mac_address": Eq("02:00:00:00:00:20"), "cloud_id": Null, "device_class": Null, "is_controllable": Eq(true),
				"already_registered": Eq(false), "existing_device_id": Null, "supported_actions": Null,
			}), ccArrayLen(1)),
		})
		tg.Get(t, CommandCenter, poll, stranger.H()).ExpectError(http.StatusForbidden, rowsNotMember)

		// A node-reported failure.
		rid2 := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/device-scan/request", nil, u.H()))
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"device-scan"), Obj{"request_id": Eq(rid2)})
		tg.Post(t, CommandCenter, nodePath+"/device-scan/"+rid2+"/results", map[string]any{"devices": []any{}, "error": "contract: no adapters"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, nodePath+"/device-scan/"+rid2, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("failed"), "request_id": Eq(rid2), "devices": Null, "device_count": Null, "error_message": Eq("contract: no adapters"),
		})
	})

	t.Run("device_list", func(t *testing.T) {
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/device-list/request", nil, u.H()))
		// Row 8: the manager is always "all" (every enabled manager on the node).
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"device-list"), Obj{"request_id": Eq(rid), "manager_name": Eq("all")})

		poll := nodePath + "/device-list/" + rid
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("pending"), "request_id": Eq(rid), "manager_name": Eq("all"), "can_edit_devices": Null,
			"devices": Null, "device_count": Null, "error_message": Null,
		})
		tg.Post(t, CommandCenter, poll+"/results", map[string]any{
			"devices":      []map[string]any{{"name": "Contract Switch", "domain": "switch", "entity_id": "switch.contract_list", "state": "on", "area": "Den"}},
			"manager_name": "contract_manager", "can_edit_devices": false,
		}, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("completed"), "request_id": Eq(rid), "manager_name": Eq("contract_manager"), "can_edit_devices": Eq(false),
			"device_count": Eq(1), "error_message": Null,
			"devices": All(ArrayOf(Obj{
				"name": Eq("Contract Switch"), "domain": Eq("switch"), "entity_id": Eq("switch.contract_list"), "is_controllable": Eq(true),
				"manufacturer": Null, "model": Null, "protocol": Null, "local_ip": Null, "mac_address": Null, "cloud_id": Null,
				"device_class": Null, "source": Eq("direct"), "area": Eq("Den"), "state": Eq("on"),
				"already_registered": Eq(false), "existing_device_id": Null, "room_id": Null, "room_name": Null,
				// Domain fallback actions (no protocol): switch → on/off.
				"supported_actions": Eq([]any{
					map[string]any{"button_text": "Turn On", "button_action": "turn_on", "button_type": "primary", "button_icon": "power"},
					map[string]any{"button_text": "Turn Off", "button_action": "turn_off", "button_type": "secondary", "button_icon": "power-off"},
				}),
			}), ccArrayLen(1)),
		})
		tg.Post(t, CommandCenter, nodePath+"/device-list/00000000-0000-4000-8000-000000000000/results", map[string]any{"devices": []any{}}, n.APIKeyH()).
			ExpectError(http.StatusNotFound, "Device list request not found")
	})

	t.Run("devices_state_and_broadcasts", func(t *testing.T) {
		entity := "light.contract_" + randHex(3)
		dev := map[string]any{
			"entity_id": entity, "name": "Contract Lamp " + tg.RunID, "domain": "light", "source": "direct",
			"protocol": "contractproto", "local_ip": "192.0.2.10", "mac_address": "02:00:00:00:00:10",
		}
		hp := "/api/v0/households/" + hh + "/devices"
		tg.Post(t, CommandCenter, hp+"/import", map[string]any{"devices": []any{dev}}, u.H()).
			Expect(http.StatusCreated, Obj{"created": Eq(1), "updated": Eq(0)})
		// Verb invalidate_device_cache: broadcast to every active household node, no details.
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "invalidate_device_cache")), commandShape("invalidate_device_cache", Obj{}))

		list := tg.Get(t, CommandCenter, hp, u.H()).ExpectStatus(http.StatusOK).JSON().([]any)
		d := findBy(list, "entity_id", entity)
		if d == nil {
			t.Fatalf("imported device missing from %v", list)
		}
		did := d["id"].(string)
		deleted := false
		t.Cleanup(func() {
			if !deleted {
				tg.do(CommandCenter, http.MethodDelete, hp+"/"+did, nil, u.H())
			}
		})

		// Row 9: CC picks the node (protocol match, else primary, else any active node) and
		// blocks up to 10 s for the node's POST to /device-state-results/{rid}.
		pending := tg.goDo(CommandCenter, http.MethodGet, hp+"/"+did+"/state", nil, u.H())
		v := ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"device-state"), Obj{
			"request_id": UUID, "entity_id": Eq(entity), "domain": Eq("light"), "protocol": Eq("contractproto"),
			"source": Eq("direct"), "cloud_id": Null, "local_ip": Eq("192.0.2.10"), "mac_address": Eq("02:00:00:00:00:10"),
		})
		rid := v.(map[string]any)["request_id"].(string)
		// LEGACY-BUG: /device-state-results is unauthenticated (doc 05 §8.2). D4: node auth,
		// and the rid must have been issued to that node (resultSinkH).
		tg.Post(t, CommandCenter, "/api/v0/device-state-results/"+rid, map[string]any{
			"entity_id": entity, "domain": "light", "state": map[string]any{"on": true}, "ui_hints": map[string]any{"kind": "toggle"},
		}, resultSinkH(n)).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{
			"entity_id": Eq(entity), "domain": Eq("light"), "state": Obj{"on": Eq(true)}, "ui_hints": Obj{"kind": Eq("toggle")}, "error": Null,
		})

		// Verb device_removed (direct-protocol devices only, sent to the protocol's node before
		// the row goes), then the cache broadcast.
		tg.Do(t, CommandCenter, http.MethodDelete, hp+"/"+did, nil, u.H()).ExpectStatus(http.StatusNoContent)
		deleted = true
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "device_removed")), commandShape("device_removed", Obj{
			"entity_id": Eq(entity), "protocol": Eq("contractproto"), "domain": Eq("light"), "cloud_id": Null,
			"local_ip": Eq("192.0.2.10"), "mac_address": Eq("02:00:00:00:00:10"), "name": Eq(dev["name"]),
		}))
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "invalidate_device_cache")), commandShape("invalidate_device_cache", Obj{}))
		tg.Do(t, CommandCenter, http.MethodDelete, hp+"/"+did, nil, u.H()).ExpectError(http.StatusNotFound, "Device not found")
	})

	t.Run("toggle_command", func(t *testing.T) {
		cfg := "/api/v0/households/" + hh + "/smart-home/config"
		cfgShape := func(external bool) Obj {
			return Obj{"device_manager": String, "primary_node_id": String, "use_external_devices": Eq(external)}
		}
		// Verb toggle_command: use_external_devices disables the built-in control_device on
		// every household node (a Pantry package provides it), and re-enables it when off.
		tg.Do(t, CommandCenter, http.MethodPut, cfg, map[string]any{"use_external_devices": true}, u.H()).Expect(http.StatusOK, cfgShape(true))
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "toggle_command")),
			commandShape("toggle_command", Obj{"command_name": Eq("control_device"), "enabled": Eq(false)}))
		tg.Do(t, CommandCenter, http.MethodPut, cfg, map[string]any{"use_external_devices": false}, u.H()).Expect(http.StatusOK, cfgShape(false))
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "toggle_command")),
			commandShape("toggle_command", Obj{"command_name": Eq("control_device"), "enabled": Eq(true)}))
		// Fields not sent publish nothing.
		tg.Do(t, CommandCenter, http.MethodPut, cfg, map[string]any{"primary_node_id": nid}, u.H()).Expect(http.StatusOK, Obj{
			"device_manager": String, "primary_node_id": Eq(nid), "use_external_devices": Eq(false),
		})
		tg.Do(t, CommandCenter, http.MethodPut, cfg, map[string]any{"primary_node_id": "contract-nosuch"}, u.H()).
			ExpectError(http.StatusBadRequest, "Node not in this household")
	})

	t.Run("bluetooth_scan", func(t *testing.T) {
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/bluetooth-scan/request", map[string]any{"role": "sink"}, u.H()))
		// Row 11.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-scan"), Obj{"request_id": Eq(rid), "role": Eq("sink")})
		poll := nodePath + "/bluetooth-scan/" + rid
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("pending"), "request_id": Eq(rid), "devices": Null, "device_count": Null, "error_message": Null,
		})
		tg.Post(t, CommandCenter, poll+"/results", map[string]any{"devices": []map[string]any{
			{"name": "Contract Speaker", "mac_address": "00:11:22:33:44:66", "device_type": "audio", "paired": true},
		}}, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("completed"), "request_id": Eq(rid), "device_count": Eq(1), "error_message": Null,
			"devices": All(ArrayOf(Obj{
				"name": Eq("Contract Speaker"), "mac_address": Eq("00:11:22:33:44:66"), "device_type": Eq("audio"),
				"paired": Eq(true), "connected": Eq(false),
			}), ccArrayLen(1)),
		})
		tg.Post(t, CommandCenter, nodePath+"/bluetooth-scan/00000000-0000-4000-8000-000000000000/results", map[string]any{}, n.APIKeyH()).
			ExpectError(http.StatusNotFound, "Scan request not found")

		// role defaults to "source".
		rid2 := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/bluetooth-scan/request", map[string]any{}, u.H()))
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-scan"), Obj{"request_id": Eq(rid2), "role": Eq("source")})
	})

	t.Run("bluetooth_pair", func(t *testing.T) {
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/bluetooth/pair", map[string]any{"mac_address": "00:11:22:33:44:77"}, u.H()))
		// Row 12.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-pair"),
			Obj{"request_id": Eq(rid), "mac_address": Eq("00:11:22:33:44:77"), "role": Eq("source")})
		poll := nodePath + "/bluetooth/pair/" + rid
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("pending"), "request_id": Eq(rid), "device_name": Null, "error_message": Null,
		})
		tg.Post(t, CommandCenter, poll+"/results", map[string]any{"success": true, "device_name": "Contract Speaker"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, poll, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("completed"), "request_id": Eq(rid), "device_name": Eq("Contract Speaker"), "error_message": Null,
		})

		rid2 := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/bluetooth/pair", map[string]any{"mac_address": "00:11:22:33:44:78", "role": "sink"}, u.H()))
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-pair"),
			Obj{"request_id": Eq(rid2), "mac_address": Eq("00:11:22:33:44:78"), "role": Eq("sink")})
		tg.Post(t, CommandCenter, nodePath+"/bluetooth/pair/"+rid2+"/results", map[string]any{"success": false, "error": "contract: refused"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, nodePath+"/bluetooth/pair/"+rid2, u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("failed"), "request_id": Eq(rid2), "device_name": Null, "error_message": Eq("contract: refused"),
		})
	})

	pollShape := func(status, rid, cmd string, errMsg, det Matcher) Obj {
		return Obj{"status": Eq(status), "request_id": Eq(rid), "command_name": Eq(cmd), "error_message": errMsg, "details": det}
	}

	t.Run("package_install", func(t *testing.T) {
		const repo = "https://github.com/contract-example/jarvis-cmd-contract"
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/package-install", map[string]any{
			"command_name": "contract_pkg", "github_repo_url": repo, "git_tag": "v0.0.1",
		}, u.H()))
		// Row 16. The URL here is untrusted; the node installs from the verify answer.
		installMsg := Obj{
			"request_id": Eq(rid), "command_name": Eq("contract_pkg"), "github_repo_url": Eq(repo), "git_tag": Eq("v0.0.1"),
		}
		verifyAns := Obj{
			"confirmed": Eq(true), "command_name": Eq("contract_pkg"), "github_repo_url": Eq(repo), "git_tag": Eq("v0.0.1"),
		}
		if Jarvisd() {
			// D48 (additive): the household's Pantry base URL rides along on the publish and
			// the verify answer, so a node installs from its own household's store.
			installMsg["pantry_url"] = NonEmptyString
			verifyAns["pantry_url"] = NonEmptyString
		}
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"package-install"), installMsg)
		p := nodePath + "/package-install/" + rid
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("pending", rid, "contract_pkg", Null, Null))

		other := NewCCNode(t, u)
		tg.Get(t, CommandCenter, "/api/v0/nodes/"+other.ID+"/package-install/"+rid+"/verify", other.APIKeyH()).
			ExpectError(http.StatusNotFound, "Package install request not found")
		tg.Get(t, CommandCenter, p+"/verify", n.APIKeyH()).Expect(http.StatusOK, verifyAns)

		// restarting is non-terminal: the node reposts the real result after boot.
		tg.Post(t, CommandCenter, p+"/results", map[string]any{"success": true, "restarting": true}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("restarting", rid, "contract_pkg", Null, Null))
		tg.Get(t, CommandCenter, p+"/verify", n.APIKeyH()).ExpectError(http.StatusConflict, "Request already restarting")

		tg.Post(t, CommandCenter, p+"/results", map[string]any{"success": true, "details": map[string]any{"version": "0.0.1"}}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("completed", rid, "contract_pkg", Null, Obj{"version": Eq("0.0.1")}))
		tg.Get(t, CommandCenter, p+"/verify", n.APIKeyH()).ExpectError(http.StatusConflict, "Request already completed")
		tg.Get(t, CommandCenter, p, stranger.H()).ExpectError(http.StatusForbidden, rowsNotMember)
		tg.Post(t, CommandCenter, nodePath+"/package-install/00000000-0000-4000-8000-000000000000/results", map[string]any{"success": true}, n.APIKeyH()).
			ExpectError(http.StatusNotFound, "Install request not found")
	})

	t.Run("package_uninstall", func(t *testing.T) {
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/package-uninstall", map[string]any{
			"command_name": "contract_pkg", "component_type": "command",
		}, u.H()))
		// Row 17.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"package-uninstall"),
			Obj{"request_id": Eq(rid), "command_name": Eq("contract_pkg"), "component_type": Eq("command")})
		p := nodePath + "/package-uninstall/" + rid
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("pending", rid, "contract_pkg", Null, Null))
		tg.Post(t, CommandCenter, p+"/results", map[string]any{"success": false, "error": "contract: busy"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("failed", rid, "contract_pkg", Eq("contract: busy"), Null))
		tg.Post(t, CommandCenter, nodePath+"/package-uninstall/00000000-0000-4000-8000-000000000000/results", map[string]any{"success": true}, n.APIKeyH()).
			ExpectError(http.StatusNotFound, "Uninstall request not found")
	})

	t.Run("package_revert", func(t *testing.T) {
		// Either name field is accepted; neither is a plain 422 detail (not the CC validation body).
		tg.Post(t, CommandCenter, nodePath+"/package-revert", map[string]any{}, u.H()).
			ExpectError(http.StatusUnprocessableEntity, "command_name or package_name is required")
		rid := rowsID(t, tg.Post(t, CommandCenter, nodePath+"/package-revert", map[string]any{"package_name": "contract_pkg"}, u.H()))
		// Row 18: both names carry the same value.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"package-revert"),
			Obj{"request_id": Eq(rid), "command_name": Eq("contract_pkg"), "package_name": Eq("contract_pkg")})
		p := nodePath + "/package-revert/" + rid
		tg.Post(t, CommandCenter, p+"/results", map[string]any{"success": true}, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, p, u.H()).Expect(http.StatusOK, pollShape("completed", rid, "contract_pkg", Null, Null))
	})

	t.Run("report_tools", func(t *testing.T) {
		pending := tg.goDo(CommandCenter, http.MethodGet, "/api/v0/mobile/nodes/"+nid+"/tools", nil, u.H())
		// Verb report_tools. LEGACY-BUG: trusted:true rides along (D4/D7 drop it).
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "report_tools")),
			commandShape("report_tools", legacyTrusted(Obj{"reply_request_id": UUID, "trusted": Eq(true)})))
		d := details(v)
		if d["reply_request_id"] != d["request_id"] {
			t.Fatalf("report_tools reply_request_id should equal request_id: %v", d)
		}
		report := rowsToolReport()
		// LEGACY-BUG: /mobile/node-tool-reports is unauthenticated (doc 05 §3.5). D4: node
		// auth, rid bound to the node.
		tg.Post(t, CommandCenter, "/api/v0/mobile/node-tool-reports/"+d["reply_request_id"].(string), report, resultSinkH(n)).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{
			"client_tools":       Eq(report["client_tools"]),
			"available_commands": Eq(report["available_commands"]),
			"installed_packages": Eq(report["installed_packages"]),
		})
		tg.Get(t, CommandCenter, "/api/v0/mobile/nodes/contract-nosuch-"+randHex(3)+"/tools", u.H()).ExpectError(http.StatusNotFound, "Node not found")
		tg.Get(t, CommandCenter, "/api/v0/mobile/nodes/"+nid+"/tools", stranger.H()).ExpectError(http.StatusForbidden, rowsNotMember)
	})

	t.Run("tool_call_leave_by", func(t *testing.T) {
		// Verb tool_call (dispatch_node_command). Its one deterministic, LLM-free trigger is
		// the appt.upcoming → leave-by reaction (signal_reaction_bridge.py, doc 10): gated on
		// proposals.enabled, it asks the node for its commands (report_tools, 4 s) and, if it
		// advertises get_drive_time, runs it headless. A no-route answer ends the reaction
		// without a card.
		tg.Do(t, CommandCenter, http.MethodPut, "/api/v0/mobile/household/"+hh+"/settings/proposals.enabled",
			map[string]any{"value": true}, u.H()).Expect(http.StatusOK, Obj{"success": Eq(true), "key": Eq("proposals.enabled"), "value": Eq(true)})
		t.Cleanup(func() {
			tg.do(CommandCenter, http.MethodPut, "/api/v0/mobile/household/"+hh+"/settings/proposals.enabled", map[string]any{"value": false}, u.H())
		})

		event := "contract-" + randHex(4)
		tg.Post(t, CommandCenter, "/api/v0/signals", map[string]any{
			"signal": map[string]any{
				"kind": "appt.upcoming", "source_key": "contract:" + event, "ttl_seconds": 60,
				"source_agent": "contract-" + tg.RunID, "scope": map[string]any{"user_id": u.ID},
			},
			"data": map[string]any{
				"event_id": event, "title": "Contract appointment", "location": "Contract Dentist",
				"start_iso": time.Now().UTC().Add(3 * time.Hour).Format(time.RFC3339),
			},
		}, n.APIKeyH()).Expect(http.StatusOK, Obj{"signal_id": Int, "mode": Eq("open"), "proposed": Eq(false)})

		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "report_tools")),
			commandShape("report_tools", legacyTrusted(Obj{"reply_request_id": UUID, "trusted": Eq(true)})))
		report := rowsToolReport()
		report["available_commands"] = []any{map[string]any{"command_name": "get_drive_time", "description": "contract"}}
		tg.Post(t, CommandCenter, "/api/v0/mobile/node-tool-reports/"+details(v)["reply_request_id"].(string), report, resultSinkH(n)).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})

		v = ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "tool_call")), commandShape("tool_call", legacyTrusted(Obj{
			"command_name":     Eq("get_drive_time"),
			"arguments":        Obj{"destination": Eq("Contract Dentist"), "resolution": Eq("strict")},
			"tool_call_id":     UUID,
			"reply_request_id": UUID,
			"trusted":          Eq(true), // LEGACY-BUG: see report_tools.
			"user_id":          Eq(u.ID),
		})))
		d := details(v)
		if d["reply_request_id"] != d["request_id"] || d["tool_call_id"] != d["request_id"] {
			t.Fatalf("tool_call ids should all equal request_id: %v", d)
		}
		// The node answers {"output": {...}} on the shared device-control sink.
		tg.Post(t, CommandCenter, "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{
			"output": map[string]any{"success": false, "reason": "generic_location"},
		}, resultSinkH(n)).Expect(http.StatusOK, Obj{"status": Eq("ok")})
	})

	t.Run("auth_ready", func(t *testing.T) {
		provider := "contract" + randHex(3)
		// sessionBody names the token endpoint either as an absolute exchange_url, or (base !=
		// "") as provider_base_url + exchange_path, the LAN-provider mode.
		sessionBody := func(exchange, base string) map[string]any {
			ac := map[string]any{
				"provider": provider, "client_id": "contract-client", "keys": []string{"access_token"},
				"authorize_url": "https://auth.contract.invalid/authorize",
				"scopes":        []string{"read"}, "supports_pkce": true,
				// Native redirect: mobile posts the code to /exchange (no relay, no
				// externally reachable CC needed).
				"native_redirect_uri": "jarvis://contract-callback",
			}
			body := map[string]any{"provider": provider, "node_id": nid, "auth_config": ac}
			if base != "" {
				body["provider_base_url"] = base
				ac["exchange_path"] = exchange
			} else {
				ac["exchange_url"] = exchange
			}
			return body
		}
		sessionAt := func(exchange, base string) map[string]any {
			return tg.Post(t, CommandCenter, "/api/v0/oauth/sessions", sessionBody(exchange, base), u.H()).Expect(http.StatusCreated, Obj{
				"session_id": UUID, "authorize_url": Regexp(`^https://auth\.contract\.invalid/authorize\?`), "requires_code_exchange": Eq(true),
			}).Object()
		}
		session := func(exchangeURL string) map[string]any { return sessionAt(exchangeURL, "") }
		host := os.Getenv(EnvOCRCallbackHost)
		if Jarvisd() {
			// SSRF fence (D4): an absolute exchange_url (an external provider) must be https on
			// a public address; a LAN provider names provider_base_url, which may be private but
			// never loopback. So the dead endpoint and the live one below go through the LAN
			// mode, at JARVIS_CONTRACT_CALLBACK_HOST (then a LAN address of the test host).
			tg.Post(t, CommandCenter, "/api/v0/oauth/sessions", sessionBody("http://127.0.0.1:9/token", ""), u.H()).
				ExpectError(http.StatusBadRequest, "Invalid exchange URL: an external provider's exchange_url must be https")
			if host == "" {
				t.Skipf("%s is not set: jarvisd's exchange paths need a LAN address of the test host", EnvOCRCallbackHost)
			}
			session = func(exchangeURL string) map[string]any {
				eu, err := url.Parse(exchangeURL)
				if err != nil {
					t.Fatal(err)
				}
				if eu.Hostname() == "127.0.0.1" {
					eu.Host = net.JoinHostPort(host, eu.Port())
				}
				return sessionAt(eu.Path, eu.Scheme+"://"+eu.Host)
			}
		}
		statusShape := func(sid, status string) Obj {
			return Obj{"session_id": Eq(sid), "status": Eq(status), "provider": Eq(provider)}
		}

		// A dead token endpoint: 502, the session stays pending, nothing is published.
		s := session("http://127.0.0.1:9/token")
		sid := s["session_id"].(string)
		au, err := url.Parse(s["authorize_url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		q := au.Query()
		if q.Get("response_type") != "code" || q.Get("client_id") != "contract-client" || q.Get("redirect_uri") != "jarvis://contract-callback" ||
			q.Get("state") == "" || q.Get("scope") != "read" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
			t.Fatalf("authorize_url query: %v", q)
		}
		tg.Get(t, CommandCenter, "/api/v0/oauth/sessions/"+sid, u.H()).Expect(http.StatusOK, statusShape(sid, "pending"))
		tg.Post(t, CommandCenter, "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "contract-code"}, u.H()).
			Expect(http.StatusBadGateway, Obj{"detail": Regexp(`^Token exchange failed: `)})
		tg.Get(t, CommandCenter, "/api/v0/oauth/sessions/"+sid, u.H()).Expect(http.StatusOK, statusShape(sid, "pending"))
		tg.Post(t, CommandCenter, "/api/v0/oauth/sessions/00000000-0000-4000-8000-000000000000/exchange", map[string]any{"code": "x"}, u.H()).
			ExpectError(http.StatusNotFound, "Auth session not found")
		tg.Get(t, CommandCenter, "/api/v0/oauth/provider/"+provider+"/credentials", n.APIKeyH()).
			ExpectError(http.StatusNotFound, "No active auth session found for this provider/node")

		// Row 22 needs a token endpoint the target can reach (it POSTs the code there before
		// publishing). Same convention as the OCR callback test.
		if host == "" {
			t.Skipf("%s is not set: the auth-ready publish needs a token endpoint the target can reach", EnvOCRCallbackHost)
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		form := make(chan url.Values, 1)
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			select {
			case form <- r.PostForm:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "contract-at", "refresh_token": "contract-rt", "expires_in": 3600})
		})}
		go srv.Serve(ln)
		defer srv.Close()

		s = session(fmt.Sprintf("http://%s/token", ln.Addr().String()))
		// base_url is the session's provider_base_url (jarvisd: the LAN mode, above).
		var credsBase Matcher = Null
		if Jarvisd() {
			credsBase = Eq("http://" + ln.Addr().String())
		}
		sid = s["session_id"].(string)
		tg.Post(t, CommandCenter, "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "contract-code"}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("ok"), "session_id": Eq(sid)})
		select {
		case f := <-form:
			if f.Get("grant_type") != "authorization_code" || f.Get("code") != "contract-code" || f.Get("client_id") != "contract-client" ||
				f.Get("redirect_uri") != "jarvis://contract-callback" || f.Get("code_verifier") == "" {
				t.Fatalf("token exchange form: %v", f)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the token endpoint was never called")
		}
		// Row 22: to every node, with the node to act in the payload.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, "jarvis/auth/"+provider+"/ready"), Obj{
			"provider": Eq(provider), "node_id": Eq(nid), "session_id": Eq(sid), "user_id": Eq(u.ID),
		})
		tg.Get(t, CommandCenter, "/api/v0/oauth/sessions/"+sid, u.H()).Expect(http.StatusOK, statusShape(sid, "active"))
		// The node's pull is one-time.
		tg.Get(t, CommandCenter, "/api/v0/oauth/provider/"+provider+"/credentials", n.APIKeyH()).Expect(http.StatusOK, Obj{
			"access_token": Eq("contract-at"), "refresh_token": Eq("contract-rt"),
			"token_data": Obj{"access_token": Eq("contract-at"), "refresh_token": Eq("contract-rt"), "expires_in": Eq(3600)},
			"base_url":   credsBase, "user_id": Eq(u.ID),
		})
		tg.Get(t, CommandCenter, "/api/v0/oauth/provider/"+provider+"/credentials", n.APIKeyH()).
			ExpectError(http.StatusNotFound, "No active auth session found for this provider/node")
		tg.Get(t, CommandCenter, "/api/v0/oauth/sessions/"+sid, u.H()).Expect(http.StatusOK, statusShape(sid, "consumed"))
		tg.Post(t, CommandCenter, "/api/v0/oauth/sessions/"+sid+"/exchange", map[string]any{"code": "again"}, u.H()).
			ExpectError(http.StatusBadRequest, "Session already consumed")
	})

	t.Run("no_stray_publishes", func(t *testing.T) {
		if left := c.Drain(500 * time.Millisecond); len(left) > 0 {
			var topics []string
			for _, m := range left {
				topics = append(topics, fmt.Sprintf("%s %s", m.Topic, m.Payload))
			}
			t.Fatalf("unexpected messages: %v", topics)
		}
	})
}

// rowsToolReport is what a node POSTs for report_tools: its OpenAI-format client tools, its
// command list and its installed packages.
func rowsToolReport() map[string]any {
	return map[string]any{
		"client_tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "contract_cmd", "description": "contract", "parameters": map[string]any{"type": "object", "properties": map[string]any{}},
		}}},
		"available_commands": []any{map[string]any{"command_name": "contract_cmd", "description": "contract"}},
		"installed_packages": []any{map[string]any{"name": "contract_pkg", "version": "0.0.1"}},
	}
}
