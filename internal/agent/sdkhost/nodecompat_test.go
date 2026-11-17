package sdkhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The Python node's RestClient on the Go agent (cc.request): GET, POST and PUT to command-center's
// /api/v0/ as the node (its key added here, never handed to Python), a non-2xx as a status for
// RestClient's None. Nothing outside the API, no other host, no other method.
func TestCCRequestIsTheNodesRestClient(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-API-Key"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v0/missing":
			http.Error(w, `{"detail":"nope"}`, http.StatusNotFound)
		default:
			var body any
			_ = json.NewDecoder(r.Body).Decode(&body)
			_ = json.NewEncoder(w).Encode(map[string]any{"got": body})
		}
	}))
	defer srv.Close()
	h := &Host{}
	if _, err := h.Answer(context.Background(), "cc.request", json.RawMessage(`{"method":"GET","path":"/api/v0/x"}`)); err != errNoCC {
		t.Fatalf("before connect = %v", err)
	}
	h.SetCC(node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k"}))

	r := ask(t, h, "cc.request", map[string]any{"method": "get", "path": "/api/v0/proposals/suppressions?command=x&user_id=7"}).(map[string]any)
	if r["status"] != 200 {
		t.Fatalf("GET = %v", r)
	}
	r = ask(t, h, "cc.request", map[string]any{"method": "POST", "path": "/api/v0/memories/inject", "json": map[string]any{"a": 1}}).(map[string]any)
	if got, _ := json.Marshal(r["body"]); string(got) != `{"got":{"a":1}}` {
		t.Fatalf("POST = %v", r)
	}
	r = ask(t, h, "cc.request", map[string]any{"method": "PUT", "path": "/api/v0/missing"}).(map[string]any)
	if r["status"] != 404 || r["body"] != nil {
		t.Fatalf("404 = %v", r)
	}
	mu.Lock()
	want := []string{"GET /api/v0/proposals/suppressions?command=x&user_id=7 n1:k", "POST /api/v0/memories/inject n1:k", "PUT /api/v0/missing n1:k"}
	if len(seen) != 3 || seen[0] != want[0] || seen[1] != want[1] || seen[2] != want[2] {
		t.Fatalf("server saw %v", seen)
	}
	mu.Unlock()

	for _, bad := range []map[string]any{
		{"method": "DELETE", "path": "/api/v0/x"},
		{"method": "GET", "path": "/api/v1/x"},
		{"method": "GET", "path": "/api/v0/../admin/nodes"},
		{"method": "GET", "path": "http://elsewhere.example/api/v0/x"},
		{"method": "GET", "path": "//elsewhere.example/api/v0/x"},
		{"method": "GET", "path": "/auth/login"},
	} {
		b, _ := json.Marshal(bad)
		if _, err := h.Answer(context.Background(), "cc.request", b); err == nil {
			t.Errorf("%v was allowed", bad)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("a refused request reached the server: %v", seen)
	}
}

// node.info is jarvisd's GET /api/v0/node/info plus where command-center is, fetched once; an
// older server without it gives the enrolled id and room. devices.list is the registry's cache.
func TestNodeInfoAndDirectDevices(t *testing.T) {
	calls := 0
	info := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !info {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"node_id":"n1","household_id":"hh1","room":"office","home_context":{"location":"Here"}}`))
	}))
	defer srv.Close()
	h := &Host{}
	h.SetCC(node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k", Room: "den"}))
	for range 2 {
		got, _ := json.Marshal(ask(t, h, "node.info", nil))
		if string(got) != `{"command_center_url":"`+srv.URL+`","home_context":{"location":"Here"},"household_id":"hh1","node_id":"n1","room":"office"}` {
			t.Fatalf("node.info = %s", got)
		}
	}
	if calls != 1 {
		t.Fatalf("fetched %d times, want once", calls)
	}

	old := &Host{}
	info = false
	old.SetCC(node.NewCC(srv.URL, node.Creds{NodeID: "n1", NodeKey: "k", Room: "den"}))
	if got, _ := json.Marshal(ask(t, old, "node.info", nil)); string(got) != `{"command_center_url":"`+srv.URL+`","node_id":"n1","room":"den"}` {
		t.Fatalf("older server: node.info = %s", got)
	}

	if got, _ := json.Marshal(ask(t, h, "devices.list", nil)); string(got) != `[]` {
		t.Fatalf("no registry: %s", got)
	}
	h.DirectDevices = func() any { return []map[string]string{{"entity_id": "light.lamp"}} }
	if got, _ := json.Marshal(ask(t, h, "devices.list", nil)); string(got) != `[{"entity_id":"light.lamp"}]` {
		t.Fatalf("devices.list = %s", got)
	}
}
