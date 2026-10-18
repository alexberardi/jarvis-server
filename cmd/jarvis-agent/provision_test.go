package main

import (
	"encoding/base64"
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
	if w, err := provisionWiFi("simulated"); err != nil || w == nil {
		t.Errorf("simulated: %v", err)
	}
	for _, name := range []string{"", "nm"} {
		if _, err := provisionWiFi(name); err == nil || !strings.Contains(err.Error(), "--wifi simulated") {
			t.Errorf("%q: %v", name, err)
		}
	}
}
