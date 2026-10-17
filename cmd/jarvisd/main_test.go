package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	adminmod "github.com/alexberardi/jarvis-server/internal/modules/admin"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
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
		if want := []string{"admin", "auth", "cc", "config", "llm", "logs", "notifications", "ocr", "recipes", "stt", "tts"}; !slices.Equal(names, want) {
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

func TestMQTTAddrsFollowPortOffset(t *testing.T) {
	t.Setenv("JARVIS_PORT_OFFSET", "10000")
	if tcp, ws := mqttTCPAddr(), mqttWSAddr(); tcp != ":11884" || ws != ":19883" {
		t.Fatalf("offset: tcp %q ws %q", tcp, ws)
	}
	// An explicit address wins over the offset, and "" still disables the listener.
	t.Setenv("JARVIS_MQTT_ADDR", "127.0.0.1:2000")
	t.Setenv("JARVIS_MQTT_WS_ADDR", "")
	if tcp, ws := mqttTCPAddr(), mqttWSAddr(); tcp != "127.0.0.1:2000" || ws != "" {
		t.Fatalf("explicit: tcp %q ws %q", tcp, ws)
	}
}

// JARVIS_MQTT_ALLOW_ANONYMOUS reaches the broker and the doctor (CLI and admin), and
// JARVIS_MQTT_WS_ALLOWED_ORIGINS the broker's WebSocket Origin check.
func TestMQTTAnonymousAndOriginsWiring(t *testing.T) {
	t.Setenv("JARVIS_MQTT_ALLOW_ANONYMOUS", "")
	t.Setenv("JARVIS_MQTT_WS_ALLOWED_ORIGINS", "")
	if mqttAllowAnonymous() || exposure(nil).MQTTAnonymous || mqttAllowedOrigins() != nil {
		t.Fatal("anonymous or origins on by default")
	}
	t.Setenv("JARVIS_MQTT_ALLOW_ANONYMOUS", "1")
	t.Setenv("JARVIS_MQTT_WS_ALLOWED_ORIGINS", " jarvis.example.com, ,https://admin.example.net ")
	if !exposure(nil).MQTTAnonymous {
		t.Error("exposure misses anonymous")
	}
	for _, m := range modules() {
		if cc, ok := m.(*ccmod.Module); ok {
			if !cc.MQTT.AllowAnonymous || !slices.Equal(cc.MQTT.AllowedOrigins, []string{"jarvis.example.com", "https://admin.example.net"}) {
				t.Errorf("cc mqtt options %+v", cc.MQTT)
			}
			return
		}
	}
	t.Fatal("no cc module")
}
