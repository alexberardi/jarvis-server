package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// newPackages is the installed Pantry packages under root, served by live (DESIGN.md §6).
// JARVIS_AGENT_UV runs that uv instead of downloading the pinned one; JARVIS_AGENT_SDK installs
// that jarvis-command-sdk requirement instead of the pinned commit (a local checkout, say).
// host, on a Pi, answers the built-in commands' SDK backends (pibuiltins.go).
func newPackages(root string, live *sidecar.Live, st sidecar.Storage, host *sdkhost.Host, log *slog.Logger) *packages.Manager {
	hc := &http.Client{Timeout: 5 * time.Minute}
	var h sidecar.Host // a nil *sdkhost.Host must stay a nil interface: no backends
	if host != nil {
		h = host
	}
	return &packages.Manager{
		Root: root,
		Python: &packages.Python{Root: root, UV: os.Getenv("JARVIS_AGENT_UV"), SDK: os.Getenv("JARVIS_AGENT_SDK"),
			HTTP: hc, Run: packages.ExecRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		HTTP:     hc,
		GOOS:     runtime.GOOS,
		Sidecars: packages.LiveSidecars{Live: live, Log: log, Storage: st, Host: h},
		Log:      log,
	}
}

// wirePackages answers the app's package install, uninstall and revert requests and Forge test
// installs, and lists
// the installed packages in report_tools. Call it after wireNodeSettings (which sets Topics).
func wirePackages(r *node.Runner, pm *packages.Manager, live *sidecar.Live, log *slog.Logger) {
	if pm == nil {
		return
	}
	q := &packages.Requests{NodeID: r.Creds.NodeID, Manager: pm, Log: log}
	if r.Topics == nil {
		r.Topics = map[string]func(context.Context, *node.CC, []byte){}
	}
	// Forge test installs too, on a Pi and a computer alike: they run no system setup.
	topics := append(slices.Clone(packages.Topics), packages.TopicTestInstall)
	for _, topic := range topics {
		r.Topics[topic] = func(ctx context.Context, cc *node.CC, payload []byte) { q.Handle(ctx, cc, topic, payload) }
	}
	r.Packages = pm.Report
	// What a Forge test install does (logs, scans, device controls) goes to jarvisd for Send to Forge.
	serve := r.OnConnected
	r.OnConnected = func(ctx context.Context, cc *node.CC) {
		go q.ShipTestEvents(ctx, cc, live.DeviceCall, 5*time.Second)
		if serve != nil {
			serve(ctx, cc)
		} else {
			<-ctx.Done()
		}
	}
}
