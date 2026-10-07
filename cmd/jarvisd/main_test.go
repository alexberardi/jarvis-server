package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	adminmod "github.com/alexberardi/jarvis-server/internal/modules/admin"
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

// The admin BFF reaches every module it reads in process (A3, A4).
func TestAdminWiring(t *testing.T) {
	for _, m := range modules() {
		a, ok := m.(*adminmod.Module)
		if !ok {
			continue
		}
		var names []string
		for _, s := range a.SettingsSources {
			names = append(names, s.Name())
		}
		slices.Sort(names)
		if want := []string{"admin", "auth", "cc", "config", "llm", "logs", "notifications", "ocr", "stt", "tts"}; !slices.Equal(names, want) {
			t.Errorf("settings sources %v, want %v", names, want)
		}
		if a.Traces == nil || a.Prompts == nil || a.Accounts == nil || a.Models == nil || a.Verify == nil ||
			a.Logs == nil || a.Registry == nil || a.Apps == nil {
			t.Errorf("admin not wired: %+v", a)
		}
		if !slices.Contains(a.Exposure.Listeners, config.ListenerAdmin) || a.Exposure.MQTTAddr == "" {
			t.Errorf("doctor exposure: %+v", a.Exposure)
		}
		return
	}
	t.Fatal("no admin module")
}
