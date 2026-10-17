package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	adminmod "github.com/alexberardi/jarvis-server/internal/modules/admin"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	notifmod "github.com/alexberardi/jarvis-server/internal/modules/notifications"
	ocrmod "github.com/alexberardi/jarvis-server/internal/modules/ocr"
	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
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
	for _, m := range testModules(t) {
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

// testModules wires the modules from the test's environment, as serve does.
func testModules(t *testing.T) []module.Module {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return modules(cfg.Env)
}

func TestMQTTAddrsFollowPortOffset(t *testing.T) {
	t.Setenv("JARVIS_PORT_OFFSET", "10000")
	if mqtt := testModules(t)[8].(*ccmod.Module).MQTT; mqtt.TCPAddr != ":11884" || mqtt.WSAddr != ":19883" {
		t.Fatalf("offset: %+v", mqtt)
	}
	// An explicit address wins over the offset, and "" still disables the listener.
	t.Setenv("JARVIS_MQTT_ADDR", "127.0.0.1:2000")
	t.Setenv("JARVIS_MQTT_WS_ADDR", "")
	if mqtt := testModules(t)[8].(*ccmod.Module).MQTT; mqtt.TCPAddr != "127.0.0.1:2000" || mqtt.WSAddr != "" {
		t.Fatalf("explicit: %+v", mqtt)
	}
}

// modules wires every module from the environment; the order is the listeners', the
// migrations' and the runner's start order, and served/names follow it.
func TestModulesWiring(t *testing.T) {
	for k, v := range map[string]string{
		"JARVIS_CONFIG_ADMIN_TOKEN": "cfg-tok", "JARVIS_MDNS": "0", "ADMIN_API_KEY": "admin-key",
		"RELAY_HOUSEHOLD_JWT": "relay-jwt", "JARVIS_DEV_SPEAKER_HINTS": "1", "JARVIS_MQTT_ALLOW_ANONYMOUS": "1",
		"JARVIS_MQTT_ADDR": "127.0.0.1:2000", "JARVIS_MQTT_WS_ADDR": "", "JARVIS_AUTH_ADMIN_TOKEN": "auth-tok",
		"AUTH_SECRET_KEY": "hs256", "JARVIS_ADMIN_UI_DIR": "/ui", "JARVIS_APP_ID": "app-id", "JARVIS_APP_KEY": "app-key",
		"JARVIS_OSX_API_URL": "http://mac:1", "JARVIS_OSX_API_KEY": "mac-key",
		phone.EnvTwilioAccountSID: "sid", phone.EnvTwilioAuthToken: "twtok", phone.EnvTwilioFromNumber: "+1555",
		"JARVIS_PHONE_PUBLIC_URL": "https://p", "JARVIS_PHONE_PUBLIC_WSS_URL": "wss://p",
	} {
		t.Setenv(k, v)
	}
	mods := testModules(t)
	var listeners []string
	for _, m := range mods {
		listeners = append(listeners, m.Listener())
	}
	want := []string{config.ListenerConfig, config.ListenerLogs, config.ListenerOCR, config.ListenerLLM, config.ListenerTTS,
		config.ListenerSTT, config.ListenerNotifications, config.ListenerRecipes, config.ListenerCC, config.ListenerAuth, config.ListenerAdmin}
	if !slices.Equal(listeners, want) {
		t.Fatalf("listeners %v, want %v", listeners, want)
	}
	cfgm, ocrm, llm := mods[0].(*configmod.Module), mods[2].(*ocrmod.Module), mods[3].(*llmmod.Module)
	notif, cc, auth, admin := mods[6].(*notifmod.Module), mods[8].(*ccmod.Module), mods[9].(*authmod.Module), mods[10].(*adminmod.Module)
	if cfgm.AdminToken != "cfg-tok" || cfgm.Advertise || !slices.Equal(cfgm.Served, want) || cfgm.MQTTPort != 2000 ||
		cfgm.SettingsGuard == nil || cfgm.External == nil {
		t.Errorf("config: %+v", cfgm)
	}
	var names []string
	for _, l := range want {
		names = append(names, configmod.ServiceNames[l])
	}
	if !slices.Equal(auth.InProcess, names) || auth.AdminToken != "auth-tok" || auth.HMACSecret != "hs256" {
		t.Errorf("auth: InProcess %v AdminToken %q HMACSecret %q", auth.InProcess, auth.AdminToken, auth.HMACSecret)
	}
	if notif.AdminKey != "admin-key" || notif.RelayHouseholdJWT != "relay-jwt" || notif.Auth == nil || notif.Users == nil {
		t.Errorf("notifications: %+v", notif)
	}
	wantMQTT := ccmod.MQTTOptions{TCPAddr: "127.0.0.1:2000", WSAddr: "", AllowAnonymous: true}
	if cc.AdminKey != "admin-key" || !cc.DevSpeakerHints || cc.MQTT != wantMQTT ||
		cc.Phone.EnvCredentials != (phone.Credentials{AccountSID: "sid", AuthToken: "twtok", FromNumber: "+1555"}) ||
		cc.Phone.Options.PublicURL != "https://p" || cc.Phone.Options.PublicWSSURL != "wss://p" {
		t.Errorf("cc: AdminKey %q hints %v MQTT %+v Phone %+v", cc.AdminKey, cc.DevSpeakerHints, cc.MQTT, cc.Phone)
	}
	if cc.LLM == nil || cc.STT == nil || cc.TTS == nil || cc.Notify == nil || cc.Recipes == nil || cc.OCR == nil ||
		cc.Endpoints == nil || cc.Names == nil || cc.Nodes == nil {
		t.Errorf("cc not wired: %+v", cc)
	}
	if ocrm.AppID != "app-id" || ocrm.AppKey != "app-key" || ocrm.AppleVisionURL != "http://mac:1" ||
		ocrm.AppleVisionKey != "mac-key" || ocrm.LLM == nil || ocrm.AppCreds == nil {
		t.Errorf("ocr: %+v", ocrm)
	}
	if llm.AppID != "app-id" || llm.AppKey != "app-key" || llm.AppCreds == nil {
		t.Errorf("llm: AppID %q AppKey %q", llm.AppID, llm.AppKey)
	}
	if admin.UIDir != "/ui" || admin.Version != version || admin.Exposure.MDNS || admin.Exposure.MQTTWSAddr != "" ||
		admin.Exposure.MQTTAddr != "127.0.0.1:2000" || !slices.Equal(admin.Exposure.Listeners, want) || admin.TTS != adminmod.Speech(mods[4].(*ttsmod.Module)) {
		t.Errorf("admin: UIDir %q Exposure %+v TTS %v", admin.UIDir, admin.Exposure, admin.TTS)
	}
}
