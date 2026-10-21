package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// exposure is what the LAN must reach: the listeners the given modules serve, the MQTT
// broker's TCP and WebSocket ports, and mDNS when advertising. The CLI doctor and the admin's
// /api/doctor share it.
func exposure(listeners []string, env config.Env) doctor.Exposure {
	return doctor.Exposure{
		Listeners:     listeners,
		MQTTAddr:      env.MQTTAddr,
		MQTTWSAddr:    env.MQTTWSAddr,
		MDNS:          env.MDNS,
		MQTTAnonymous: env.MQTTAllowAnonymous,
	}
}

// doctorPorts lists the ports `jarvisd doctor` checks: the mounted listeners' own ports and
// the MQTT WebSocket only when legacy ports are on.
func doctorPorts(cfg config.Config) []doctor.Port {
	var listeners []string
	for _, m := range modules(cfg.Env) {
		listeners = append(listeners, m.Listener())
	}
	legacy := readLegacyPorts(cfg)
	env := cfg.Env
	env.MQTTWSAddr = mqttWSAddr(cfg.Env, legacy)
	return exposure(boundListeners(listeners, legacy), env).Ports(cfg.Ports)
}

// boundListeners is the listeners that get a port of their own: all of them with legacy ports
// on, else the ones not mounted on the config port.
func boundListeners(listeners []string, legacy bool) []string {
	if legacy {
		return listeners
	}
	var out []string
	for _, l := range listeners {
		if config.Paths[l] == "" {
			out = append(out, l)
		}
	}
	return out
}

// mqttWSAddr is the MQTT WebSocket address jarvisd listens on: JARVIS_MQTT_WS_ADDR when set
// (even to "", which disables it), else 9883 moved by JARVIS_PORT_OFFSET with legacy ports on,
// and none without.
func mqttWSAddr(env config.Env, legacy bool) string {
	if _, set := os.LookupEnv("JARVIS_MQTT_WS_ADDR"); set || !legacy {
		return env.MQTTWSAddr
	}
	return config.ShiftAddr(config.DefaultMQTTWSAddr, config.PortOffset())
}

// readLegacyPorts is serve's legacy-ports decision, read without changing anything:
// JARVIS_LEGACY_PORTS when set, else the stored server.legacy_ports. No database, no settings
// table or no row (jarvisd hasn't decided yet) reads as off, as does a bad env value (serve
// refuses to start with one).
func readLegacyPorts(cfg config.Config) bool {
	if set, on, err := config.ParseLegacyPorts(cfg.Env.LegacyPorts); set || err != nil {
		return on
	}
	path := cfg.DBPath()
	if _, err := os.Stat(path); err != nil {
		return false
	}
	c, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return false
	}
	defer c.Close()
	var v sql.NullString
	if err := c.QueryRow(`SELECT value FROM config_settings WHERE key = ?
		AND household_id IS NULL AND node_id IS NULL AND user_id IS NULL ORDER BY id LIMIT 1`, configmod.LegacyPortsKey).Scan(&v); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(v.String)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
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
		checks := doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg), Interfaces: cfg.MDNSInterfaces, Home: cfg.Home,
			MQTTAnonymous: cfg.Env.MQTTAllowAnonymous})
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
