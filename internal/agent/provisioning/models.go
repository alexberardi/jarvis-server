// Package provisioning is the Pi node's setup API: while the node broadcasts its setup hotspot,
// the mobile app talks to it on :8080 to read the node, list Wi-Fi networks, hand over the home
// Wi-Fi, a provisioning token and K2, and poll progress. It is a port of the Python node's
// N:provisioning/{api,models,state_machine,registration}.py; the bodies are golden-tested
// against models.py, since the app (jarvis-node-mobile src/api/provisioningApi.ts) parses them.
//
// Wi-Fi is behind the WiFi interface (the NetworkManager backend is a separate piece). The API
// has no authentication, as in the Python node: it must only be served on the setup hotspot.
package provisioning

import (
	"bytes"
	"encoding/json"
)

// State is the provisioning state machine's state (ProvisioningState).
type State string

const (
	StateAPMode      State = "AP_MODE"     // broadcasting the hotspot, waiting for the app
	StateConnecting  State = "CONNECTING"  // joining the home Wi-Fi
	StateRegistering State = "REGISTERING" // registering with the command center
	StateProvisioned State = "PROVISIONED"
	StateError       State = "ERROR"
)

// ErrorCode says why an attempt ended in ERROR (ProvisioningErrorCode).
type ErrorCode string

const (
	ErrConfigWrite        ErrorCode = "config_write_failed"
	ErrWiFiConnect        ErrorCode = "wifi_connect_failed"
	ErrRegistrationFailed ErrorCode = "registration_failed"
	ErrCredentialsSave    ErrorCode = "credentials_save_failed"
	ErrInternal           ErrorCode = "internal_error"
)

// NodeInfo is GET /api/v1/info.
type NodeInfo struct {
	NodeID                string   `json:"node_id"`
	FirmwareVersion       string   `json:"firmware_version"`
	Hardware              string   `json:"hardware"`
	MACAddress            string   `json:"mac_address"`
	Capabilities          []string `json:"capabilities"`
	State                 State    `json:"state"`
	PreviouslyProvisioned bool     `json:"previously_provisioned"`
	// SetupMode is "hotspot" (the app joins the node's SSID) or "lan" (no hotspot; the node is
	// on the user's network, found over mDNS). Not in the Python node, hence omitempty.
	SetupMode string `json:"setup_mode,omitempty"`
}

// NetworkInfo is one Wi-Fi scan result.
type NetworkInfo struct {
	SSID           string `json:"ssid"`
	SignalStrength int    `json:"signal_strength"` // dBm
	Security       string `json:"security"`        // WPA2, WPA3, OPEN, …
}

// ScanNetworksResponse is GET /api/v1/scan-networks.
type ScanNetworksResponse struct {
	Networks []NetworkInfo `json:"networks"`
}

// ProvisionRequest is POST /api/v1/provision. Every field but ConfigServiceURL is required.
type ProvisionRequest struct {
	WiFiSSID          string `json:"wifi_ssid"`
	WiFiPassword      string `json:"wifi_password"`
	Room              string `json:"room"`
	CommandCenterURL  string `json:"command_center_url"`
	ConfigServiceURL  string `json:"config_service_url"`
	HouseholdID       string `json:"household_id"`
	NodeID            string `json:"node_id"` // the CC-assigned id
	ProvisioningToken string `json:"provisioning_token"`
}

// ProvisionResponse is POST /api/v1/provision's answer: the attempt runs in the background.
type ProvisionResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// Status is GET /api/v1/status.
type Status struct {
	State              State      `json:"state"`
	Message            string     `json:"message"`
	ProgressPercent    int        `json:"progress_percent"`
	Error              *string    `json:"error"`
	ErrorCode          *ErrorCode `json:"error_code"`
	RegistrationStatus *int       `json:"registration_status"` // CC's HTTP status when it refused
	Retryable          bool       `json:"retryable"`           // the app may resend credentials now
	HotspotRestored    bool       `json:"hotspot_restored"`    // the setup hotspot is back
}

// K2Request is POST /api/v1/provision/k2.
type K2Request struct {
	NodeID    string `json:"node_id"`
	Kid       string `json:"kid"`
	K2        string `json:"k2"` // base64url, 32 bytes
	CreatedAt string `json:"created_at"`
}

// K2Response is POST /api/v1/provision/k2's answer.
type K2Response struct {
	Success bool    `json:"success"`
	NodeID  *string `json:"node_id"`
	Kid     *string `json:"kid"`
	Error   *string `json:"error"`
}

// FactoryResetResponse is POST /api/v1/factory-reset's answer.
type FactoryResetResponse struct {
	Success bool     `json:"success"`
	Cleared []string `json:"cleared"`
	Error   *string  `json:"error"`
}

// marshal encodes like FastAPI: compact, no HTML escaping, and [] (never null) for lists.
func marshal(v any) ([]byte, error) {
	switch x := v.(type) {
	case NodeInfo:
		if x.Capabilities == nil {
			x.Capabilities = []string{}
		}
		v = x
	case ScanNetworksResponse:
		if x.Networks == nil {
			x.Networks = []NetworkInfo{}
		}
		v = x
	case FactoryResetResponse:
		if x.Cleared == nil {
			x.Cleared = []string{}
		}
		v = x
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
