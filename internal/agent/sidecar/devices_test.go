package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// startLights starts a sidecar over testdata/packages/lights (two device families, one behind
// OAuth, and a device manager) with a settings store.
func startLights(t *testing.T) (*Sidecar, *nodesettings.Store) {
	t.Helper()
	py := python(t)
	root, err := filepath.Abs(filepath.Join("testdata", "packages", "lights"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{map[string]any{
		"name": "lights", "root": root,
		"families": []string{"device_families/fakelight/protocol.py", "device_families/cloudy/protocol.py"},
		"managers": []string{"device_managers/fakemgr/manager.py"},
	}}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Storage: st})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	return sc, st
}

func entityIDs(t *testing.T, res map[string]any) []string {
	t.Helper()
	var out []string
	for _, d := range res["devices"].([]any) {
		out = append(out, d.(map[string]any)["entity_id"].(string))
	}
	return out
}

func device(res map[string]any, entityID string) map[string]any {
	for _, d := range res["devices"].([]any) {
		if m := d.(map[string]any); m["entity_id"] == entityID {
			return m
		}
	}
	return nil
}

// The families and managers a package installs are described for the snapshot and the
// heartbeat; a family whose secrets aren't set is listed but not active.
func TestSidecarDescribesDeviceFamilies(t *testing.T) {
	sc, _ := startLights(t)
	if f := sc.Failed(); len(f) != 0 {
		t.Fatalf("failed = %v", f)
	}
	if p := sc.Protocols(); !reflect.DeepEqual(p, []string{"fakelight"}) {
		t.Fatalf("protocols = %v", p)
	}
	dev := sc.Devices()
	var fams, mgrs []string
	for _, f := range dev.Families {
		fams = append(fams, f.Entry["family_name"].(string))
	}
	for _, m := range dev.Managers {
		mgrs = append(mgrs, m.Entry["manager_name"].(string))
	}
	if strings.Join(fams, ",") != "fakelight,cloudy" || strings.Join(mgrs, ",") != "jarvis_direct,fakemgr" {
		t.Fatalf("families %v, managers %v", fams, mgrs)
	}
	cloudy := dev.Families[1]
	if cloudy.Entry["is_configured"] != false || cloudy.Entry["connection_type"] != "cloud" ||
		cloudy.Entry["friendly_name"] != "Cloudy Lights" || cloudy.Entry["is_custom"] != true ||
		cloudy.Entry["authentication"].(map[string]any)["provider"] != "cloudy" || cloudy.Entry["secrets"] != nil {
		t.Fatalf("cloudy = %v", cloudy.Entry)
	}
	if len(cloudy.Secrets) != 1 || cloudy.Secrets[0].Key != "CLOUDY_TOKEN" || cloudy.Secrets[0].Scope != "integration" {
		t.Fatalf("cloudy secrets = %+v", cloudy.Secrets)
	}
	acts, _ := dev.Families[0].Entry["supported_actions"].([]any)
	if len(acts) != 2 || acts[0].(map[string]any)["button_action"] != "turn_on" {
		t.Fatalf("fakelight actions = %v", dev.Families[0].Entry["supported_actions"])
	}
	if m := dev.Managers[0].Entry; m["can_edit_devices"] != true || m["is_available"] != true || m["friendly_name"] != "Jarvis Direct" {
		t.Fatalf("jarvis_direct = %v", m)
	}
}

// device_scan: every configured family's devices, deduplicated by MAC (case-insensitively),
// without their extra, with the family's supported_actions. Once OAuth stores the cloud
// family's token it joins the scan, and its lamp keeps its own entity_id.
func TestSidecarScansDevices(t *testing.T) {
	sc, st := startLights(t)
	res, err := sc.DeviceCall("device_scan", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := entityIDs(t, res); strings.Join(ids, ",") != "light.lamp,climate.hall,light.pair_me" {
		t.Fatalf("scan = %v", ids)
	}
	lamp := device(res, "light.lamp")
	if lamp["name"] != "Lamp" || lamp["local_ip"] != "10.0.0.5" || lamp["protocol"] != "fakelight" || lamp["is_controllable"] != true {
		t.Fatalf("lamp = %v", lamp)
	}
	if _, ok := lamp["extra"]; ok {
		t.Fatalf("extra left the node: %v", lamp)
	}
	if acts := lamp["supported_actions"].([]any); len(acts) != 2 {
		t.Fatalf("supported_actions = %v", acts)
	}

	who, err := sc.StoreAuth("cloudy", map[string]any{"access_token": "tok", "_base_url": "https://cloud"}, json.Number("7"))
	if err != nil || who != "family cloudy" {
		t.Fatalf("store auth = %q, %v", who, err)
	}
	if got := st.Secret("CLOUDY_TOKEN", "integration", nil); got != "tok@https://cloud" {
		t.Fatalf("token = %q", got)
	}
	if p := sc.Protocols(); !reflect.DeepEqual(p, []string{"cloudy", "fakelight"}) {
		t.Fatalf("protocols after auth = %v", p)
	}
	res, err = sc.DeviceCall("device_scan", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ids := entityIDs(t, res); strings.Join(ids, ",") != "light.lamp,climate.hall,light.pair_me,light.lamp_cloudy" {
		t.Fatalf("scan after auth = %v", ids)
	}
	if who, err := sc.StoreAuth("nobody", map[string]any{}, nil); err != nil || who != "" {
		t.Fatalf("unknown provider = %q, %v", who, err)
	}
}

// device_list: the built-in jarvis_direct manager (the families' devices as manager devices),
// a package's manager, "all", and a manager that isn't there.
func TestSidecarListsDevices(t *testing.T) {
	sc, _ := startLights(t)
	res, err := sc.DeviceCall("device_list", map[string]any{"manager_name": "jarvis_direct"})
	if err != nil {
		t.Fatal(err)
	}
	if res["manager_name"] != "jarvis_direct" || res["can_edit_devices"] != true ||
		strings.Join(entityIDs(t, res), ",") != "light.lamp,climate.hall,light.pair_me" {
		t.Fatalf("jarvis_direct = %v", res)
	}
	if lamp := device(res, "light.lamp"); lamp["source"] != "direct" || lamp["mac_address"] != "aa:bb:cc:00:00:01" || lamp["area"] != nil {
		t.Fatalf("lamp = %v", lamp)
	}
	res, _ = sc.DeviceCall("device_list", map[string]any{"manager_name": "fakemgr"})
	plug := device(res, "switch.hub_plug")
	if res["can_edit_devices"] != false || plug == nil || plug["area"] != "Kitchen" || plug["extra"] != nil {
		t.Fatalf("fakemgr = %v", res)
	}
	res, _ = sc.DeviceCall("device_list", map[string]any{"manager_name": "all"})
	if res["manager_name"] != "all" || res["can_edit_devices"] != false || len(entityIDs(t, res)) != 4 {
		t.Fatalf("all = %v", res)
	}
	res, _ = sc.DeviceCall("device_list", map[string]any{"manager_name": "nope"})
	if res["error"] != "Device manager 'nope' not found or not configured" {
		t.Fatalf("nope = %v", res)
	}
}

// device_state: the family's state normalized for the device's domain, with UI hints (as the
// Python node's domain handlers do, Python's rounding included); pairing; failures.
func TestSidecarReadsDeviceState(t *testing.T) {
	sc, _ := startLights(t)
	state := func(details map[string]any) map[string]any {
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
	lamp := state(map[string]any{"entity_id": "light.lamp", "protocol": "fakelight", "source": "direct", "local_ip": "10.0.0.5"})
	want := map[string]any{"entity_id": "light.lamp", "domain": "light",
		"state": map[string]any{"state": "on", "brightness": 100.0, "rgb": []any{255.0, 128.0, 0.0}},
		"ui_hints": map[string]any{"control_type": "light", "features": []any{"brightness", "color", "color_temp"},
			"min_value": 0.0, "max_value": 100.0, "step": 1.0, "unit": "%"}}
	if !reflect.DeepEqual(lamp, want) {
		t.Fatalf("lamp = %v", lamp)
	}
	hall := state(map[string]any{"entity_id": "climate.hall", "domain": "climate", "protocol": "fakelight", "cloud_id": "t-1"})
	want = map[string]any{"entity_id": "climate.hall", "domain": "climate",
		"state": map[string]any{"unit": "F", "online": true, "state": "off", "mode": "heat",
			"current_temperature": 20.0, "target_temperature": 22.0},
		"ui_hints": map[string]any{"control_type": "thermostat", "features": []any{"heat", "off"},
			"min_value": 50.0, "max_value": 90.0, "step": 1.0, "unit": "F"}}
	if !reflect.DeepEqual(hall, want) {
		t.Fatalf("hall = %v", hall)
	}
	pair := state(map[string]any{"entity_id": "light.pair_me", "protocol": "fakelight"})
	if !reflect.DeepEqual(pair["state"], map[string]any{"needs_pairing": true}) ||
		!reflect.DeepEqual(pair["ui_hints"], map[string]any{"control_type": "pairing"}) {
		t.Fatalf("pair = %v", pair)
	}
	odd := state(map[string]any{"entity_id": "light.odd", "domain": "fan", "protocol": "fakelight"})
	if !reflect.DeepEqual(odd["state"], map[string]any{"state": "on", "level": 3.0}) || odd["ui_hints"].(map[string]any)["control_type"] != "toggle" {
		t.Fatalf("no domain handler = %v", odd)
	}
	for _, d := range []map[string]any{
		{"entity_id": "light.gone", "protocol": "fakelight"},                  // the family raises
		{"entity_id": "light.lamp", "protocol": "nope"},                       // no such family
		{"entity_id": "light.lamp", "protocol": "cloudy"},                     // not configured
		{"entity_id": "light.ha", "protocol": "", "source": "home_assistant"}, // HA isn't on this node
	} {
		if got := state(d); got["error"] != "Failed to query device state" || got["entity_id"] != d["entity_id"] || got["domain"] != "light" {
			t.Fatalf("%v = %v", d, got)
		}
	}
	if got := state(map[string]any{}); got["error"] != "missing entity_id" {
		t.Fatalf("no entity = %v", got)
	}
}

// device_removed runs the family's on_removed with the device jarvisd describes.
func TestSidecarRemovesADevice(t *testing.T) {
	sc, st := startLights(t)
	res, err := sc.DeviceCall("device_removed", map[string]any{"details": map[string]any{
		"entity_id": "light.lamp", "protocol": "fakelight", "local_ip": "10.0.0.5"}})
	if err != nil || res["ran"] != true {
		t.Fatalf("removed = %v, %v", res, err)
	}
	rec, ok := st.Data("fakelight", "removed", time.Now())
	if !ok || rec.Value("entity_id") != "light.lamp" || rec.Value("ip") != "10.0.0.5" {
		t.Fatalf("on_removed saw %v", rec)
	}
	if res, _ := sc.DeviceCall("device_removed", map[string]any{"details": map[string]any{"protocol": "nope"}}); res["ran"] != false {
		t.Fatalf("unknown protocol = %v", res)
	}
}
