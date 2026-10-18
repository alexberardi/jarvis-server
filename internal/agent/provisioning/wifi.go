package provisioning

import (
	"context"
	"fmt"
	"sync"
)

// WiFi is what the provisioning flow needs from the radio (WiFiManager in N:wifi_manager.py).
// The NetworkManager backend implements it on a Pi; Simulated stands in for development.
type WiFi interface {
	// ScanNetworks lists visible networks. While the hotspot is up it may answer from a scan
	// taken before the hotspot started.
	ScanNetworks(ctx context.Context) ([]NetworkInfo, error)
	// Connect joins ssid. The hotspot may be gone once it is called.
	Connect(ctx context.Context, ssid, password string) error
	// CommitConnection makes the last Connect's profile the permanent one (after registration).
	CommitConnection(ctx context.Context) error
	// RollbackConnection removes the profile the last Connect created (one that existed
	// before is kept).
	RollbackConnection(ctx context.Context) error
	// StartAPMode broadcasts the setup hotspot ssid again.
	StartAPMode(ctx context.Context, ssid string) error
}

// Simulated is SimulatedWiFi: four made-up networks; Connect succeeds for those only.
type Simulated struct {
	mu        sync.Mutex
	connected string
}

var simulatedNetworks = []NetworkInfo{
	{SSID: "HomeNetwork", SignalStrength: -45, Security: "WPA2"},
	{SSID: "Neighbor_5G", SignalStrength: -72, Security: "WPA2"},
	{SSID: "CoffeeShop_Free", SignalStrength: -80, Security: "OPEN"},
	{SSID: "IoT_Network", SignalStrength: -55, Security: "WPA3"},
}

func (s *Simulated) ScanNetworks(context.Context) ([]NetworkInfo, error) {
	return append([]NetworkInfo(nil), simulatedNetworks...), nil
}

func (s *Simulated) Connect(_ context.Context, ssid, _ string) error {
	for _, n := range simulatedNetworks {
		if n.SSID == ssid {
			s.mu.Lock()
			s.connected = ssid
			s.mu.Unlock()
			return nil
		}
	}
	return fmt.Errorf("simulated: no network %q", ssid)
}

func (s *Simulated) CommitConnection(context.Context) error { return nil }

func (s *Simulated) RollbackConnection(context.Context) error {
	s.mu.Lock()
	s.connected = ""
	s.mu.Unlock()
	return nil
}

func (s *Simulated) StartAPMode(context.Context, string) error {
	s.mu.Lock()
	s.connected = ""
	s.mu.Unlock()
	return nil
}
