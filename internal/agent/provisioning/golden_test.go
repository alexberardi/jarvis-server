package provisioning

import (
	"encoding/json"
	"os"
	"testing"
)

// The goldens are the Python node's own models (tools/golden/export_provisioning.py runs
// N:provisioning/models.py): the mobile app parses these bodies, so the bytes must match.
type golden struct {
	States     []State           `json:"states"`
	ErrorCodes []ErrorCode       `json:"error_codes"`
	Cases      map[string]string `json:"cases"`
}

func loadGolden(t *testing.T) golden {
	t.Helper()
	b, err := os.ReadFile("testdata/models_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestModelsMatchPythonGoldens(t *testing.T) {
	g := loadGolden(t)
	errCode := ErrRegistrationFailed
	cases := map[string]any{
		"info": NodeInfo{NodeID: "jarvis-a1b2c3d4", FirmwareVersion: "1.0.0", Hardware: "pi-zero-w",
			MACAddress: "b8:27:eb:a1:b2:c3", Capabilities: []string{"voice", "speaker"}, State: StateAPMode},
		"info_previously_provisioned": NodeInfo{NodeID: "jarvis-a1b2c3d4", FirmwareVersion: "1.0.0",
			Hardware: "raspberry-pi", MACAddress: "b8:27:eb:a1:b2:c3", State: StateError, PreviouslyProvisioned: true},
		"scan": ScanNetworksResponse{Networks: []NetworkInfo{
			{SSID: "HomeNetwork", SignalStrength: -45, Security: "WPA2"},
			{SSID: "Café <&>", SignalStrength: -80, Security: "OPEN"},
		}},
		"scan_empty":         ScanNetworksResponse{},
		"provision_accepted": ProvisionResponse{Success: true, Message: msgAccepted},
		"provision_busy":     ProvisionResponse{Success: false, Message: msgBusy},
		"status_idle":        NewStateMachine().Status(),
		"status_connecting":  Status{State: StateConnecting, Message: "Connecting to HomeNetwork...", ProgressPercent: 35},
		"status_registration_failed": Status{State: StateError, Message: msgFailedRestored, ProgressPercent: 70,
			Error:     ptrTo("Command center rejected registration (HTTP 401): Invalid or expired provisioning token"),
			ErrorCode: &errCode, RegistrationStatus: ptrTo(401), Retryable: true, HotspotRestored: true},
		"status_internal": Status{State: StateError, Message: msgFailed, ProgressPercent: 10,
			Error: ptrTo("boom"), ErrorCode: ptrTo(ErrInternal)},
		"status_provisioned": Status{State: StateProvisioned, Message: msgProvisioned, ProgressPercent: 100},
		"k2_ok":              K2Response{Success: true, NodeID: ptrTo("0b7c4e1e-1111-2222-3333-444455556666"), Kid: ptrTo("k2-2026-01")},
		"k2_refused":         K2Response{Error: ptrTo(msgK2Provisioned)},
		"reset_ok":           FactoryResetResponse{Success: true, Cleared: []string{"provisioning_marker", "k2_key"}},
		"reset_failed":       FactoryResetResponse{Error: ptrTo("permission denied")},
	}
	if len(cases) != len(g.Cases) {
		t.Errorf("%d Go cases for %d goldens", len(cases), len(g.Cases))
	}
	for name, v := range cases {
		want, ok := g.Cases[name]
		if !ok {
			t.Errorf("%s: no golden", name)
			continue
		}
		got, err := marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, want)
		}
	}
}

func TestEnumsMatchPython(t *testing.T) {
	g := loadGolden(t)
	states := []State{StateAPMode, StateConnecting, StateRegistering, StateProvisioned, StateError}
	codes := []ErrorCode{ErrConfigWrite, ErrWiFiConnect, ErrRegistrationFailed, ErrCredentialsSave, ErrInternal}
	if b, _ := json.Marshal(states); string(b) != mustJSON(g.States) {
		t.Errorf("states %s, Python %s", b, mustJSON(g.States))
	}
	if b, _ := json.Marshal(codes); string(b) != mustJSON(g.ErrorCodes) {
		t.Errorf("error codes %s, Python %s", b, mustJSON(g.ErrorCodes))
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
