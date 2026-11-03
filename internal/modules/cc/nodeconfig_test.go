package cc

import (
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"strings"
	"testing"
)

func TestPlainNodeConfig(t *testing.T) {
	e := newEnv(t)
	tok := e.auth.addUser(7, "hh1", authn.RoleMember)
	stranger := e.auth.addUser(9, "hh2", authn.RoleMember)
	n := e.createNode("n1", "hh1")
	other := e.createNode("n2", "hh1")
	c := e.dialNode(n)
	c.subscribe("jarvis/nodes/n1/#")
	path := "/api/v0/nodes/n1/node-config"
	for _, method := range []string{"GET", "PATCH"} {
		e.do(method, path, map[string]any{"settings": map[string]any{"wake_ack_audio_enabled": false}}, bearer(stranger)).want(403)
	}
	e.do("PATCH", path, map[string]any{"settings": map[string]any{"k2": "SECRET"}}, bearer(tok)).want(400)
	done := make(chan *resp, 1)
	go func() { done <- e.do("GET", path, nil, bearer(tok)) }()
	verb, det := command(t, c.next())
	if verb != "get_node_config" {
		t.Fatal(verb)
	}
	rid := det["request_id"].(string)
	body := map[string]any{"output": map[string]any{"success": true, "values": map[string]any{"wake_ack_audio_enabled": true, "k2": "SECRET", "api_key": "SECRET"}, "error": "SECRET"}}
	e.do("POST", "/api/v0/device-control-results/"+rid, body, other.h()).want(403)
	e.do("POST", "/api/v0/device-control-results/"+rid, body, n.h()).want(200)
	out := (<-done).want(200).json()
	values := out["values"].(map[string]any)
	if len(values) != 1 || values["wake_ack_audio_enabled"] != true {
		t.Fatal(out)
	}
	go func() {
		done <- e.do("PATCH", path, map[string]any{"settings": map[string]any{"wake_ack_audio_enabled": false}}, bearer(tok))
	}()
	verb, det = command(t, c.next())
	if verb != "update_node_config" || det["confirm"] != true {
		t.Fatal(verb, det)
	}
	rid = det["request_id"].(string)
	e.do("POST", "/api/v0/device-control-results/"+rid, map[string]any{"output": map[string]any{"success": false, "error": "SECRET"}}, n.h()).want(200)
	r := (<-done).want(502)
	if strings.Contains(string(r.body), "SECRET") {
		t.Fatal("node error escaped")
	}
}
