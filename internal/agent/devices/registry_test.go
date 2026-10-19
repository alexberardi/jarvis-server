package devices

import (
	"context"
	"testing"
	"time"
)

const nodeDevices = `[
 {"entity_id":"light.lamp","name":"Lamp","domain":"light","source":"direct","protocol":"lifx","local_ip":"10.0.0.5","mac_address":"aa","cloud_id":"","model":"A19","room_name":"Den"},
 {"entity_id":"light.ha","name":"HA light","domain":"light","source":"home_assistant","protocol":null},
 {"entity_id":"climate.hall","name":"Hall","domain":"climate","source":"direct","protocol":"nest","cloud_id":"t-1"}
]`

// The registry is jarvisd's direct devices (GET /node/devices, source "direct" only), as the
// Python node's DirectDeviceService caches them.
func TestRegistryKeepsTheDirectDevices(t *testing.T) {
	api := &fakeAPI{gets: map[string]string{"/api/v0/node/devices": nodeDevices}}
	r := &Registry{}
	ctx := context.Background()
	n, err := r.Refresh(ctx, api)
	if err != nil || n != 2 {
		t.Fatalf("refresh = %d, %v", n, err)
	}
	d, ok := r.Get(ctx, api, "light.lamp")
	want := Device{EntityID: "light.lamp", Name: "Lamp", Domain: "light", Protocol: "lifx", LocalIP: "10.0.0.5",
		MacAddress: "aa", Model: "A19", RoomName: "Den"}
	if !ok || d != want {
		t.Fatalf("lamp = %+v", d)
	}
	if _, ok := r.Get(ctx, api, "light.ha"); ok {
		t.Fatal("a Home Assistant device is in the direct registry")
	}

	// After an invalidate the next read fetches again.
	calls := len(api.calls)
	r.Invalidate()
	if devs := r.Devices(ctx, api); len(devs) != 2 || len(api.calls) != calls+1 {
		t.Fatalf("devices = %v, calls %v", devs, api.calls)
	}
	// A device not known yet is looked for once more (one just added).
	api.gets["/api/v0/node/devices"] = `[{"entity_id":"switch.new","name":"New","domain":"switch","source":"direct","protocol":"kasa"}]`
	if d, ok := r.Get(ctx, api, "switch.new"); !ok || d.Protocol != "kasa" {
		t.Fatalf("new = %+v %v", d, ok)
	}
	// jarvisd unreachable: the cache stays as it was.
	delete(api.gets, "/api/v0/node/devices")
	if _, err := r.Refresh(ctx, api); err == nil || len(r.cached()) != 1 {
		t.Fatalf("failed refresh: %v, cache %v", err, r.cached())
	}
}

// Run refreshes at once and then on every tick, until ctx ends.
func TestRegistryRefreshesOnASchedule(t *testing.T) {
	api := &fakeAPI{gets: map[string]string{"/api/v0/node/devices": nodeDevices}}
	r := &Registry{Every: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx, api, quiet); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		api.mu.Lock()
		n := len(api.calls)
		api.mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("refreshes = %d", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
}
