//go:build contract

package contract

import (
	"net/http"
	"testing"
)

// Ordinary preferences have no Python HTTP equivalent and never provision K2.
func TestCCPlainNodeConfig(t *testing.T) {
	tg := T(t)
	if !Jarvisd() {
		t.Skip("contract: ordinary node settings are jarvisd-only (no legacy equivalent); set " + EnvImpl + "=jarvisd")
	}
	tg.NeedMQTT(t)
	u := SharedUser(t)
	n := NewCCNode(t, u)
	outsider := NewUser(t)
	path := "/api/v0/nodes/" + n.ID + "/node-config"
	patch := map[string]any{"settings": map[string]any{"wake_ack_audio_enabled": false}}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodPost} {
		p := path
		if method == http.MethodPost {
			p += "/basic"
		}
		tg.Do(t, CommandCenter, method, p, patch, outsider.H()).ExpectError(403, "User is not a member of this household")
	}
	tg.Do(t, CommandCenter, http.MethodPatch, path, map[string]any{"settings": map[string]any{"k2": "sentinel"}}, u.H()).
		ExpectError(400, "unsupported setting")
	c := DialMQTTNode(t, n)
	c.Subscribe(t, "jarvis/nodes/"+n.ID+"/#")
	field := Obj{"key": String, "label": String, "help": Optional(String), "group": String,
		"type": String, "min": Num, "max": Num, "step": Optional(Num)}
	tg.Get(t, CommandCenter, path+"?schema=1", u.H()).Expect(200, Obj{
		"values": Obj{}, "fields": NonEmptyArrayOf(field),
	})
	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			verb, want := "get_node_config", Obj{}
			var body any
			if method == http.MethodPatch {
				verb, body = "update_node_config", patch
				want = Obj{"settings": Obj{"wake_ack_audio_enabled": Eq(false)}, "restart": Eq(false), "confirm": Eq(true)}
			}
			pending := tg.goDo(CommandCenter, method, path, body, u.H())
			v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(n.ID, verb)), commandShape(verb, want))
			rid := details(v)["request_id"].(string)
			tg.Post(t, CommandCenter, "/api/v0/commands/"+rid+"/verify", map[string]any{}, n.APIKeyH()).Expect(200, Obj{"valid": Eq(true)})
			tg.Post(t, CommandCenter, "/api/v0/device-control-results/"+rid, map[string]any{
				"output": map[string]any{"success": true, "values": map[string]any{
					"wake_ack_audio_enabled": false, "k2": "sentinel", "api_key": "sentinel",
				}, "read_only": []string{"wake_ack_audio_enabled", "k2"}},
			}, n.APIKeyH()).ExpectStatus(200)
			awaitHTTP(t, pending).Expect(200, Obj{
				"values": Obj{"wake_ack_audio_enabled": Eq(false)},
				"fields": All(ArrayOf(field), ccArrayLen(1)), "read_only": EqDeep([]any{"wake_ack_audio_enabled"}),
			})
		})
	}
	r := tg.Post(t, CommandCenter, path+"/basic", patch, u.H()).Expect(200, Obj{"status": Eq("sent"), "request_id": UUID}).Object()
	v := ExpectMQTT(t, c.Next(t, mqttWait, commandMsg(n.ID, "update_node_config")), commandShape("update_node_config",
		Obj{"settings": Obj{"wake_ack_audio_enabled": Eq(false)}, "restart": Eq(false)}))
	if details(v)["request_id"] != r["request_id"] {
		t.Fatal("compatibility response does not match the command")
	}
}
