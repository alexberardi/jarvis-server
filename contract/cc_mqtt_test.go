//go:build contract

package contract

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// The MQTT topic catalogue (docs/cc/05 §2.4). A fake node subscribes to jarvis/nodes/{nid}/#
// at QoS 1 on the target's broker, each subtest triggers a CC route that publishes, and the
// exact topic, QoS (1), retain flag (false) and payload JSON shape are frozen. Where the node
// answers (HTTP pull, result POST, MQTT response topic), the fake node answers too, so the
// round trip is frozen as well.
//
// Rows covered: 1 commands (verbs callback, update_node_config, preview_led_pattern,
// measure_ambient_noise, action, routine), 2 settings/request, 3 k2/provision,
// 4 factory-reset, 6 routines/sync, 13 bluetooth-disconnect, 14 bluetooth-discoverable,
// 20 command-data/{op} with its response topic.

const mqttWait = 10 * time.Second

// httpResult carries a request made from a goroutine (the long-poll routes block until the
// fake node answers).
type httpResult struct {
	r   *Resp
	err error
}

func (tg *Target) goDo(listener, method, path string, body any, hs ...H) <-chan httpResult {
	ch := make(chan httpResult, 1)
	go func() {
		r, err := tg.do(listener, method, path, body, hs...)
		ch <- httpResult{r, err}
	}()
	return ch
}

func awaitHTTP(t *testing.T, ch <-chan httpResult) *Resp {
	t.Helper()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("background request: %v", res.err)
		}
		res.r.t = t
		return res.r
	case <-time.After(30 * time.Second):
		t.Fatalf("background request did not finish")
	}
	return nil
}

// commandMsg matches a jarvis/nodes/{nid}/commands message carrying verb.
func commandMsg(nid, verb string) func(MQTTMsg) bool {
	return func(m MQTTMsg) bool {
		if m.Topic != "jarvis/nodes/"+nid+"/commands" {
			return false
		}
		var arr []struct {
			Command string `json:"command"`
		}
		return json.Unmarshal(m.Payload, &arr) == nil && len(arr) == 1 && arr[0].Command == verb
	}
}

// commandShape is row 1's payload: a one-element JSON array [{command, details}] with
// request_id injected into details.
func commandShape(verb string, details Obj) Matcher {
	if _, ok := details["request_id"]; !ok {
		details["request_id"] = UUID
	}
	return All(ArrayOf(Obj{"command": Eq(verb), "details": details}), ccArrayLen(1))
}

func details(v any) map[string]any {
	return v.([]any)[0].(map[string]any)["details"].(map[string]any)
}

func TestCCMQTTCatalogue(t *testing.T) {
	tg := T(t)
	tg.NeedMQTT(t)
	n := SharedCCNode(t)
	u := SharedUser(t)
	nid := n.ID
	base := "jarvis/nodes/" + nid + "/"

	// The node's own client id and subscription (mqtt_tts_listener.py:2733-2753); clean
	// session here so nothing lingers on the broker after the run.
	c := DialMQTT(t, "jarvis-node-"+nid)
	c.Subscribe(t, base+"#")

	t.Run("settings_request", func(t *testing.T) {
		path := "/api/v0/nodes/" + nid + "/settings/requests"
		req := tg.Post(t, CommandCenter, path, nil, u.H()).Expect(http.StatusCreated, settingsRequestShape).Object()
		rid := req["request_id"].(string)
		if req["status"] != "pending" || req["node_id"] != nid {
			t.Fatalf("new request: %v", req)
		}
		// Row 2. include_values is omitted unless true; user_id is the requesting user.
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"settings/request"),
			Obj{"request_id": Eq(rid), "node_id": Eq(nid), "user_id": Eq(u.ID)})

		req2 := tg.Post(t, CommandCenter, path+"?include_values=true", nil, u.H()).Expect(http.StatusCreated, settingsRequestShape).Object()
		rid2 := req2["request_id"].(string)
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"settings/request"),
			Obj{"request_id": Eq(rid2), "node_id": Eq(nid), "include_values": Eq(true), "user_id": Eq(u.ID)})

		// The node's reconnect backstop lists both, oldest first.
		list := tg.Get(t, CommandCenter, path, n.APIKeyH()).Expect(http.StatusOK, ArrayOf(settingsRequestShape)).JSON().([]any)
		if len(list) < 2 || list[len(list)-2].(map[string]any)["request_id"] != rid || list[len(list)-1].(map[string]any)["request_id"] != rid2 {
			t.Fatalf("pending list: %v", list)
		}
		tg.Get(t, CommandCenter, path+"/"+rid, n.APIKeyH()).Expect(http.StatusOK, settingsRequestShape)
		tg.Get(t, CommandCenter, path+"/00000000-0000-4000-8000-000000000000", n.APIKeyH()).ExpectError(http.StatusNotFound, "Request not found")

		// Mobile's poll: 202 with a body while pending (doc 05 §7.13).
		tg.Get(t, CommandCenter, path+"/"+rid+"/result", u.H()).
			Expect(http.StatusAccepted, Obj{"status": Eq("pending"), "request_id": Eq(rid), "message": Eq("Waiting for node response")})

		snap := map[string]any{
			"ciphertext": "Y29udHJhY3Q", "nonce": "bm9uY2U", "tag": "dGFn",
			"aad_schema_version": 1, "aad_commands_schema_version": 2, "aad_revision": 3,
		}
		tg.Do(t, CommandCenter, http.MethodPut, path+"/"+rid+"/snapshot", snap, n.APIKeyH()).
			Expect(http.StatusCreated, Obj{"snapshot_id": UUID, "node_id": Eq(nid), "created_at": TimestampNaive})
		tg.Do(t, CommandCenter, http.MethodPut, path+"/"+rid+"/snapshot", snap, n.APIKeyH()).
			ExpectError(http.StatusConflict, "Request already fulfilled")

		tg.Get(t, CommandCenter, path+"/"+rid+"/result", u.H()).Expect(http.StatusOK, Obj{
			"status": Eq("fulfilled"), "request_id": Eq(rid),
			"snapshot": Obj{
				"snapshot_id": UUID, "ciphertext": Eq("Y29udHJhY3Q"), "nonce": Eq("bm9uY2U"), "tag": Eq("dGFn"),
				"aad":        Obj{"node_id": Eq(nid), "schema_version": Eq(1), "commands_schema_version": Eq(2), "revision": Eq(3), "request_id": Eq(rid)},
				"created_at": TimestampNaive,
			},
		})
		tg.Get(t, CommandCenter, path+"/00000000-0000-4000-8000-000000000000/result", u.H()).
			ExpectError(http.StatusNotFound, "Request not found")

		// LEGACY-BUG: the /result poll has no household check (doc 05 §8.4); any signed-in
		// user can poll another household's request. D4: Go checks membership.
		other := NewUser(t)
		tg.Get(t, CommandCenter, path+"/"+rid2+"/result", other.H()).ExpectStatus(http.StatusAccepted)
		// Creating one does check: power_user in the node's household.
		tg.Post(t, CommandCenter, path, nil, other.H()).ExpectStatus(http.StatusForbidden)
	})

	t.Run("k2_provision", func(t *testing.T) {
		k2 := map[string]any{"k2": "contract-k2-" + randHex(8), "kid": "contract-kid", "created_at": "2026-10-06T00:00:00Z"}
		pending := tg.goDo(CommandCenter, http.MethodPost, "/api/v0/nodes/"+nid+"/k2", k2, u.H())

		// Row 3: a nudge only, never key material (doc 05 §7.11).
		v := ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"k2/provision"), Obj{"request_id": UUID})
		rid := v.(map[string]any)["request_id"].(string)

		pull := "/api/v0/nodes/" + nid + "/k2/provision/" + rid
		tg.Get(t, CommandCenter, "/api/v0/nodes/contract-other/k2/provision/"+rid, n.APIKeyH()).
			ExpectError(http.StatusForbidden, "Node mismatch")
		tg.Get(t, CommandCenter, pull, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"k2": Eq(k2["k2"]), "kid": Eq("contract-kid"), "created_at": Eq("2026-10-06T00:00:00Z")})
		// One-time read.
		tg.Get(t, CommandCenter, pull, n.APIKeyH()).ExpectError(http.StatusNotFound, "No pending K2 for this request")
		tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/k2/ack/"+rid, map[string]any{"success": true}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{"ok": Eq(true), "node_id": Eq(nid), "kid": Eq("contract-kid")})

		// A failed ack is a 502 carrying the node's error verbatim.
		pending = tg.goDo(CommandCenter, http.MethodPost, "/api/v0/nodes/"+nid+"/k2", k2, u.H())
		v = ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"k2/provision"), Obj{"request_id": UUID})
		rid = v.(map[string]any)["request_id"].(string)
		tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/k2/ack/"+rid, map[string]any{"success": false, "error": "contract: refused"}, n.APIKeyH()).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).ExpectError(http.StatusBadGateway, "contract: refused")
	})

	t.Run("callback", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/callbacks", map[string]any{
			"command_name": "contract_cmd", "callback_name": "contract_cb", "data": map[string]any{"x": 1},
			"target_node_id": nid, "navigation_type": "stack",
		}, u.H()).Expect(http.StatusCreated, Obj{
			"id": UUID, "status": Eq("pending"), "navigation_type": Eq("stack"), "created_at": TimestampNaive,
		}).Object()
		jid := r["id"].(string)

		// Row 1, verb callback: details carry only the opaque id (here request_id == job id).
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "callback")), commandShape("callback", Obj{}))
		if details(v)["request_id"] != jid {
			t.Fatalf("callback request_id %v != job id %s", details(v)["request_id"], jid)
		}

		tg.Get(t, CommandCenter, "/api/v0/callbacks/"+jid, n.APIKeyH()).Expect(http.StatusOK, Obj{
			"job_id": Eq(jid), "command_name": Eq("contract_cmd"), "callback_name": Eq("contract_cb"),
			"data": Obj{"x": Eq(1)}, "user_id": Eq(u.ID),
			"voice_command": Eq("cb:contract_cb"), "conversation_id": Eq("callback:" + jid),
		})
		// Another node gets a 404, not a 403 (no existence leak).
		other := NewCCNode(t, u)
		tg.Get(t, CommandCenter, "/api/v0/callbacks/"+jid, other.APIKeyH()).ExpectError(http.StatusNotFound, "Callback job not found")

		tg.Get(t, CommandCenter, "/api/v0/callbacks/"+jid+"/status", u.H()).Expect(http.StatusOK, Obj{
			"id": Eq(jid), "status": Eq("pending"), "navigation_type": Eq("stack"),
			"completed_at": Null, "error_message": Null, "context_data": Null,
		})
		tg.Post(t, CommandCenter, "/api/v0/callbacks/"+jid+"/result", map[string]any{
			"success": true, "context_data": map[string]any{"shown": "contract"},
		}, n.APIKeyH()).Expect(http.StatusOK, Obj{"id": Eq(jid), "status": Eq("completed"), "completed_at": TimestampNaive})
		tg.Get(t, CommandCenter, "/api/v0/callbacks/"+jid+"/status", u.H()).Expect(http.StatusOK, Obj{
			"id": Eq(jid), "status": Eq("completed"), "navigation_type": Eq("stack"),
			"completed_at": TimestampNaive, "error_message": Null, "context_data": Obj{"shown": Eq("contract")},
		})

		tg.Post(t, CommandCenter, "/api/v0/callbacks", map[string]any{
			"command_name": "c", "callback_name": "cb", "target_node_id": "contract-nosuch-" + randHex(3),
		}, u.H()).ExpectError(http.StatusNotFound, "Target node not found")
	})

	t.Run("update_node_config", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/node-config", map[string]any{
			"settings": map[string]any{"contract_key": 1}, "restart": false,
		}, u.H()).Expect(http.StatusOK, Obj{"status": Eq("sent"), "request_id": UUID}).Object()
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "update_node_config")),
			commandShape("update_node_config", Obj{"settings": Obj{"contract_key": Eq(1)}, "restart": Eq(false)}))
		if details(v)["request_id"] != r["request_id"] {
			t.Fatalf("request_id mismatch: %v vs %v", details(v)["request_id"], r["request_id"])
		}
	})

	t.Run("preview_led_pattern", func(t *testing.T) {
		r := tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/led/preview", map[string]any{"pattern": "contract"}, u.H()).
			Expect(http.StatusOK, Obj{"status": Eq("sent"), "request_id": UUID}).Object()
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "preview_led_pattern")),
			commandShape("preview_led_pattern", Obj{"pattern": Eq("contract"), "duration_seconds": Eq(3.0)}))
		if details(v)["request_id"] != r["request_id"] {
			t.Fatalf("request_id mismatch")
		}
	})

	t.Run("ambient_noise_and_verify", func(t *testing.T) {
		path := "/api/v0/nodes/" + nid + "/ambient-noise-measurements"
		r := tg.Post(t, CommandCenter, path, map[string]any{"duration_seconds": 30}, u.H()).
			Expect(http.StatusOK, Obj{"request_id": UUID, "status": Eq("sent")}).Object()
		rid := r["request_id"].(string)
		// Duration is clamped to 1–10 s.
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "measure_ambient_noise")),
			commandShape("measure_ambient_noise", Obj{"duration_seconds": Eq(10.0), "request_id": Eq(rid)}))

		// The verify pattern (doc 05 §3.5, §7.3): a node mismatch is false and does NOT consume
		// the entry; the owner's first verify is true; a replay is false.
		other := NewCCNode(t, u)
		verify := "/api/v0/commands/" + rid + "/verify"
		tg.Post(t, CommandCenter, verify, map[string]any{}, other.APIKeyH()).Expect(http.StatusOK, Obj{"valid": Eq(false)})
		tg.Post(t, CommandCenter, verify, map[string]any{}, n.APIKeyH()).Expect(http.StatusOK, Obj{"valid": Eq(true)})
		tg.Post(t, CommandCenter, verify, map[string]any{}, n.APIKeyH()).Expect(http.StatusOK, Obj{"valid": Eq(false)})
		tg.Post(t, CommandCenter, "/api/v0/commands/"+randHex(8)+"/verify", nil, n.APIKeyH()).Expect(http.StatusOK, Obj{"valid": Eq(false)})
		tg.Post(t, CommandCenter, verify, map[string]any{}, H{"X-API-Key": nid + ":bad"}).
			ExpectError(http.StatusUnauthorized, "Invalid node credentials")

		poll := Obj{"request_id": Eq(rid), "status": Eq("pending"), "completed_at": Null, "result": Null}
		tg.Get(t, CommandCenter, path+"/"+rid, u.H()).Expect(http.StatusOK, poll)

		res := map[string]any{"success": true, "duration_seconds": 10.0, "chunks": 5, "p50_rms": 1.5, "p75_rms": 2.5,
			"p95_rms": 3.5, "max_rms": 4.5, "suggested_silence_threshold": 7}
		tg.Post(t, CommandCenter, "/api/v0/nodes/contract-other/ambient-noise-measurements/"+rid+"/result", res, n.APIKeyH()).
			ExpectError(http.StatusForbidden, "Node may only post results for itself")
		tg.Post(t, CommandCenter, path+"/"+rid+"/result", res, n.APIKeyH()).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		tg.Get(t, CommandCenter, path+"/"+rid, u.H()).Expect(http.StatusOK, Obj{
			"request_id": Eq(rid), "status": Eq("completed"), "completed_at": Regexp(`^\d{4}-\d{2}-\d{2}T[\d:.]+Z$`),
			"result": Obj{"success": Eq(true), "duration_seconds": Eq(10.0), "chunks": Eq(5), "p50_rms": Eq(1.5), "p75_rms": Eq(2.5),
				"p95_rms": Eq(3.5), "max_rms": Eq(4.5), "suggested_silence_threshold": Eq(7), "error": Null},
		})
		tg.Get(t, CommandCenter, "/api/v0/nodes/contract-other/ambient-noise-measurements/"+rid, u.H()).
			ExpectError(http.StatusNotFound, "Not found")

		// LEGACY-BUG: trigger and poll have no household check (doc 05 §8.4). A user from
		// another household can make this node measure. D4: Go checks membership.
		stranger := NewUser(t)
		r2 := tg.Post(t, CommandCenter, path, nil, stranger.H()).Expect(http.StatusOK, Obj{"request_id": UUID, "status": Eq("sent")}).Object()
		ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "measure_ambient_noise")),
			commandShape("measure_ambient_noise", Obj{"duration_seconds": Eq(3.0), "request_id": Eq(r2["request_id"])}))
	})

	t.Run("action", func(t *testing.T) {
		pending := tg.goDo(CommandCenter, http.MethodPost, "/api/v0/nodes/"+nid+"/actions", map[string]any{
			"command_name": "contract_cmd", "action_name": "send", "context": map[string]any{"k": "v"},
		}, u.H())
		// LEGACY-BUG: trusted:true rides in the payload and makes the node skip verify (D4/D7:
		// Go never sends it; per-node broker ACLs make commands authentic).
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "action")), commandShape("action", Obj{
			"command_name": Eq("contract_cmd"), "action_name": Eq("send"), "context": Obj{"k": Eq("v")},
			"trusted": Eq(true), "user_id": Eq(u.ID),
		}))
		rid := details(v)["request_id"].(string)
		// LEGACY-BUG: the result sink is unauthenticated (doc 05 §8.2). D4: node auth, and the
		// rid must belong to that node.
		tg.Post(t, CommandCenter, "/api/v0/device-control-results/"+rid, map[string]any{"success": true}).
			Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{
			"status": Eq("completed"), "request_id": Eq(rid), "success": Eq(true), "error": Null,
		})
	})

	t.Run("routine_sync_and_run_now", func(t *testing.T) {
		hh := u.HouseholdID
		r := tg.Post(t, CommandCenter, "/api/v0/households/"+hh+"/routines", map[string]any{
			"name": "Contract " + tg.RunID, "trigger_phrases": []string{"contract run"},
			"steps": []map[string]any{{"command": "contract_cmd", "args": []map[string]string{{"key": "a", "value": "1"}}}},
		}, u.H()).ExpectStatus(http.StatusCreated).Object()
		routineID, slug := r["id"].(string), r["slug"].(string)
		deleted := false
		t.Cleanup(func() {
			if !deleted {
				tg.do(CommandCenter, http.MethodDelete, "/api/v0/households/"+hh+"/routines/"+routineID, nil, u.H())
			}
		})
		// Row 6: every active node in the household gets the nudge.
		sync := Obj{"event": Eq("routines_changed"), "household_id": Eq(hh)}
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"routines/sync"), sync)

		pending := tg.goDo(CommandCenter, http.MethodPost, "/api/v0/households/"+hh+"/routines/"+routineID+"/run-now",
			map[string]any{"node_id": nid}, u.H())
		// Row 1, verb routine. Today details carry only the slug (the node pulls the
		// definition); D24 makes Go carry the full definition.
		v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(nid, "routine")), commandShape("routine", Obj{
			"routine_name": Eq(slug), "reply_request_id": UUID, "tool_call_id": UUID,
			"trusted":       Eq(true), // LEGACY-BUG: see the action subtest.
			"voice_command": Eq("routine: " + slug),
		}))
		d := details(v)
		if d["reply_request_id"] != d["request_id"] {
			t.Fatalf("routine reply_request_id should equal request_id: %v", d)
		}
		tg.Post(t, CommandCenter, "/api/v0/device-control-results/"+d["reply_request_id"].(string), map[string]any{
			"output": map[string]any{"success": true, "passed": 1, "failed": 0, "message": "contract done"},
		}).Expect(http.StatusOK, Obj{"status": Eq("ok")})
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{
			"success": Eq(true), "status": Eq("success"), "message": Eq("contract done"), "passed": Eq(1), "failed": Eq(0),
		})

		tg.Do(t, CommandCenter, http.MethodDelete, "/api/v0/households/"+hh+"/routines/"+routineID, nil, u.H()).
			ExpectStatus(http.StatusNoContent)
		deleted = true
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"routines/sync"), sync)
	})

	t.Run("bluetooth_fire_and_forget", func(t *testing.T) {
		// Provisioning auth: the CC admin key is accepted on these routes.
		tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/bluetooth/discoverable", nil, CCAdminH()).
			Expect(http.StatusAccepted, Obj{"status": Eq("accepted"), "timeout_seconds": Eq(120)})
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-discoverable"), Obj{"timeout": Eq(120)})

		tg.Post(t, CommandCenter, "/api/v0/nodes/"+nid+"/bluetooth/disconnect", map[string]any{"mac_address": "00:11:22:33:44:55"}, CCAdminH()).
			Expect(http.StatusAccepted, Obj{"status": Eq("accepted")})
		ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"bluetooth-disconnect"), Obj{"mac_address": Eq("00:11:22:33:44:55")})
	})

	t.Run("command_data_request_response", func(t *testing.T) {
		pending := tg.goDo(CommandCenter, http.MethodGet, "/api/v0/mobile/command-data/nodes/"+nid+"/commands", nil, u.H())
		// Row 20: CC subscribes to {request}/response/{correlation_id} before publishing.
		v := ExpectMQTT(t, c.NextTopic(t, mqttWait, base+"command-data/commands"), Obj{"correlation_id": UUID})
		cid := v.(map[string]any)["correlation_id"].(string)
		reply, _ := json.Marshal(map[string]any{"commands": []map[string]any{{"command_name": "contract_cmd", "contract": true}}})
		c.Publish(t, base+"command-data/commands/response/"+cid, reply)
		// The node's own response comes back to us too (we're subscribed to #).
		c.NextTopic(t, mqttWait, base+"command-data/commands/response/"+cid)
		awaitHTTP(t, pending).Expect(http.StatusOK, Obj{
			"commands": All(ArrayOf(Obj{"command_name": Eq("contract_cmd"), "contract": Eq(true)}), ccArrayLen(1)),
		})
	})

	t.Run("factory_reset", func(t *testing.T) {
		victim := NewCCNode(t, u)
		vc := DialMQTT(t, "jarvis-node-"+victim.ID)
		vc.Subscribe(t, "jarvis/nodes/"+victim.ID+"/#")

		tg.Do(t, CommandCenter, http.MethodDelete, "/api/v0/admin/nodes/"+victim.ID, nil, u.H()).
			Expect(http.StatusOK, Obj{"message": Eq("Deleted")})
		// Row 4: published before auth is revoked; no task_id on this (DELETE) path.
		v := ExpectMQTT(t, vc.NextTopic(t, mqttWait, "jarvis/nodes/"+victim.ID+"/factory-reset"),
			Obj{"request_id": Regexp(`^[0-9a-f]{32}$`), "node_id": Eq(victim.ID)})
		tok := v.(map[string]any)["request_id"].(string)

		// The node's key is gone, so verify-reset is unauthenticated and consumes the token.
		body := map[string]any{"node_id": victim.ID, "request_id": tok}
		tg.Post(t, CommandCenter, "/api/v0/nodes/verify-reset", body).Expect(http.StatusOK, Obj{"verified": Eq(true)})
		tg.Post(t, CommandCenter, "/api/v0/nodes/verify-reset", body).ExpectError(http.StatusNotFound, "Invalid or expired reset token")
		tg.Get(t, CommandCenter, "/api/v0/node/mqtt-credentials", victim.APIKeyH()).ExpectStatus(http.StatusUnauthorized)
		tg.Get(t, CommandCenter, "/api/v0/admin/nodes/"+victim.ID, u.H()).ExpectError(http.StatusNotFound, "Node not found")
	})

	// Anything left over is a publish this suite didn't expect.
	t.Run("no_stray_publishes", func(t *testing.T) {
		if left := c.Drain(500 * time.Millisecond); len(left) > 0 {
			var topics []string
			for _, m := range left {
				topics = append(topics, fmt.Sprintf("%s %s", m.Topic, m.Payload))
			}
			t.Fatalf("unexpected messages on %s#: %v", base, topics)
		}
	})
}
