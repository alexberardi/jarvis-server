package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// startHub starts a sidecar over testdata/packages/hub (a manager with get_state/control) and
// testdata/packages/lights (a family, and a manager without them).
func startHub(t *testing.T) *Sidecar {
	t.Helper()
	py := python(t)
	pkg := func(name string) string {
		root, err := filepath.Abs(filepath.Join("testdata", "packages", name))
		if err != nil {
			t.Fatal(err)
		}
		return root
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{
		map[string]any{"name": "hub", "root": pkg("hub"), "managers": []string{"device_managers/fakehub/manager.py"}},
		map[string]any{"name": "lights", "root": pkg("lights"),
			"families": []string{"device_families/fakelight/protocol.py"}, "managers": []string{"device_managers/fakemgr/manager.py"}},
	}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc
}

func deviceState(t *testing.T, sc *Sidecar, details map[string]any) map[string]any {
	t.Helper()
	res, err := sc.DeviceCall("device_state", map[string]any{"details": details})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// A source no configured manager has is not found.
func TestControlForAnUnknownManager(t *testing.T) {
	sc := startHub(t)
	res, _ := sc.Action(map[string]any{"command_name": "control_device", "action_name": "turn_on",
		"context": map[string]any{"entity_id": "switch.x", "source": "nowhere"}})
	if res["error"] != "Device manager 'nowhere' not found or not configured" {
		t.Fatalf("unknown manager = %v", res)
	}
	if got := deviceState(t, sc, map[string]any{"entity_id": "switch.x", "source": "nowhere"}); got["error"] != "Failed to query device state" {
		t.Fatalf("unknown manager state = %v", got)
	}
}

// The heartbeat's managers: configured package managers only, not the built-in jarvis_direct.
func TestManagersForTheHeartbeat(t *testing.T) {
	sc := startHub(t)
	if got := sc.Managers(); !reflect.DeepEqual(got, []string{"fakehub", "fakemgr"}) {
		t.Fatalf("managers = %v", got)
	}
}

var tv = map[string]any{"entity_id": "media_player.tv", "domain": "media_player", "name": "TV", "source": "fakehub"}

// A manager's device: its state as the manager reports it, the catalogue's media hints with
// the device's own features, and no "available_features" left in the state.
func TestManagerDeviceStateUsesTheCatalogue(t *testing.T) {
	sc := startHub(t)
	got := deviceState(t, sc, tv)
	want := map[string]any{"entity_id": "media_player.tv", "domain": "media_player",
		"state": map[string]any{"state": "playing", "position": 10.0, "duration": 300.0},
		"ui_hints": map[string]any{"control_type": "media", "features": []any{"play", "pause", "stop"},
			"min_value": nil, "max_value": nil, "step": nil, "unit": nil}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tv = %v", got)
	}
}

// The manager's error and a manager answering None both come out "unknown" with an error.
func TestManagerDeviceStateFailuresAreUnknown(t *testing.T) {
	sc := startHub(t)
	broken := deviceState(t, sc, map[string]any{"entity_id": "media_player.broken", "source": "fakehub"})
	if !reflect.DeepEqual(broken["state"], map[string]any{"state": "unknown"}) || broken["error"] != "hub offline" {
		t.Fatalf("broken = %v", broken)
	}
	missing := deviceState(t, sc, map[string]any{"entity_id": "media_player.missing", "source": "fakehub"})
	if missing["error"] != "Failed to query device state" {
		t.Fatalf("missing = %v", missing)
	}
}

// Control goes to the manager, and the next state read follows it; an action the device
// doesn't support is the manager's refusal.
func TestManagerControlsItsDeviceAndStateFollows(t *testing.T) {
	sc := startHub(t)
	res, err := sc.Action(map[string]any{"command_name": "control_device", "action_name": "pause", "context": tv})
	if err != nil || res["success"] != true {
		t.Fatalf("pause = %v, %v", res, err)
	}
	if s := deviceState(t, sc, tv)["state"].(map[string]any)["state"]; s != "paused" {
		t.Fatalf("state after pause = %v", s)
	}
	res, _ = sc.Action(map[string]any{"command_name": "control_device", "action_name": "seek", "context": tv})
	if res["success"] != false || res["error"] != "TV doesn't support seek" {
		t.Fatalf("seek = %v", res)
	}
}

// A manager without get_state/control (written before them) gets the SDK's unsupported answers.
func TestManagerWithoutStateOrControlIsUnsupported(t *testing.T) {
	sc := startHub(t)
	plug := map[string]any{"entity_id": "switch.hub_plug", "domain": "switch", "source": "fakemgr"}
	if got := deviceState(t, sc, plug); got["error"] != "Failed to query device state" {
		t.Fatalf("plug state = %v", got)
	}
	res, _ := sc.Action(map[string]any{"command_name": "control_device", "action_name": "turn_on", "context": plug})
	if res["success"] != false || res["error"] != "Fake Hub can't control devices" {
		t.Fatalf("plug control = %v", res)
	}
}
