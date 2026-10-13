package main

import (
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The heartbeat's browser_extension capability: off with run --no-browser, else the hub's
// state, with the hub's change channel waking a heartbeat.
func TestHeartbeatCaps(t *testing.T) {
	caps, changed := heartbeatCaps(node.Multi{})
	if caps()["browser_extension"] != extDisabled || changed != nil {
		t.Fatalf("no browser tools: %v", caps())
	}
	hub := browser.NewHub(nil)
	caps, changed = heartbeatCaps(node.Multi{browser.NewTools(hub)})
	if caps()["browser_extension"] != extDisconnected || changed == nil || changed() == nil {
		t.Fatalf("hub, nothing connected: %v", caps())
	}
}
