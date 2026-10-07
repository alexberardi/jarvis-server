package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
)

// doctorPorts lists what the LAN must reach: every served listener, the MQTT broker's TCP and
// WebSocket ports, and mDNS when advertising.
func doctorPorts(cfg config.Config) []doctor.Port {
	var out []doctor.Port
	seen := map[int]bool{}
	add := func(name string, port int, proto string) {
		if port > 0 && !seen[port] {
			seen[port] = true
			out = append(out, doctor.Port{Name: name, Port: port, Proto: proto})
		}
	}
	for _, m := range modules() {
		add(m.Listener(), cfg.Ports[m.Listener()], "tcp")
	}
	add("mqtt", portOf(envOr("JARVIS_MQTT_ADDR", mqtt.DefaultTCPAddr)), "tcp")
	add("mqtt-ws", portOf(envOr("JARVIS_MQTT_WS_ADDR", mqtt.DefaultWSAddr)), "tcp")
	if os.Getenv("JARVIS_MDNS") != "0" {
		add("mdns", 5353, "udp")
	}
	return out
}

// runDoctor prints each check (or JSON with --json) and fails when any check fails.
func runDoctor(ctx context.Context, args []string, stdout io.Writer) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	checks := doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg)})
	if len(args) > 0 && args[0] == "--json" {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(checks); err != nil {
			return err
		}
	} else {
		for _, c := range checks {
			fmt.Fprintf(stdout, "%-5s %s: %s\n", strings.ToUpper(string(c.Status)), c.Name, c.Detail)
			if c.Fix != "" {
				fmt.Fprintf(stdout, "      fix:\n        %s\n", strings.ReplaceAll(c.Fix, "\n", "\n        "))
			}
		}
	}
	for _, c := range checks {
		if c.Status == doctor.Fail {
			return errors.New("doctor found problems")
		}
	}
	return nil
}
