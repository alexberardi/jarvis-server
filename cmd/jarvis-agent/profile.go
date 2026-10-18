package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
)

// profile is what kind of node this agent is: a computer (the desktop agent) or a Raspberry Pi
// voice node (go-pi-node.md §7 phase 4). It decides how the node names itself to jarvisd and
// which tools and on-screen parts it has.
type profile struct {
	name string
	// installMode and nodeKind go in the heartbeat (version_info.install_mode, node_kind) and
	// the registration. jarvisd treats a node as a computer on node_kind "desktop" or
	// install_mode "desktop-agent" (cc/signout.go desktop()): sign-out instead of factory reset.
	installMode, nodeKind string
	// desktop: the desktop, screen, dictation and browser tools, the status icon and the island.
	desktop bool
}

var (
	desktopProfile = profile{name: "desktop", installMode: "desktop-agent", nodeKind: "desktop", desktop: true}
	// piProfile is §8 Q6: install_mode "tarball" (the Python node's), runtime "go" (node.Runtime,
	// always sent), and no node_kind: jarvisd's Pi default.
	piProfile = profile{name: "pi", installMode: "tarball"}
)

// piMarker is written by the Pi installer (install-pi.sh): its presence makes `auto` a Pi.
const piMarker = "/etc/jarvis-node"

// profileFlag is the --profile flag of run, enroll, status and listen.
func profileFlag(fs *flag.FlagSet) *string {
	return fs.String("profile", "auto", `"desktop", "pi" (a Raspberry Pi voice node: headless, no desktop tools), or "auto": `+
		"pi on a Pi with the ReSpeaker HAT or where "+piMarker+" exists, else desktop")
}

// resolveProfile reads --profile; detected is hat.Detected and exists reports whether a path
// exists (both injected so tests need no Pi).
func resolveProfile(flagValue string, detected func() bool, exists func(string) bool) (profile, error) {
	switch flagValue {
	case "desktop":
		return desktopProfile, nil
	case "pi":
		return piProfile, nil
	case "auto", "":
		if detected() || exists(piMarker) {
			return piProfile, nil
		}
		return desktopProfile, nil
	}
	return profile{}, fmt.Errorf("--profile must be auto, desktop or pi, not %q", flagValue)
}

// thisProfile is resolveProfile on this machine.
func thisProfile(flagValue string) (profile, error) {
	return resolveProfile(flagValue, hat.Detected, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}
