package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
)

// K2 arrives before registration, keyed by the CC-assigned id; the node's later run (which
// opens the store for its enrolled id) must still find it.
func TestProvisionOptionsKeepK2ForTheRegisteredNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jarvis-agent", "node.json")
	o := provisionOptions(path, &provisioning.Simulated{}, "", nil)
	if o.Provisioned() {
		t.Fatal("provisioned before any credentials")
	}
	k2 := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if err := o.SaveK2("cc-node-1", k2, "k2-2026-01", "2026-10-17T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := o.SaveK2("cc-node-1", "short", "k2-2026-01", "2026-10-17T09:00:00Z"); err == nil {
		t.Error("a bad K2 was accepted")
	}
	if err := o.SaveCreds(node.Creds{ConfigURL: "http://192.0.2.10:7700", NodeID: "cc-node-1", NodeKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if !o.Provisioned() || o.Identity().NodeID != "cc-node-1" {
		t.Errorf("after creds: provisioned %v, id %q", o.Provisioned(), o.Identity().NodeID)
	}
	st, err := nodesettings.OpenStore(filepath.Dir(path), "cc-node-1")
	if err != nil || !st.HasK2() {
		t.Errorf("K2 lost: %v", err)
	}
}

func TestProvisionWiFiBackend(t *testing.T) {
	if w, err := provisionWiFi("simulated", ""); err != nil || w == nil {
		t.Errorf("simulated: %v", err)
	}
	for _, name := range []string{"", "nm"} {
		if _, err := provisionWiFi(name, ""); err == nil || !strings.Contains(err.Error(), "--wifi networkmanager") {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// radio is a WiFi that also caches a scan, as the NetworkManager backend does.
type radio struct {
	provisioning.Simulated
	calls []string
}

func (r *radio) ScanAndCache(context.Context) ([]provisioning.NetworkInfo, error) {
	r.calls = append(r.calls, "scan")
	return nil, nil
}

func (r *radio) StartAPMode(_ context.Context, ssid string) error {
	r.calls = append(r.calls, "ap "+ssid)
	return nil
}

// The radio can't scan once it is the hotspot: the scan comes first (N:run_provisioning.py).
func TestStartHotspotScansFirst(t *testing.T) {
	r := &radio{}
	startHotspot(t.Context(), r, "jarvis-1234abcd", slog.New(slog.DiscardHandler))
	if got := strings.Join(r.calls, ","); got != "scan,ap jarvis-1234abcd" {
		t.Errorf("calls %q", got)
	}
}

func TestDefaultAPSSID(t *testing.T) {
	if got := defaultAPSSID("jarvis-1234abcd"); got != "jarvis-1234abcd" {
		t.Errorf("%q", got)
	}
	if got := defaultAPSSID("0f6c2a4e-77aa-4b1e-9d3c-5e8f00c1d2e3"); got != "jarvis-00c1d2e3" {
		t.Errorf("%q", got)
	}
}

// The recovery cycle runs only for a node provisioned before, with a real radio, unless
// JARVIS_RECOVERY_DISABLED.
func TestProvisionRecovery(t *testing.T) {
	sm := provisioning.NewStateMachine()
	yes, no := func() bool { return true }, func() bool { return false }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, ok := provisionRecovery(&radio{}, "jarvis-x", no, sm, env(nil), nil); ok {
		t.Error("recovery for a fresh node")
	}
	if _, ok := provisionRecovery(&provisioning.Simulated{}, "jarvis-x", yes, sm, env(nil), nil); ok {
		t.Error("recovery with no radio")
	}
	if _, ok := provisionRecovery(&radio{}, "jarvis-x", yes, sm, env(map[string]string{"JARVIS_RECOVERY_DISABLED": "1"}), nil); ok {
		t.Error("recovery though disabled")
	}
	r, ok := provisionRecovery(&radio{}, "jarvis-x", yes, sm, env(nil), nil)
	if !ok || r.APSSID != "jarvis-x" || r.Reboot == nil || r.Pairing == nil || r.Pairing() {
		t.Errorf("%v %+v", ok, r)
	}
	sm.Transition(provisioning.StateConnecting, "", 10)
	if !r.Pairing() {
		t.Error("an attempt in flight isn't pairing")
	}
}
