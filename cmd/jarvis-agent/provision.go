package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/provisioning"
	"github.com/alexberardi/jarvis-server/internal/buildinfo"
)

// provisionCmd serves the setup API the mobile app provisions a Pi node through
// (go-pi-node.md §7 4.4), until the node is provisioned (exit 0) or interrupted. The API has no
// authentication, as on the Python node: run it only while the setup hotspot is up.
func provisionCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("provision", flag.ContinueOnError)
	listen := fs.String("listen", ":8080", "address of the provisioning API (the app uses port 8080)")
	captive := fs.String("captive-listen", "", `also answer the phones' connectivity checks here, e.g. ":80" (needs the port)`)
	wifi := fs.String("wifi", "", `Wi-Fi backend: "simulated" (made-up networks, no radio)`)
	apSSID := fs.String("ap-ssid", "", "the setup hotspot to bring back after a failed attempt")
	credsFlag := fs.String("creds", "", "credentials file (default: the user config dir's jarvis-agent/node.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := provisionWiFi(*wifi)
	if err != nil {
		return err
	}
	path, err := credsPath(*credsFlag)
	if err != nil {
		return err
	}
	log := logger()
	ctx, done := context.WithCancel(ctx)
	defer done()
	o := provisionOptions(path, w, *apSSID, log)
	o.OnProvisioned = done
	api := &http.Server{Addr: *listen, Handler: provisioning.New(ctx, o), ReadHeaderTimeout: 10 * time.Second}
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

// provisionWiFi picks the Wi-Fi backend. The NetworkManager one is not built yet.
func provisionWiFi(name string) (provisioning.WiFi, error) {
	if name == "simulated" {
		return &provisioning.Simulated{}, nil
	}
	return nil, fmt.Errorf("no Wi-Fi backend %q yet (the NetworkManager one is still to come); use --wifi simulated", name)
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
	}
}
