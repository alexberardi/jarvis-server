package devices

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// RefreshEvery is how often the registry is fetched again (N:'s DeviceDiscoveryAgent).
const RefreshEvery = 5 * time.Minute

// Device is one of the household's direct devices, as jarvisd lists them to a node (N:'s
// DeviceRecord): what a control or a state read needs to reach it.
type Device struct {
	EntityID   string `json:"entity_id"`
	Name       string `json:"name"`
	Domain     string `json:"domain"`
	Protocol   string `json:"protocol"`
	LocalIP    string `json:"local_ip"`
	MacAddress string `json:"mac_address"`
	CloudID    string `json:"cloud_id"`
	Model      string `json:"model"`
	RoomName   string `json:"room_name"`
}

// Registry caches the household's direct devices (GET /api/v0/node/devices, source "direct"),
// as N:'s DirectDeviceService: fetched at start and every Every, dropped when jarvisd says the
// devices changed (invalidate_device_cache, device_removed) and fetched again on the next read.
// Device control (#177) resolves an entity_id here.
type Registry struct {
	// Every is the refresh interval; 0 is RefreshEvery.
	Every time.Duration

	mu      sync.Mutex
	devices map[string]Device
	loaded  bool
}

type deviceRow struct {
	Device
	Source   *string `json:"source"`
	Protocol *string `json:"protocol"`
}

// Refresh fetches the registry and returns how many direct devices it holds. On an error the
// cache is left as it was.
func (r *Registry) Refresh(ctx context.Context, api API) (int, error) {
	var rows []deviceRow
	if err := api.Do(ctx, http.MethodGet, "/api/v0/node/devices", nil, &rows); err != nil {
		return 0, err
	}
	devs := map[string]Device{}
	for _, row := range rows {
		if row.Source == nil || *row.Source != "direct" {
			continue
		}
		d := row.Device
		if row.Protocol != nil {
			d.Protocol = *row.Protocol
		}
		devs[d.EntityID] = d
	}
	r.mu.Lock()
	r.devices, r.loaded = devs, true
	r.mu.Unlock()
	return len(devs), nil
}

// Invalidate drops the cache; the next read fetches it again.
func (r *Registry) Invalidate() {
	r.mu.Lock()
	r.devices, r.loaded = nil, false
	r.mu.Unlock()
}

func (r *Registry) cached() []Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, d)
	}
	return out
}

func (r *Registry) isLoaded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loaded
}

// Devices are the cached devices, fetched first when the cache was dropped.
func (r *Registry) Devices(ctx context.Context, api API) []Device {
	if !r.isLoaded() {
		_, _ = r.Refresh(ctx, api)
	}
	return r.cached()
}

// Get is one device by entity_id. One not in the cache is looked for once more (a device just
// added, as N:'s control_device does).
func (r *Registry) Get(ctx context.Context, api API, entityID string) (Device, bool) {
	r.Devices(ctx, api)
	r.mu.Lock()
	d, ok := r.devices[entityID]
	r.mu.Unlock()
	if ok {
		return d, true
	}
	if _, err := r.Refresh(ctx, api); err != nil {
		return Device{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok = r.devices[entityID]
	return d, ok
}

// Run fetches the registry now and then every Every, until ctx ends.
func (r *Registry) Run(ctx context.Context, api API, log *slog.Logger) {
	every := r.Every
	if every <= 0 {
		every = RefreshEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if n, err := r.Refresh(ctx, api); err != nil {
			if ctx.Err() == nil {
				log.Warn("devices: registry not refreshed", "err", err)
			}
		} else {
			log.Debug("devices: registry refreshed", "devices", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
