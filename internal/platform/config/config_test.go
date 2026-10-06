package config

import (
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
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

func TestAddrUnknownListener(t *testing.T) {
	c, _ := load(env(map[string]string{"JARVIS_HOME": "/d"}))
	if _, err := c.Addr("nope"); err == nil {
		t.Fatal("want error")
	}
}
