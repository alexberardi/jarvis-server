package cc

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

func TestNodeURLs(t *testing.T) {
	cfg := config.Config{Ports: map[string]int{config.ListenerCC: 17703, config.ListenerConfig: 17700}}
	lan := func() string { return "10.0.0.122" }
	none := func() string { return "" }
	cases := []struct {
		name, host, remote, proto string
		tls                       bool
		lan                       func() string
		cfg                       config.Config
		wantCC, wantConfig        string
	}{
		// The 2026-10-09 bug: a phone on USB `adb reverse` reaches the server as localhost.
		{name: "localhost", host: "localhost:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "localhost no port", host: "localhost", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "ipv4 loopback", host: "127.0.0.1:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "ipv6 loopback", host: "[::1]:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "upper-case localhost", host: "LocalHost.:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "unspecified", host: "0.0.0.0:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:17703", wantConfig: "http://10.0.0.122:17700"},
		{name: "default ports", host: "localhost:7703", lan: lan,
			wantCC: "http://10.0.0.122:7703", wantConfig: "http://10.0.0.122:7700"},
		// No LAN address: nothing useful to say, so the fields are omitted.
		{name: "loopback without LAN", host: "localhost:7703", lan: none, cfg: cfg},
		{name: "loopback nil lan", host: "localhost:7703", cfg: cfg},
		// A LAN host is reachable from the node as the phone used it.
		{name: "lan ip", host: "10.0.0.122:7703", lan: lan, cfg: cfg,
			wantCC: "http://10.0.0.122:7703", wantConfig: "http://10.0.0.122:17700"},
		{name: "mdns name", host: "jarvis.local:7703", lan: lan,
			wantCC: "http://jarvis.local:7703", wantConfig: "http://jarvis.local:7700"},
		{name: "ipv6 lan", host: "[fd00::5]:7703", lan: lan,
			wantCC: "http://[fd00::5]:7703", wantConfig: "http://[fd00::5]:7700"},
		// Through a public hostname (no port): the CC URL as used; no config URL to derive.
		{name: "public tunnel", host: "command-center.example.io", remote: "127.0.0.1:5555", proto: "https", lan: lan,
			wantCC: "https://command-center.example.io"},
		{name: "forwarded proto ignored off-host", host: "command-center.example.io", remote: "10.0.0.9:5555", proto: "https", lan: lan,
			wantCC: "http://command-center.example.io"},
		{name: "tls", host: "jarvis.example.io:7703", tls: true, lan: lan,
			wantCC: "https://jarvis.example.io:7703", wantConfig: "https://jarvis.example.io:7700"},
		{name: "empty host", host: "", lan: lan},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v0/provisioning/token", nil)
			r.Host = c.host
			if c.remote != "" {
				r.RemoteAddr = c.remote
			}
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			if c.tls {
				r.TLS = &tls.ConnectionState{}
			} else {
				r.TLS = nil
			}
			cc, cfgURL := nodeURLs(r, c.cfg, c.lan)
			if cc != c.wantCC || cfgURL != c.wantConfig {
				t.Errorf("nodeURLs(%q) = %q, %q; want %q, %q", c.host, cc, cfgURL, c.wantCC, c.wantConfig)
			}
		})
	}
}

// The token response carries the node URLs (additive: the legacy fields are unchanged).
func TestProvisioningTokenNodeURLs(t *testing.T) {
	lan := "192.168.1.50"
	e := newEnv(t, envOpts{configure: func(m *Module) { m.lanAddr = func() string { return lan } }})
	member := e.auth.addUser(1, "hh1", "member")

	// The test server is reached as 127.0.0.1 — the loopback case.
	tok := e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1"}, bearer(member)).want(201).json()
	if tok["node_command_center_url"] != "http://192.168.1.50:7703" || tok["node_config_service_url"] != "http://192.168.1.50:7700" {
		t.Fatalf("node URLs: %v", tok)
	}
	for _, k := range []string{"token", "node_id", "expires_at", "expires_in"} {
		if _, ok := tok[k]; !ok {
			t.Fatalf("legacy field %q missing: %v", k, tok)
		}
	}

	// No LAN address: the fields are omitted rather than handing the node a loopback URL.
	lan = ""
	tok = e.do("POST", "/api/v0/provisioning/token", map[string]any{"household_id": "hh1"}, bearer(member)).want(201).json()
	if _, ok := tok["node_command_center_url"]; ok {
		t.Fatalf("node_command_center_url present without a LAN address: %v", tok)
	}
	if _, ok := tok["node_config_service_url"]; ok {
		t.Fatalf("node_config_service_url present without a LAN address: %v", tok)
	}
}
