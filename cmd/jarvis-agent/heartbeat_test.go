package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"

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
	// Website access is reported only for a connected browser that says (TestHubWebsiteAccess).
	if _, ok := caps()["browser_website_access"]; ok {
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

// A connected extension that reports website access off shows in the heartbeat as
// browser_website_access "off" (the admin's "connected, website access off"), and its turning it
// on wakes a heartbeat that says "on".
func TestHeartbeatReportsWebsiteAccess(t *testing.T) {
	d, err := os.MkdirTemp("", "jhb") // t.TempDir is too long for a socket path on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	path := filepath.Join(d, "b.sock")
	ln, err := browser.Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := browser.NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = hub.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	caps, changed := heartbeatCaps(node.Multi{browser.NewTools(hub)}, nil)

	c, err := browser.Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	send := func(m map[string]any) {
		b, _ := json.Marshal(m)
		if _, err := c.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(ch <-chan struct{}) {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatalf("no heartbeat wake; caps %v", caps())
		}
	}
	woke := changed()
	send(map[string]any{"type": "host", "origin": "chrome-extension://" + browser.ChromeDevExtensionID + "/"})
	send(map[string]any{"type": "hello", "protocol": browser.Protocol, "browser": "Firefox", "website_access": false})
	wait(woke)
	if c := caps(); c["browser_extension"] != extConnected || c["browser_website_access"] != "off" {
		t.Fatalf("connected, access off: %v", c)
	}
	woke = changed()
	send(map[string]any{"type": "event", "event": "access", "website_access": true})
	wait(woke)
	if c := caps(); c["browser_website_access"] != "on" {
		t.Fatalf("access turned on: %v", c)
	}
}
