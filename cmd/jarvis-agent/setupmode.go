package main

import (
	"context"
	"io"
	"log/slog"
	"net"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
)

// A Pi's setup mode at boot (go-pi-node.md §7 4.4, D6; #292): a Pi with no credentials (after a
// factory reset, or installed without enrolling) doesn't exit. It scans, starts the setup hotspot
// (jarvis-<id8>) and serves the provisioning API the app uses, as N:run_provisioning.py does,
// with the HAT's LEDs on the setup pattern. Once the app has provisioned it, `run` goes on as
// the node in the same process. A Pi stays in setup mode until then, however long that is.

// piSetupHooks is what setup mode touches, swappable for tests.
type piSetupHooks struct {
	wifi func() (provisioning.WiFi, error)
	// hat opens the HAT's LEDs (nil when there is none) and how to hand them back.
	hat             func(*hatFlags, *slog.Logger) (*hat.LEDs, func())
	listen, captive string
	ready           func(api net.Addr)
}

var piSetup = piSetupHooks{
	wifi: func() (provisioning.WiFi, error) { return provisioning.NewNetworkManager("wlan0") },
	hat: func(hf *hatFlags, log *slog.Logger) (*hat.LEDs, func()) {
		hd := openHat(hf, nil, log)
		if hd == nil {
			return nil, nil
		}
		return hd.leds, func() { hd.Close() }
	},
	// The app provisions on :8080; phones' connectivity checks come to :80 (the unit grants
	// CAP_NET_BIND_SERVICE for it).
	listen:  ":8080",
	captive: ":80",
}

// piSetupMode runs setup mode until the node is provisioned (nil) or ctx ends (ctx's error).
func piSetupMode(ctx context.Context, log *slog.Logger, path string, hf *hatFlags) error {
	log.Info("not enrolled: setup mode (the setup hotspot and the provisioning API)")
	if leds, closeHat := piSetup.hat(hf, log); leds != nil {
		leds.SetPattern(hat.Setup)
		// The node's own run opens the HAT again.
		defer closeHat()
	}
	w, err := piSetup.wifi()
	if err != nil {
		return err
	}
	if c, ok := w.(io.Closer); ok {
		defer c.Close()
	}
	_, simulated := w.(*provisioning.Simulated)
	ssid := ""
	if !simulated {
		ssid = defaultAPSSID(provisioning.SystemIdentity(enrolledID(path)).NodeID)
	}
	if err := serveSetup(ctx, setupServer{path: path, wifi: w, apSSID: ssid, listen: piSetup.listen,
		captive: piSetup.captive, hotspot: !simulated, log: log, ready: piSetup.ready}); err != nil {
		return err
	}
	return ctx.Err()
}
