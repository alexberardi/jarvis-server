package main

import (
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The heartbeat's browser_extension capability: off with run --no-browser, else the hub's
// state, with the hub's change channel waking a heartbeat.
func TestHeartbeatCaps(t *testing.T) {
	caps, changed := heartbeatCaps(node.Multi{}, nil)
	if caps()["browser_extension"] != extDisabled || changed != nil {
		t.Fatalf("no browser tools: %v", caps())
	}
	hub := browser.NewHub(nil)
	caps, changed = heartbeatCaps(node.Multi{browser.NewTools(hub)}, nil)
	if caps()["browser_extension"] != extDisconnected || changed == nil || changed() == nil {
		t.Fatalf("hub, nothing connected: %v", caps())
	}
}

// The heartbeat's computer_control capability is the switch's state: "off" by default, "on"
// once turned on. Without a switch (a Pi) it is left out.
func TestHeartbeatReportsComputerControl(t *testing.T) {
	caps, _ := heartbeatCaps(node.Multi{}, nil)
	if _, ok := caps()["computer_control"]; ok {
		t.Fatalf("no switch: %v", caps())
	}
	sw := computer.NewSwitch(false, nil)
	caps, _ = heartbeatCaps(node.Multi{}, sw)
	if caps()["computer_control"] != "off" || caps()["browser_extension"] != extDisabled {
		t.Fatalf("off: %v", caps())
	}
	_ = sw.Set(true)
	if caps()["computer_control"] != "on" {
		t.Fatalf("on: %v", caps())
	}
}
