package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
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

// runDoctor is `jarvisd doctor [--json] [--fix]`: it prints each check (or JSON) and fails
// when any check fails. --fix first applies the firewall checks' fix_cmds (ID5: private LAN
// subnets only, rules tagged jarvisd), which needs root or an elevated prompt, then checks
// again. envErr is the env file it couldn't read, reported as a check.
func runDoctor(ctx context.Context, envErr error, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	fix := fs.Bool("fix", false, "apply the firewall fix (needs sudo / an Administrator prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fix && !doctor.Elevated() {
		if runtime.GOOS == "windows" {
			return errors.New("applying the firewall fix needs an elevated prompt: run `jarvisd doctor --fix` as Administrator")
		}
		return errors.New("applying the firewall fix needs root: run `sudo jarvisd doctor --fix`")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	check := func() []doctor.Check {
		checks := doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg), Interfaces: cfg.MDNSInterfaces, Home: cfg.Home})
		if envErr != nil {
			checks = append([]doctor.Check{envCheck(envErr)}, checks...)
		}
		return checks
	}
	checks := check()
	if *fix {
		// With --json, stdout is the JSON alone; progress goes to stderr.
		progress := stdout
		if *asJSON {
			progress = stderr
		}
		n, err := doctor.Apply(ctx, checks, nil, progress)
		if err != nil {
			return fmt.Errorf("firewall fix: %w", err)
		}
		if n == 0 {
			fmt.Fprintln(progress, "doctor: no firewall change needed")
		} else {
			fmt.Fprintf(progress, "doctor: applied %d firewall command(s); checking again\n", n)
			checks = check()
		}
	}
	if *asJSON {
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

// envCheck reports an env file doctor couldn't read: typically a plain user and the system
// service's root:jarvisd 0640 /etc/jarvisd/jarvisd.env. The checks then use the default
// ports, which are wrong only if that file changes them.
func envCheck(err error) doctor.Check {
	c := doctor.Check{Name: "env file", Status: doctor.Warn,
		Detail: fmt.Sprintf("%v; checked the default ports, which is wrong if the file changes them", err)}
	if errors.Is(err, os.ErrPermission) {
		c.Detail = fmt.Sprintf("%v (it belongs to the jarvisd service); checked the default ports instead", err)
		c.Fix = "sudo jarvisd doctor"
	}
	return c
}
