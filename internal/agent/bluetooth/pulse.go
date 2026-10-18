package bluetooth

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Module is one loaded PulseAudio module (pactl list modules short).
type Module struct {
	ID, Name, Args string
}

// Pulse is the node's PulseAudio (§8 Q2 keeps it: it carries the Bluetooth audio).
type Pulse interface {
	// List is the names of the sinks or sources (kind "sinks" or "sources").
	List(ctx context.Context, kind string) ([]string, error)
	Modules(ctx context.Context) ([]Module, error)
	LoadModule(ctx context.Context, name, args string) (id string, err error)
	UnloadModule(ctx context.Context, id string) error
}

// macUnderscored is mac as Pulse's bluez sink and source names have it.
func macUnderscored(mac string) string { return strings.ReplaceAll(strings.ToUpper(mac), ":", "_") }

// hasEndpoint reports whether Pulse has a sink or source for mac: bluez_sink/bluez_source
// (PulseAudio) or bluez_output/bluez_input (PipeWire's pulse), as N: checks both.
func hasEndpoint(ctx context.Context, p Pulse, mac string) bool {
	m := macUnderscored(mac)
	for _, kind := range []string{"sinks", "sources"} {
		names, err := p.List(ctx, kind)
		if err != nil {
			continue
		}
		for _, name := range names {
			for _, prefix := range []string{"bluez_sink.", "bluez_output.", "bluez_source.", "bluez_input."} {
				if strings.HasPrefix(name, prefix+m) {
					return true
				}
			}
		}
	}
	return false
}

// phoneSource is the source Pulse makes for a phone streaming to the node over A2DP.
func phoneSource(mac string) string { return "bluez_source." + macUnderscored(mac) + ".a2dp_source" }

func loopbackFor(ctx context.Context, p Pulse, source string) (string, bool) {
	mods, err := p.Modules(ctx)
	if err != nil {
		return "", false
	}
	for _, m := range mods {
		if m.Name == "module-loopback" && strings.Contains(" "+m.Args+" ", " source="+source+" ") {
			return m.ID, true
		}
	}
	return "", false
}

// routeAudio is N:'s _configure_audio. role "source" (the node plays to a speaker or
// headphones): nothing to do; the sink is there for the music packages, and TTS stays on the
// node's own sink. role "sink" (a phone plays through the node): bridge the phone's a2dp_source
// into the default sink with a module-loopback, once (it appears a little after the link).
func (n *Node) routeAudio(ctx context.Context, mac, role string) {
	if n.Pulse == nil || role != "sink" {
		return
	}
	src := phoneSource(mac)
	deadline := time.Now().Add(or(n.SourceFor, 5*time.Second))
	for {
		names, err := n.Pulse.List(ctx, "sources")
		if err != nil {
			n.log().Error("bluetooth: pactl list sources failed", "err", err)
			return
		}
		if contains(names, src) {
			break
		}
		if time.Now().After(deadline) || sleep(ctx, or(n.Poll, 300*time.Millisecond)) != nil {
			n.log().Warn("bluetooth: the phone's a2dp_source didn't appear; no loopback", "source", src)
			return
		}
	}
	if _, ok := loopbackFor(ctx, n.Pulse, src); ok {
		return
	}
	// 100 ms keeps lip-sync acceptable for video and rides out ALSA buffer hiccups (N:).
	id, err := n.Pulse.LoadModule(ctx, "module-loopback", "source="+src+" latency_msec=100")
	if err != nil {
		n.log().Error("bluetooth: loopback not loaded", "source", src, "err", err)
		return
	}
	n.log().Info("bluetooth: loopback loaded (phone to the node's speaker)", "source", src, "module", id)
}

// unrouteAudio unloads the phone's loopback, if there is one (N:'s _unload_loopback_for_mac).
func (n *Node) unrouteAudio(ctx context.Context, mac string) {
	if n.Pulse == nil {
		return
	}
	src := phoneSource(mac)
	id, ok := loopbackFor(ctx, n.Pulse, src)
	if !ok {
		return
	}
	if err := n.Pulse.UnloadModule(ctx, id); err != nil {
		n.log().Error("bluetooth: loopback not unloaded", "source", src, "err", err)
		return
	}
	n.log().Info("bluetooth: loopback unloaded", "source", src, "module", id)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Pactl is Pulse through the pactl command (the agent's unit sets XDG_RUNTIME_DIR, so it reaches
// the node user's PulseAudio).
type Pactl struct{}

func (Pactl) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "pactl", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pactl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// columns splits pactl's "short" listing into its tab-separated fields.
func columns(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

func (p Pactl) List(ctx context.Context, kind string) ([]string, error) {
	out, err := p.run(ctx, "list", kind, "short")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, r := range columns(out) {
		if len(r) >= 2 {
			names = append(names, r[1])
		}
	}
	return names, nil
}

func (p Pactl) Modules(ctx context.Context) ([]Module, error) {
	out, err := p.run(ctx, "list", "modules", "short")
	if err != nil {
		return nil, err
	}
	var mods []Module
	for _, r := range columns(out) {
		if len(r) >= 2 {
			m := Module{ID: r[0], Name: r[1]}
			if len(r) >= 3 {
				m.Args = r[2]
			}
			mods = append(mods, m)
		}
	}
	return mods, nil
}

func (p Pactl) LoadModule(ctx context.Context, name, args string) (string, error) {
	out, err := p.run(ctx, append([]string{"load-module", name}, strings.Fields(args)...)...)
	return strings.TrimSpace(out), err
}

func (p Pactl) UnloadModule(ctx context.Context, id string) error {
	_, err := p.run(ctx, "unload-module", id)
	return err
}
