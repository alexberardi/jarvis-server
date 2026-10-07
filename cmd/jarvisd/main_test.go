package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

func TestRunCommands(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	ctx := context.Background()

	var out bytes.Buffer
	if err := run(ctx, []string{"version"}, &out); err != nil || strings.TrimSpace(out.String()) != version {
		t.Fatalf("version: %v %q", err, out.String())
	}

	out.Reset()
	if err := run(ctx, []string{"migrate", "status"}, &out); err != nil || !strings.Contains(out.String(), "MODULE") {
		t.Fatalf("migrate status: %v %q", err, out.String())
	}

	for _, bad := range [][]string{nil, {"bogus"}, {"migrate"}} {
		if err := run(ctx, bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: want error", bad)
		}
	}
}

func TestDoctorPortsIncludeAdmin(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	t.Setenv("JARVIS_PORT_ADMIN", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ports := doctorPorts(cfg)
	for _, p := range ports {
		if p.Name == config.ListenerAdmin && p.Port == 7710 && p.Proto == "tcp" {
			return
		}
	}
	t.Fatalf("admin listener 7710 missing from %v", ports)
}
