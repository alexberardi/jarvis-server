package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
	"github.com/alexberardi/jarvis-server/internal/agent/reset"
	"github.com/alexberardi/jarvis-server/internal/buildinfo"
)

// provisionCmd serves the setup API the mobile app provisions a Pi node through
// (go-pi-node.md §7 4.4), until the node is provisioned (exit 0) or interrupted. The API has no
// authentication, as on the Python node: run it only while the setup hotspot is up.
func provisionCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "address of the provisioning API (the app uses port 8080)")
	captive := fs.String("captive-listen", "", `also answer the phones' connectivity checks here, e.g. ":80" (needs the port)`)
	wifi := fs.String("wifi", "", `Wi-Fi backend: "networkmanager" (a Pi: NetworkManager over D-Bus) or "simulated" (made-up networks, no radio)`)
	iface := fs.String("wifi-iface", "wlan0", "the Wi-Fi interface (networkmanager)")
	apSSID := fs.String("ap-ssid", "", "the setup hotspot (networkmanager: started here; default jarvis-<last 8 of the node id>)")
	credsFlag := fs.String("creds", "", "credentials file (default: the user config dir's jarvis-agent/node.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := provisionWiFi(*wifi, *iface)
	if err != nil {
		return err
	}
	if c, ok := w.(io.Closer); ok {
		defer c.Close()
	}
	path, err := credsPath(*credsFlag)
	if err != nil {
		return err
	}
	log := logger()
	ctx, done := context.WithCancel(ctx)
	defer done()
	_, simulated := w.(*provisioning.Simulated)
	if !simulated && *apSSID == "" {
		*apSSID = defaultAPSSID(provisioning.SystemIdentity(enrolledID(path)).NodeID)
	}
	o := provisionOptions(path, w, *apSSID, log)
	o.OnProvisioned = done
	srv := provisioning.New(ctx, o)
	if !simulated {
		startHotspot(ctx, w, *apSSID, log)
	}
	if r, ok := provisionRecovery(w, *apSSID, o.Provisioned, srv.StateMachine(), os.Getenv, log); ok {
		go r.Run(ctx)
	}
	api := &http.Server{Addr: *listen, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	servers := []*http.Server{api}
	if *captive != "" {
		servers = append(servers, &http.Server{Addr: *captive, Handler: provisioning.CaptiveHandler(), ReadHeaderTimeout: 10 * time.Second})
	}
	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() { errc <- s.ListenAndServe() }()
	}
	log.Info("provisioning: serving", "listen", *listen, "captive", *captive, "wifi", *wifi)
	select {
	case err = <-errc:
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		s.Shutdown(sctx)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// provisionWiFi picks the Wi-Fi backend.
func provisionWiFi(name, iface string) (provisioning.WiFi, error) {
	switch name {
	case "networkmanager":
		return provisioning.NewNetworkManager(iface)
	case "simulated":
		return &provisioning.Simulated{}, nil
	}
	return nil, fmt.Errorf("no Wi-Fi backend %q; use --wifi networkmanager (a Pi) or --wifi simulated", name)
}

// defaultAPSSID is N:run_provisioning.py's hotspot name: jarvis- and the node id's last 8.
func defaultAPSSID(nodeID string) string {
	id := strings.TrimPrefix(nodeID, "jarvis-")
	return "jarvis-" + id[max(0, len(id)-8):]
}

func enrolledID(path string) string {
	c, _ := node.LoadCreds(path)
	return c.NodeID
}

// startHotspot scans first (the radio can't once it is the hotspot; ScanNetworks answers from
// that scan) and starts the setup hotspot, as N:run_provisioning.py does. A failure is logged:
// the API still answers on the node's own address.
func startHotspot(ctx context.Context, w provisioning.WiFi, ssid string, log *slog.Logger) {
	if sc, ok := w.(interface {
		ScanAndCache(context.Context) ([]provisioning.NetworkInfo, error)
	}); ok {
		nets, err := sc.ScanAndCache(ctx)
		if err != nil {
			log.Warn("provisioning: scan before the hotspot", "err", err)
		}
		log.Info("provisioning: networks scanned", "count", len(nets))
	}
	if err := w.StartAPMode(ctx, ssid); err != nil {
		log.Warn("provisioning: could not start the setup hotspot; the API answers on the node's own address", "ssid", ssid, "err", err)
		return
	}
	log.Info("provisioning: setup hotspot up", "ssid", ssid)
}

// provisionRecovery is the AP↔STA recovery cycle for a node that was provisioned before (it has
// a known Wi-Fi to go back to) and has a real radio.
func provisionRecovery(w provisioning.WiFi, apSSID string, provisioned func() bool, sm *provisioning.StateMachine,
	getenv func(string) string, log *slog.Logger) (provisioning.Recovery, bool) {
	if _, sim := w.(*provisioning.Simulated); sim || apSSID == "" || provisioned == nil || !provisioned() {
		return provisioning.Recovery{}, false
	}
	r, ok := provisioning.RecoveryFromEnv(getenv)
	if !ok {
		return r, false
	}
	r.WiFi, r.APSSID, r.Log = w, apSSID, log
	// A phone is mid-flow while an attempt is CONNECTING or REGISTERING; a failed one (ERROR,
	// hotspot restored) is idle again and must not pin the hotspot.
	r.Pairing = sm.Busy
	r.Reboot = func() error {
		out, err := exec.Command("sudo", "-n", "systemctl", "reboot").CombinedOutput()
		if err != nil {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return r, true
}

// provisionOptions stores what provisioning hands the node where `run` reads it: the
// credentials file, and K2 in the node settings store beside it under the CC-assigned id.
func provisionOptions(path string, w provisioning.WiFi, apSSID string, log *slog.Logger) provisioning.Options {
	enrolled := func() (node.Creds, bool) {
		c, err := node.LoadCreds(path)
		return c, err == nil
	}
	return provisioning.Options{
		WiFi: w, APSSID: apSSID, FirmwareVersion: buildinfo.Version(version), Log: log,
		Identity: func() provisioning.Identity {
			c, _ := enrolled()
			return provisioning.SystemIdentity(c.NodeID)
		},
		SaveCreds: func(c node.Creds) error { return node.SaveCreds(path, c) },
		SaveK2: func(nodeID, k2, kid, createdAt string) error {
			st, err := nodesettings.OpenStore(filepath.Dir(path), nodeID)
			if err != nil {
				return err
			}
			return st.SaveK2(k2, kid, createdAt)
		},
		Provisioned: func() bool { _, ok := enrolled(); return ok },
		// The same wipe as jarvisd's factory-reset task (reset.Wipe); the node stays in setup mode.
		FactoryReset: func(context.Context) ([]string, error) { return reset.Wipe(filepath.Dir(path)) },
	}
}
