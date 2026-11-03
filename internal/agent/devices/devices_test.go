package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeAPI is command-center: what the node posted, by path, and canned GET answers.
type fakeAPI struct {
	mu    sync.Mutex
	posts map[string]any
	gets  map[string]string // path -> JSON answer; missing: an error
	calls []string
}

func (f *fakeAPI) Do(_ context.Context, method, path string, body, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method+" "+path)
	if method == http.MethodPost {
		if f.posts == nil {
			f.posts = map[string]any{}
		}
		b, _ := json.Marshal(body)
		var v any
		_ = json.Unmarshal(b, &v)
		f.posts[path] = v
		return nil
	}
	ans, ok := f.gets[path]
	if !ok {
		return fmt.Errorf("404 %s", path)
	}
	return json.Unmarshal([]byte(ans), out)
}

func (f *fakeAPI) posted(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, _ := f.posts[path].(map[string]any)
	return m
}

// fakeFamilies is the sidecar's device side.
type fakeFamilies struct {
	mu      sync.Mutex
	calls   []string
	params  map[string]map[string]any
	results map[string]map[string]any
	err     error
	auth    []any
	stored  string
}

func (f *fakeFamilies) DeviceCall(method string, params map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	if f.params == nil {
		f.params = map[string]map[string]any{}
	}
	f.params[method] = params
	if f.err != nil {
		return nil, f.err
	}
	return f.results[method], nil
}

func (f *fakeFamilies) StoreAuth(provider string, values map[string]any, uid any) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, provider, values, uid)
	return f.stored, nil
}

func newNode(fam *fakeFamilies) *Node {
	return &Node{NodeID: "n1", Families: fam, Registry: &Registry{}, Log: quiet}
}

// device-scan: the sidecar's scan is posted as the request's results; a failed scan posts the
// error with no devices (N:'s _upload_error).
func TestScanPostsTheResults(t *testing.T) {
	devs := map[string]any{"devices": []any{map[string]any{"entity_id": "light.lamp"}}}
	fam := &fakeFamilies{results: map[string]map[string]any{"device_scan": devs}}
	api := &fakeAPI{}
	n := newNode(fam)
	n.Handle(context.Background(), api, TopicScan, []byte(`{"request_id":"r1"}`))
	if got := api.posted("/api/v0/nodes/n1/device-scan/r1/results"); !reflect.DeepEqual(got, devs) {
		t.Fatalf("posted %v (calls %v)", got, api.calls)
	}
	// The same request again (a QoS-1 redelivery) runs once.
	n.Handle(context.Background(), api, TopicScan, []byte(`{"request_id":"r1"}`))
	if len(fam.calls) != 1 {
		t.Fatalf("scans = %v", fam.calls)
	}

	fam.err = errors.New("sidecar exited")
	n.Handle(context.Background(), api, TopicScan, []byte(`{"request_id":"r2"}`))
	want := map[string]any{"devices": []any{}, "error": "sidecar exited"}
	if got := api.posted("/api/v0/nodes/n1/device-scan/r2/results"); !reflect.DeepEqual(got, want) {
		t.Fatalf("error posted %v", got)
	}
	// No request id: nothing runs.
	n.Handle(context.Background(), api, TopicScan, []byte(`{}`))
	n.Handle(context.Background(), api, TopicScan, []byte(`not json`))
	if len(fam.calls) != 2 {
		t.Fatalf("scans = %v", fam.calls)
	}
}

// device-list: the manager named (jarvis_direct by default); the sidecar's error is posted
// with no devices.
func TestListPostsTheManagersDevices(t *testing.T) {
	res := map[string]any{"devices": []any{}, "manager_name": "jarvis_direct", "can_edit_devices": true}
	fam := &fakeFamilies{results: map[string]map[string]any{"device_list": res}}
	api := &fakeAPI{}
	n := newNode(fam)
	n.Handle(context.Background(), api, TopicList, []byte(`{"request_id":"r1"}`))
	if fam.params["device_list"]["manager_name"] != "jarvis_direct" {
		t.Fatalf("params = %v", fam.params)
	}
	if got := api.posted("/api/v0/nodes/n1/device-list/r1/results"); !reflect.DeepEqual(got, res) {
		t.Fatalf("posted %v", got)
	}
	fam.results["device_list"] = map[string]any{"error": "Device manager 'hub' not found or not configured"}
	n.Handle(context.Background(), api, TopicList, []byte(`{"request_id":"r2","manager_name":"hub"}`))
	want := map[string]any{"devices": []any{}, "error": "Device manager 'hub' not found or not configured"}
	if fam.params["device_list"]["manager_name"] != "hub" || !reflect.DeepEqual(api.posted("/api/v0/nodes/n1/device-list/r2/results"), want) {
		t.Fatalf("posted %v", api.posted("/api/v0/nodes/n1/device-list/r2/results"))
	}
}

// device-state: the whole request goes to the sidecar as the device's details; its answer is
// posted to the shared result sink.
func TestStatePostsTheDevicesState(t *testing.T) {
	res := map[string]any{"entity_id": "light.lamp", "domain": "light", "state": map[string]any{"state": "on"}}
	fam := &fakeFamilies{results: map[string]map[string]any{"device_state": res}}
	api := &fakeAPI{}
	n := newNode(fam)
	req := `{"request_id":"r1","entity_id":"light.lamp","domain":"light","protocol":"lifx","source":"direct","local_ip":"10.0.0.5"}`
	n.Handle(context.Background(), api, TopicState, []byte(req))
	det, _ := fam.params["device_state"]["details"].(map[string]any)
	if det["entity_id"] != "light.lamp" || det["protocol"] != "lifx" || det["local_ip"] != "10.0.0.5" {
		t.Fatalf("details = %v", fam.params["device_state"])
	}
	if got := api.posted("/api/v0/device-state-results/r1"); !reflect.DeepEqual(got, res) {
		t.Fatalf("posted %v", got)
	}
	fam.err = errors.New("boom")
	n.Handle(context.Background(), api, TopicState, []byte(`{"request_id":"r2","entity_id":"light.lamp"}`))
	if got := api.posted("/api/v0/device-state-results/r2"); !reflect.DeepEqual(got, map[string]any{"error": "boom"}) {
		t.Fatalf("error posted %v", got)
	}
}

// invalidate_device_cache empties the registry; device_removed runs the family's cleanup with
// the device's details and then empties it too.
func TestVerbsInvalidateAndRemove(t *testing.T) {
	fam := &fakeFamilies{results: map[string]map[string]any{"device_removed": {"ran": true}}}
	api := &fakeAPI{gets: map[string]string{"/api/v0/node/devices": `[{"entity_id":"light.lamp","domain":"light","name":"Lamp","source":"direct","protocol":"lifx"}]`}}
	n := newNode(fam)
	ctx := context.Background()
	if _, err := n.Registry.Refresh(ctx, api); err != nil || len(n.Registry.Cached()) != 1 {
		t.Fatalf("refresh: %v %v", n.Registry.Cached(), err)
	}
	verbs := n.Verbs()
	verbs[VerbInvalidate](ctx, api, map[string]any{})
	if len(n.Registry.Cached()) != 0 {
		t.Fatal("invalidate left the cache")
	}
	_, _ = n.Registry.Refresh(ctx, api)
	det := map[string]any{"entity_id": "light.lamp", "protocol": "lifx"}
	verbs[VerbRemoved](ctx, api, det)
	if !reflect.DeepEqual(fam.params["device_removed"], map[string]any{"details": det}) || len(n.Registry.Cached()) != 0 {
		t.Fatalf("removed: params %v, cache %v", fam.params, n.Registry.Cached())
	}
}

// jarvis/auth/{provider}/ready for this node: the credentials are pulled once, user_id goes
// to the sidecar as the user, base_url is also handed over as _base_url, and the heartbeat is
// sent again (the family's now configured). Another node's message is ignored.
func TestAuthReadyStoresTheCredentials(t *testing.T) {
	fam := &fakeFamilies{stored: "family nest"}
	api := &fakeAPI{gets: map[string]string{
		"/api/v0/oauth/provider/nest/credentials": `{"access_token":"a","refresh_token":"r","token_data":{"expires_in":3600},"base_url":"https://x","user_id":9007199254740993}`,
	}}
	beats := 0
	n := newNode(fam)
	n.Changed = func() { beats++ }
	ctx := context.Background()
	n.AuthReady(ctx, api, "jarvis/auth/nest/ready", []byte(`{"provider":"nest","node_id":"n2"}`))
	if len(api.calls) != 0 || len(fam.auth) != 0 {
		t.Fatalf("another node's: calls %v", api.calls)
	}
	n.AuthReady(ctx, api, "jarvis/auth/nest/ready", []byte(`{"provider":"nest","node_id":"n1","user_id":7}`))
	if len(fam.auth) != 3 || fam.auth[0] != "nest" {
		t.Fatalf("auth = %v", fam.auth)
	}
	vals := fam.auth[1].(map[string]any)
	if vals["access_token"] != "a" || vals["_base_url"] != "https://x" || vals["base_url"] != "https://x" || vals["user_id"] != nil {
		t.Fatalf("values = %v", vals)
	}
	// The user id stays an exact integer on its way to Python (a float64 would round 2^53+1).
	if uid, ok := fam.auth[2].(json.Number); !ok || uid != "9007199254740993" {
		t.Fatalf("user id = %#v", fam.auth[2])
	}
	if b, _ := json.Marshal(vals["token_data"]); string(b) != `{"expires_in":3600}` {
		t.Fatalf("token_data = %s", b)
	}
	if beats != 1 {
		t.Fatalf("beats = %d", beats)
	}
	// Nobody takes it: no beat. The provider comes from the topic when the body lacks it.
	fam.stored = ""
	api.gets["/api/v0/oauth/provider/other/credentials"] = `{"access_token":"b","base_url":null}`
	n.AuthReady(ctx, api, "jarvis/auth/other/ready", []byte(`{"node_id":"n1"}`))
	if beats != 1 || fam.auth[3] != "other" || fam.auth[5] != nil {
		t.Fatalf("beats %d, auth %v", beats, fam.auth)
	}
	// A provider that doesn't fit in a path is refused.
	n.AuthReady(ctx, api, "jarvis/auth/x/ready", []byte(`{"provider":"../admin","node_id":"n1"}`))
	if strings.Contains(strings.Join(api.calls, ","), "admin") {
		t.Fatalf("calls = %v", api.calls)
	}
}
