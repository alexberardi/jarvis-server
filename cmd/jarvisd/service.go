package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

// runService is `jarvisd service <verb>` (I1): it registers jarvisd with the OS's service
// manager and drives it. The install scripts (I3, I4) hand off to it.
func runService(ctx context.Context, flagHome string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return errors.New("usage: jarvisd service install|uninstall|start|stop|restart|status")
	}
	verb := args[0]
	fs := flag.NewFlagSet("service "+verb, flag.ContinueOnError)
	fs.SetOutput(stdout)
	user := fs.Bool("user", false, "Linux: a systemd --user unit for your account (no root)")
	var bin, runAs *string
	var noStart, purge, yes, keepFirewall *bool
	var asJSON *bool
	var wait *time.Duration
	switch verb {
	case "install":
		bin = fs.String("bin", "", "the jarvisd binary the service runs (default: this one)")
		runAs = fs.String("run-as", "", "macOS: the account the LaunchDaemon runs as (default: $SUDO_USER)")
		noStart = fs.Bool("no-start", false, "register without starting")
	case "status":
		asJSON = fs.Bool("json", false, "print JSON")
		wait = fs.Duration("wait", 0, "wait up to this long for jarvisd to be running and healthy")
	case "uninstall":
		purge = fs.Bool("purge", false, "also delete the data directory, env file and service account")
		yes = fs.Bool("yes", false, "with --purge: don't ask for confirmation")
		keepFirewall = fs.Bool("keep-firewall", false, "leave the firewall rules doctor --fix added")
	case "start", "stop", "restart":
	default:
		return fmt.Errorf("unknown service command %q", verb)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if verb == "install" {
		m, err := service.New(*user, stdout)
		if err != nil {
			return err
		}
		opts := service.InstallOptions{Home: flagHome, User: *user, RunAs: *runAs, NoStart: *noStart, Binary: *bin}
		if opts.Binary == "" {
			if opts.Binary, err = os.Executable(); err != nil {
				return err
			}
		}
		if opts.Binary, err = filepath.Abs(opts.Binary); err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(opts.Binary); err == nil {
			opts.Binary = resolved
		}
		if opts.Home != "" {
			if opts.Home, err = filepath.Abs(opts.Home); err != nil {
				return err
			}
		}
		if err := m.Install(ctx, opts); err != nil {
			return err
		}
		if opts.NoStart {
			return nil
		}
		st, err := waitHealthy(ctx, m, 2*time.Minute)
		printStatus(stdout, st)
		return err
	}

	m, err := service.Open(*user, stdout)
	if err != nil {
		return err
	}
	switch verb {
	case "uninstall":
		return uninstall(ctx, m, uninstallOptions{Purge: *purge, Yes: *yes, KeepFirewall: *keepFirewall},
			os.Stdin, stdinIsTerminal(), stdout)
	case "start":
		return m.Start(ctx)
	case "stop":
		return m.Stop(ctx)
	case "restart":
		return m.Restart(ctx)
	}
	// status
	st, err := waitHealthy(ctx, m, *wait)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if eerr := enc.Encode(st); eerr != nil {
			return eerr
		}
	} else {
		printStatus(stdout, st)
	}
	return err
}

// serviceStatus is the manager's view plus jarvisd's own health endpoint.
type serviceStatus struct {
	service.Status
	HealthURL string `json:"health_url,omitempty"`
	Healthy   bool   `json:"healthy"`
	HealthErr string `json:"health_error,omitempty"`
}

// waitHealthy polls until the service runs and GET /health on the config listener answers
// 200, or wait passes (0 = check once). It fails unless both hold.
func waitHealthy(ctx context.Context, m service.Manager, wait time.Duration) (serviceStatus, error) {
	deadline := time.Now().Add(wait)
	for {
		st, err := checkService(ctx, m)
		switch {
		case err != nil:
		case !st.Installed:
			return st, service.ErrNotInstalled
		case st.Running && st.Healthy:
			return st, nil
		case st.Running:
			err = fmt.Errorf("jarvisd is running but not healthy: %s", st.HealthErr)
		default:
			err = fmt.Errorf("jarvisd is not running (%s)", st.State)
		}
		if !time.Now().Before(deadline) {
			return st, err
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func checkService(ctx context.Context, m service.Manager) (serviceStatus, error) {
	st, err := m.Status(ctx)
	out := serviceStatus{Status: st}
	if err != nil || !st.Installed {
		return out, err
	}
	out.HealthURL = healthURL(st.Home)
	out.HealthErr = probeHealth(ctx, out.HealthURL)
	out.Healthy = out.HealthErr == ""
	return out, nil
}

// healthURL is the config listener's /health for the service's home, honouring its env
// file's JARVIS_HOST / JARVIS_PORT_CONFIG.
func healthURL(home string) string {
	if home != "" && os.Getenv("JARVIS_HOME") == "" {
		_ = bootstrap(home, false, io.Discard)
	}
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Config{Ports: config.DefaultPorts}
	}
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Ports[config.ListenerConfig])) + "/health"
}

func probeHealth(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err.Error()
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.Status
	}
	return ""
}

func printStatus(w io.Writer, st serviceStatus) {
	fmt.Fprintf(w, "supervisor: %s\n", st.Kind)
	state := st.State
	if st.PID > 0 {
		state += fmt.Sprintf(", pid %d", st.PID)
	}
	fmt.Fprintf(w, "state:      %s\n", state)
	if st.Detail != "" {
		fmt.Fprintf(w, "detail:     %s\n", st.Detail)
	}
	if st.Home != "" {
		fmt.Fprintf(w, "home:       %s\n", st.Home)
	}
	if st.UpgradeHelper != "" {
		fmt.Fprintf(w, "updater:    %s\n", st.UpgradeHelper)
	}
	if st.HealthURL != "" {
		h := "ok"
		if !st.Healthy {
			h = "failing: " + st.HealthErr
		}
		fmt.Fprintf(w, "health:     %s (%s)\n", h, st.HealthURL)
	}
}
