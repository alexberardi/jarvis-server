package admin

import "testing"

// Run in the real member stack, alongside the other access-tier checks.
func testDeviceMemberRoutes(t *testing.T, base, h1, h2, root string, users map[string]string) {
	t.Helper()
	cc := base + "/api/cc/api/v0"
	hh := cc + "/households/" + h1
	for _, token := range []string{users["m1@example.com"], users["p1@example.com"], users["a1@example.com"], root} {
		for _, path := range []string{"/devices", "/rooms"} {
			if code, out := call(t, "GET", hh+path, nil, bearer(token)...); code != 200 {
				t.Fatalf("device read: %d %v", code, out)
			}
		}
		if code, out := call(t, "POST", hh+"/devices/import", map[string]any{"node_id": "node-h1", "devices": []any{map[string]any{"entity_id": "light.test", "name": "Desk lamp", "domain": "light", "source": "direct"}}}, bearer(token)...); code != 201 {
			t.Fatalf("device import: %d %v", code, out)
		}
		_, out := call(t, "GET", hh+"/devices", nil, bearer(token)...)
		id := obj(t, out.([]any)[0])["id"].(string)
		path := hh + "/devices/" + id
		if code, out := call(t, "PATCH", path, map[string]any{"name": "Desk lamp", "room_id": nil}, bearer(token)...); code != 200 {
			t.Fatalf("device edit: %d %v", code, out)
		}
		for _, op := range []struct {
			method, suffix string
			body           any
		}{
			{"GET", "/state", nil}, {"POST", "/control", map[string]any{"action": "pair_start"}},
		} {
			if code, out := call(t, op.method, path+op.suffix, op.body, bearer(token)...); code != 503 {
				t.Fatalf("offline device: %d %v", code, out)
			}
			if code, out := call(t, op.method, path+op.suffix, op.body, bearer(users["m2@example.com"])...); code != 403 {
				t.Fatalf("foreign device: %d %v", code, out)
			}
		}
		if code, out := call(t, "DELETE", path, nil, bearer(token)...); code != 204 {
			t.Fatalf("device delete: %d %v", code, out)
		}
		for _, kind := range []string{"device-scan", "device-list"} {
			path := cc + "/nodes/node-h1/" + kind
			code, out := call(t, "POST", path+"/request", nil, bearer(token)...)
			if code != 201 {
				t.Fatalf("device job: %d %v", code, out)
			}
			poll := path + "/" + obj(t, out)["id"].(string)
			if code, out := call(t, "GET", poll, nil, bearer(token)...); code != 200 {
				t.Fatalf("device poll: %d %v", code, out)
			}
			if code, out := call(t, "GET", poll, nil, bearer(users["m2@example.com"])...); code != 403 {
				t.Fatalf("foreign poll: %d %v", code, out)
			}
			if code, out := call(t, "POST", cc+"/nodes/node-h2/"+kind+"/request", nil, bearer(token)...); token != root && code != 403 {
				t.Fatalf("foreign request: %d %v", code, out)
			}
		}
	}
	if code, out := call(t, "GET", hh+"/devices", nil); code != 401 {
		t.Fatalf("anonymous devices: %d %v", code, out)
	}
	if code, out := call(t, "GET", cc+"/households/"+h2+"/devices", nil, append(bearer(users["m1@example.com"]), "X-API-Key", tierCCAdminKey)...); code != 403 {
		t.Fatalf("foreign devices with admin key: %d %v", code, out)
	}
}
