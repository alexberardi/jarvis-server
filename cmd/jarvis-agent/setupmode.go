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
// A Pi whose wlan0 is already on a network (installed and activated over Wi-Fi without
// enrolling; #423) keeps it: no hotspot, the API on its LAN address. The hotspot would drop the
// LAN, and with no credentials nothing brings the network back, not even a reboot.

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
	log.Info("not enrolled: setup mode (the provisioning API, and the setup hotspot when wlan0 is on no network)")
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
	hotspot := !simulated
	if !simulated {
		ssid = defaultAPSSID(provisioning.SystemIdentity(enrolledID(path)).NodeID)
		if home, err := w.CurrentSSID(ctx); err != nil {
			log.Warn("provisioning: could not tell whether wlan0 is on a network; starting the setup hotspot", "err", err)
		} else if home != "" {
			log.Info("not enrolled; on a Wi-Fi network: setup over the LAN, no hotspot", "ssid", home)
			hotspot = false
		}
	}
	if err := serveSetup(ctx, setupServer{path: path, wifi: w, apSSID: ssid, listen: piSetup.listen,
		captive: piSetup.captive, hotspot: hotspot, log: log, ready: piSetup.ready}); err != nil {
		return err
	}
	return ctx.Err()
}
