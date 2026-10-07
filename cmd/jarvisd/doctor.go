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

// exposure is what the LAN must reach: the listeners the given modules serve, the MQTT
// broker's TCP and WebSocket ports, and mDNS when advertising. The CLI doctor and the admin's
// /api/doctor share it.
func exposure(listeners []string) doctor.Exposure {
	return doctor.Exposure{
		Listeners:  listeners,
		MQTTAddr:   envOr("JARVIS_MQTT_ADDR", mqtt.DefaultTCPAddr),
		MQTTWSAddr: envOr("JARVIS_MQTT_WS_ADDR", mqtt.DefaultWSAddr),
		MDNS:       os.Getenv("JARVIS_MDNS") != "0",
	}
}

// doctorPorts lists the ports `jarvisd doctor` checks.
func doctorPorts(cfg config.Config) []doctor.Port {
	var listeners []string
	for _, m := range modules() {
		listeners = append(listeners, m.Listener())
	}
	return exposure(listeners).Ports(cfg.Ports)
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
	if doctor.Worst(checks) == doctor.Fail {
		return errors.New("doctor found problems")
	}
	return nil
}
