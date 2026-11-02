package display

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	displayconfig "github.com/alexberardi/jarvis-server/internal/display"
)

// fakeHousehold is jarvisd's household device API with one remote media destination (a TV whose
// state follows its controls), a second device with the same name in another domain, and a light.
type fakeHousehold struct {
	mu       sync.Mutex
	state    string
	features []string
	controls []map[string]any
	reject   string
}

func (f *fakeHousehold) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v0/node/devices":
		WriteJSON(w, []map[string]any{
			{"id": "tv-1", "name": "Lounge TV", "entity_id": "media_player.lounge", "domain": "media_player"},
			{"id": "sw-1", "name": "Lounge TV", "entity_id": "switch.lounge_tv_power", "domain": "switch"},
			{"id": "sp-1", "name": "Kitchen", "entity_id": "media_player.kitchen", "domain": "media_player"},
			{"id": "sp-2", "name": "kitchen", "entity_id": "media_player.kitchen_2", "domain": "media_player"},
			{"id": "li-1", "name": "Lamp", "entity_id": "light.lamp", "domain": "light"},
		})
	case r.URL.Path == "/api/v0/node/devices/tv-1/state":
		WriteJSON(w, map[string]any{"state": map[string]any{"state": f.state, "position": 12.5, "duration": 300.0},
			"ui_hints": map[string]any{"control_type": "media", "features": f.features}})
	case r.URL.Path == "/api/v0/node/devices/tv-1/control" && r.Method == "POST":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.controls = append(f.controls, body)
		if f.reject != "" {
			WriteJSON(w, map[string]any{"success": false, "error": f.reject})
			return
		}
		switch body["action"] {
		case "pause":
			f.state = "paused"
		case "play", "play_media":
			f.state = "playing"
		case "stop":
			f.state = "idle"
		}
		WriteJSON(w, map[string]any{"success": true})
	default:
		http.NotFound(w, r)
	}
}

func remoteEngine(t *testing.T) (*Engine, *fakeHousehold) {
	t.Helper()
	t.Setenv("DISPLAY", "")
	f := &fakeHousehold{state: "playing", features: []string{"pause", "play", "stop"}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	e := &Engine{ctx: context.Background(), cc: node.NewCC(srv.URL, node.Creds{NodeID: "n", NodeKey: "k"})}
	c, err := displayconfig.MergeConfiguration(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = true
	e.delivery = displayconfig.Delivery{Configuration: c}
	return e, f
}

func media(t *testing.T, e *Engine, r MediaRequest) map[string]any {
	t.Helper()
	out, err := e.ControlMedia(context.Background(), r)
	if err != nil {
		t.Fatalf("%+v: %v", r, err)
	}
	return out
}

// Status for a remote destination is that device's playback, not this display's (nothing
// plays locally here), and its controls follow the device's own features.
func TestRemoteStatusFollowsTheDestination(t *testing.T) {
	e, f := remoteEngine(t)
	got := media(t, e, MediaRequest{Action: "status", Destination: "tv-1"})
	if got["active"] != true || got["kind"] != "remote" || got["paused"] != false || got["controllable"] != true || got["position"] != 12.5 {
		t.Fatalf("status = %v", got)
	}
	media(t, e, MediaRequest{Action: "pause", Destination: "tv-1"})
	if got = media(t, e, MediaRequest{Action: "status", Destination: "tv-1"}); got["paused"] != true || got["active"] != true {
		t.Fatalf("status after pause = %v", got)
	}
	f.features = []string{"turn_on"}
	if got = media(t, e, MediaRequest{Action: "status", Destination: "tv-1"}); got["controllable"] != false {
		t.Fatalf("a destination without pause/play is not controllable: %v", got)
	}
}

// Each media action reaches the device as its generic control action and data.
func TestRemoteActionsBecomeDeviceActions(t *testing.T) {
	e, f := remoteEngine(t)
	for _, r := range []MediaRequest{
		{Action: "play", URL: "https://example.org/v.mp4", Destination: "tv-1"},
		{Action: "pause", Destination: "Lounge TV"}, // by name: the media device, not the switch
		{Action: "resume", Destination: "media_player.lounge"},
		{Action: "seek", Seconds: 42, Destination: "tv-1"},
		{Action: "stop", Destination: "tv-1"},
	} {
		media(t, e, r)
	}
	want := []map[string]any{
		{"action": "play_media", "data": map[string]any{"url": "https://example.org/v.mp4"}},
		{"action": "pause", "data": nil},
		{"action": "play", "data": nil},
		{"action": "seek", "data": map[string]any{"seconds": 42.0}},
		{"action": "stop", "data": nil},
	}
	if !reflect.DeepEqual(f.controls, want) {
		t.Fatalf("controls = %v", f.controls)
	}
}

func TestRemoteRefusals(t *testing.T) {
	e, f := remoteEngine(t)
	for _, c := range []struct {
		r    MediaRequest
		want string
	}{
		{MediaRequest{Action: "pause", Destination: "Lamp"}, "destination unavailable"},
		{MediaRequest{Action: "pause", Destination: "nowhere"}, "destination unavailable"},
		{MediaRequest{Action: "pause", Destination: "Kitchen"}, "ambiguous"},
		{MediaRequest{Action: "play", URL: "file:///etc/passwd", Destination: "tv-1"}, "direct HTTP(S) media link"},
		{MediaRequest{Action: "play", Query: "cats", Destination: "tv-1"}, "choose local playback"},
		{MediaRequest{Action: "seek", Seconds: -1, Destination: "tv-1"}, "invalid seek position"},
	} {
		if _, err := e.ControlMedia(context.Background(), c.r); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.r, err, c.want)
		}
	}
	if len(f.controls) != 0 {
		t.Fatalf("a refused request reached the device: %v", f.controls)
	}
	f.reject = "Lounge TV doesn't support seek"
	if _, err := e.ControlMedia(context.Background(), MediaRequest{Action: "seek", Seconds: 5, Destination: "tv-1"}); err == nil || err.Error() != f.reject {
		t.Fatalf("the device's refusal = %v", err)
	}
}
