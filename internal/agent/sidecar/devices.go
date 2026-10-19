package sidecar

import (
	"encoding/json"
	"sort"

	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// deviceComponents is the sidecar's "families"/"managers" description: N:'s snapshot entries
// with their secrets' specs inline.
type deviceComponents struct {
	Families []map[string]json.RawMessage `json:"families"`
	Managers []map[string]json.RawMessage `json:"managers"`
}

func components(raw []map[string]json.RawMessage) []nodesettings.DeviceComponent {
	out := make([]nodesettings.DeviceComponent, 0, len(raw))
	for _, r := range raw {
		c := nodesettings.DeviceComponent{Entry: map[string]any{}}
		_ = json.Unmarshal(r["secrets"], &c.Secrets)
		for k, v := range r {
			if k == "secrets" {
				continue
			}
			var x any
			if json.Unmarshal(v, &x) == nil {
				c.Entry[k] = x
			}
		}
		out = append(out, c)
	}
	return out
}

func (s *Sidecar) setDevices(d deviceComponents) {
	dev := nodesettings.Devices{Families: components(d.Families), Managers: components(d.Managers)}
	s.devMu.Lock()
	s.devices = dev
	s.devMu.Unlock()
}

// Devices are the installed packages' device families and managers (with the built-in
// jarvis_direct manager) as last described: at start, after StoreAuth and on RefreshDevices.
func (s *Sidecar) Devices() nodesettings.Devices {
	s.devMu.Lock()
	defer s.devMu.Unlock()
	return s.devices
}

// RefreshDevices describes the families again: whether each is configured follows the secrets
// its validate_secrets reads, which the app's secrets push and OAuth set while it runs.
func (s *Sidecar) RefreshDevices() error {
	var d deviceComponents
	if err := s.roundTrip("device_families", nil, &d); err != nil {
		return err
	}
	s.setDevices(d)
	return nil
}

// Protocols are the configured device families' names, sorted: the heartbeat's "protocols",
// which jarvisd uses to pick the node that reads a device's state.
func (s *Sidecar) Protocols() []string {
	out := []string{}
	for _, f := range s.Devices().Families {
		if name, _ := f.Entry["family_name"].(string); name != "" && f.Entry["is_configured"] == true {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// DeviceCall runs one of the sidecar's device methods (device_scan, device_list,
// device_state, device_removed) and returns its result as is: the body the Python node posts.
// A scan waits for every family's discover (up to 10 s each, together), and the sidecar
// answers one request at a time.
func (s *Sidecar) DeviceCall(method string, params map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := s.roundTrip(method, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StoreAuth hands the OAuth credentials jarvisd holds for provider to the command or device
// family that declares it (store_auth_values, as user uid; nil for none) and returns which
// one took them ("" for none). A family that takes them is described again: with its token
// set it is configured and joins the scans and the heartbeat's protocols.
func (s *Sidecar) StoreAuth(provider string, values map[string]any, uid any) (string, error) {
	var res struct {
		Stored *string `json:"stored"`
	}
	if err := s.roundTrip("store_auth", map[string]any{"provider": provider, "values": values, "user_id": uid}, &res); err != nil {
		return "", err
	}
	if res.Stored == nil {
		return "", nil
	}
	return *res.Stored, s.RefreshDevices()
}
