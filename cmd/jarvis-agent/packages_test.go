package main

import (
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// The agent subscribes to the app's package requests beside the settings topics, lists its
// installed packages in report_tools, and the snapshot and Stored data follow the sidecar in
// service (a sidecar.Live, swapped on every package change).
func TestWirePackages(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	live := &sidecar.Live{}
	tools := node.Multi{live}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	r := &node.Runner{Creds: node.Creds{NodeID: "n1"}, Log: log}
	wireNodeSettings(r, st, tools, nil, log)
	wirePackages(r, newPackages(t.TempDir(), live, st, nil, log), log)
	var topics []string
	for k := range r.Topics {
		topics = append(topics, k)
	}
	sort.Strings(topics)
	want := "config/push,k2/provision,package-install,package-revert,package-uninstall,settings/request"
	if strings.Join(topics, ",") != want {
		t.Fatalf("topics = %v", topics)
	}
	if r.Packages == nil || len(r.Packages()) != 0 {
		t.Fatal("no installed_packages")
	}
	if dataCommands(tools) != live {
		t.Fatal("Stored data doesn't follow the live sidecar")
	}
	wirePackages(r, nil, log) // the spike's --sidecar-commands: no package requests
}
