package config

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(env(map[string]string{"JARVIS_HOME": "/data"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "0.0.0.0" || c.DBPath() != filepath.Join("/data", "jarvis.db") {
		t.Fatalf("got %+v", c)
	}
	for name, port := range DefaultPorts {
		if c.Ports[name] != port {
			t.Errorf("%s: port %d, want %d", name, c.Ports[name], port)
		}
	}
	if addr, _ := c.Addr(ListenerCC); addr != "0.0.0.0:7703" {
		t.Fatalf("cc addr %q", addr)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := load(env(map[string]string{
		"JARVIS_HOME":                "/data",
		"JARVIS_HOST":                "127.0.0.1",
		"JARVIS_PORT_COMMAND_CENTER": "17703",
		"JARVIS_PORT_NOTIFICATIONS":  "0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Ports[ListenerCC] != 17703 || c.Ports[ListenerNotifications] != 0 || c.Ports[ListenerAuth] != 7701 {
		t.Fatalf("ports %v", c.Ports)
	}
	if addr, _ := c.Addr(ListenerCC); addr != "127.0.0.1:17703" {
		t.Fatalf("addr %q", addr)
	}
}

func TestLoadRejectsBadPort(t *testing.T) {
	if _, err := load(env(map[string]string{"JARVIS_HOME": "/d", "JARVIS_PORT_AUTH": "http"})); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadPortOffset(t *testing.T) {
	c, err := load(env(map[string]string{
		"JARVIS_HOME":                "/d",
		"JARVIS_PORT_OFFSET":         "10000",
		"JARVIS_PORT_COMMAND_CENTER": "20000", // an explicit port is taken as is
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Ports[ListenerAuth] != 17701 || c.Ports[ListenerRecipes] != 17030 || c.Ports[ListenerCC] != 20000 {
		t.Fatalf("ports %v", c.Ports)
	}
}

func TestLoadRejectsBadPortOffset(t *testing.T) {
	for _, v := range []string{"x", "-1", "60000"} {
		if _, err := load(env(map[string]string{"JARVIS_HOME": "/d", "JARVIS_PORT_OFFSET": v})); err == nil {
			t.Errorf("JARVIS_PORT_OFFSET=%s: want error", v)
		}
	}
}

func TestShiftAddr(t *testing.T) {
	for _, tc := range []struct {
		addr   string
		offset int
		want   string
	}{
		{":1884", 0, ":1884"},
		{":1884", 10000, ":11884"},
		{"127.0.0.1:9883", 1, "127.0.0.1:9884"},
		{"", 10000, ""},             // a disabled listener stays disabled
		{"nonsense", 5, "nonsense"}, // not host:port: left for the listener to reject
	} {
		if got := ShiftAddr(tc.addr, tc.offset); got != tc.want {
			t.Errorf("ShiftAddr(%q, %d) = %q, want %q", tc.addr, tc.offset, got, tc.want)
		}
	}
}

func TestPortOffset(t *testing.T) {
	t.Setenv("JARVIS_PORT_OFFSET", "500")
	if got := PortOffset(); got != 500 {
		t.Fatalf("PortOffset() = %d", got)
	}
	t.Setenv("JARVIS_PORT_OFFSET", "junk")
	if got := PortOffset(); got != 0 {
		t.Fatalf("PortOffset() on junk = %d", got)
	}
}

func TestLoadMDNSInterfaces(t *testing.T) {
	for in, want := range map[string][]string{
		"":                 nil,
		" , ":              nil,
		"en0":              {"en0"},
		" en0 , eth1,,wg0": {"en0", "eth1", "wg0"},
	} {
		c, err := load(env(map[string]string{"JARVIS_HOME": "/d", "JARVIS_MDNS_INTERFACES": in}))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.MDNSInterfaces, want) {
			t.Errorf("JARVIS_MDNS_INTERFACES=%q: got %v, want %v", in, c.MDNSInterfaces, want)
		}
	}
}

func TestAddrUnknownListener(t *testing.T) {
	c, _ := load(env(map[string]string{"JARVIS_HOME": "/d"}))
	if _, err := c.Addr("nope"); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadEnvDefaults(t *testing.T) {
	c, err := load(env(map[string]string{"JARVIS_HOME": "/d"}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Env{MDNS: true, MQTTAddr: ":1884", MQTTWSAddr: ":9883"}); c.Env != want {
		t.Fatalf("Env = %+v, want %+v", c.Env, want)
	}
	c, _ = load(env(map[string]string{"JARVIS_HOME": "/d", "JARVIS_PORT_OFFSET": "10000"}))
	if c.Env.MQTTAddr != ":11884" || c.Env.MQTTWSAddr != ":19883" {
		t.Fatalf("offset: %+v", c.Env)
	}
}

// config keeps its own copy of the broker's defaults so it does not link the broker.
func TestMQTTDefaultsMatchBroker(t *testing.T) {
	if DefaultMQTTAddr != mqtt.DefaultTCPAddr || DefaultMQTTWSAddr != mqtt.DefaultWSAddr {
		t.Fatalf("config %q/%q, mqtt %q/%q", DefaultMQTTAddr, DefaultMQTTWSAddr, mqtt.DefaultTCPAddr, mqtt.DefaultWSAddr)
	}
}

func TestLoadEnv(t *testing.T) {
	c, err := load(env(map[string]string{
		"JARVIS_HOME": "/d", "JARVIS_PORT_OFFSET": "10000",
		"JARVIS_CONFIG_ADMIN_TOKEN": "ct", "JARVIS_MDNS": "0", "ADMIN_API_KEY": "ak", "RELAY_HOUSEHOLD_JWT": "jwt",
		"JARVIS_DEV_SPEAKER_HINTS": "1", "JARVIS_MQTT_ADDR": "127.0.0.1:2000", "JARVIS_MQTT_WS_ADDR": "",
		"JARVIS_MQTT_ALLOW_ANONYMOUS": "1", "JARVIS_MQTT_WS_ALLOWED_ORIGINS": "a.example, b.example", "JARVIS_AUTH_ADMIN_TOKEN": "at", "AUTH_SECRET_KEY": "hs",
		"JARVIS_ADMIN_UI_DIR": "/ui", "JARVIS_APP_ID": "id", "JARVIS_APP_KEY": "key",
		"JARVIS_OSX_API_URL": "http://mac", "JARVIS_OSX_API_KEY": "mk",
		"TWILIO_ACCOUNT_SID": "sid", "TWILIO_AUTH_TOKEN": "tt", "TWILIO_FROM_NUMBER": "+1",
		"JARVIS_PHONE_PUBLIC_URL": "https://p", "JARVIS_PHONE_PUBLIC_WSS_URL": "wss://p",
		"JARVIS_CONTAINER": " Unraid\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// An explicit MQTT address wins over the offset, and "" still disables the listener.
	// JARVIS_CONTAINER is trimmed and lower-cased.
	want := Env{ConfigAdminToken: "ct", AdminAPIKey: "ak", RelayHouseholdJWT: "jwt", DevSpeakerHints: true,
		MQTTAddr: "127.0.0.1:2000", MQTTWSAddr: "", MQTTAllowAnonymous: true, MQTTWSAllowedOrigins: "a.example, b.example", AuthAdminToken: "at",
		AuthSecretKey: "hs", AdminUIDir: "/ui", AppID: "id", AppKey: "key", OSXAPIURL: "http://mac", OSXAPIKey: "mk",
		TwilioAccountSID: "sid", TwilioAuthToken: "tt", TwilioFromNumber: "+1",
		PhonePublicURL: "https://p", PhonePublicWSSURL: "wss://p", Container: "unraid"}
	if c.Env != want {
		t.Fatalf("Env = %+v, want %+v", c.Env, want)
	}
	// Only "1" turns the dev switches on; any value but "0" keeps mDNS on.
	c, _ = load(env(map[string]string{"JARVIS_HOME": "/d", "JARVIS_MDNS": "false",
		"JARVIS_DEV_SPEAKER_HINTS": "true", "JARVIS_MQTT_ALLOW_ANONYMOUS": "yes"}))
	if !c.Env.MDNS || c.Env.DevSpeakerHints || c.Env.MQTTAllowAnonymous {
		t.Fatalf("switches: %+v", c.Env)
	}
}
