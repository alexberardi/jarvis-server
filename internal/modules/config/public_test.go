package config

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
)

// get sends GET path with the given Host, peer address and extra headers; it returns the
// name → url map (for /services) or the single row's url under its name.
func getURLs(t *testing.T, h http.Handler, path, host, peer string, hdr ...string) map[string]string {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Host, r.RemoteAddr = host, peer
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	if list, ok := body["services"].([]any); ok {
		for _, s := range list {
			x := s.(map[string]any)
			out[x["name"].(string)] = x["url"].(string)
		}
		return out
	}
	out[body["name"].(string)] = body["url"].(string)
	return out
}

func publicSetup(t *testing.T) (*Module, http.Handler) {
	t.Helper()
	m, h := setup(t, pconfig.ListenerConfig, pconfig.ListenerAuth, pconfig.ListenerCC)
	m.MQTTPort = 1884
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, u := range map[string]string{
		"jarvis-config-service": "https://config.example.io",
		"jarvis-command-center": "https://Command-Center.example.io/",
		"jarvis-mqtt-broker":    "wss://mqtt.example.io",
	} {
		if _, err := m.SetPublicURL(ctx, name, u); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	return m, h
}

const (
	lanPeer  = "10.0.0.9:51000"
	loopPeer = "127.0.0.1:51000"
)

func TestPublicURLByHost(t *testing.T) {
	_, h := publicSetup(t)
	lan := map[string]string{
		"jarvis-config-service": "http://localhost:7700",
		"jarvis-auth":           "http://localhost:7701",
		"jarvis-command-center": "http://localhost:7703",
		"jarvis-mqtt-broker":    "mqtt://localhost:1884",
	}
	public := map[string]string{
		"jarvis-config-service": "https://config.example.io:443",
		"jarvis-auth":           "http://localhost:7701", // no public URL: unchanged
		"jarvis-command-center": "https://command-center.example.io:443",
		"jarvis-mqtt-broker":    "wss://mqtt.example.io:443",
	}
	check := func(label string, got, want map[string]string) {
		t.Helper()
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", label, k, got[k], v)
			}
		}
	}
	// On the LAN: the LAN URLs.
	check("lan ip", getURLs(t, h, "/services", "10.0.0.107:7700", lanPeer), lan)
	check("lan mdns", getURLs(t, h, "/services", "jarvis.local:7700", lanPeer), lan)
	// Through the tunnel: Cloudflare preserves Host. Case, port and a trailing dot don't matter;
	// any configured public host counts, not only config's own.
	check("tunnel", getURLs(t, h, "/services", "config.example.io", loopPeer), public)
	check("tunnel case/port/dot", getURLs(t, h, "/services", "CONFIG.Example.io.:443", lanPeer), public)
	check("other public host", getURLs(t, h, "/services", "command-center.example.io", loopPeer), public)
	// The URL style does not override the public answer.
	tunnelDocker := getURLs(t, h, "/services?style=dockerized", "config.example.io", loopPeer)
	if tunnelDocker["jarvis-command-center"] != public["jarvis-command-center"] || tunnelDocker["jarvis-auth"] != "http://host.docker.internal:7701" {
		t.Errorf("tunnel dockerized: %v", tunnelDocker)
	}
	// /services/{name} answers in kind too.
	check("one, tunnel", getURLs(t, h, "/services/jarvis-command-center", "config.example.io", loopPeer),
		map[string]string{"jarvis-command-center": "https://command-center.example.io:443"})
	check("one, lan", getURLs(t, h, "/services/jarvis-command-center", "10.0.0.107:7700", lanPeer),
		map[string]string{"jarvis-command-center": "http://localhost:7703"})

	// A tunnel that rewrites Host: forwarded headers count from a loopback peer (cloudflared on
	// this machine) and are ignored from anyone else.
	check("xfh loopback", getURLs(t, h, "/services", "localhost:7700", loopPeer, "X-Forwarded-Host", "config.example.io"), public)
	check("xfh ipv6 loopback", getURLs(t, h, "/services", "localhost:7700", "[::1]:5000", "X-Forwarded-Host", "config.example.io, x"), public)
	check("cf loopback", getURLs(t, h, "/services", "localhost:7700", loopPeer, "Cf-Connecting-Ip", "203.0.113.7"), public)
	check("xfh lan", getURLs(t, h, "/services", "10.0.0.107:7700", lanPeer, "X-Forwarded-Host", "config.example.io"), lan)
	check("cf lan", getURLs(t, h, "/services", "10.0.0.107:7700", lanPeer, "Cf-Connecting-Ip", "203.0.113.7"), lan)
	check("xfh loopback unknown", getURLs(t, h, "/services", "localhost:7700", loopPeer, "X-Forwarded-Host", "evil.example"), lan)
}

func TestPublicURLStyleExternal(t *testing.T) {
	_, h := publicSetup(t)
	got := getURLs(t, h, "/services?style=external&remote_host=10.0.0.107", "10.0.0.107:7700", lanPeer)
	for k, v := range map[string]string{
		"jarvis-command-center": "https://command-center.example.io:443",
		"jarvis-mqtt-broker":    "wss://mqtt.example.io:443",
		"jarvis-auth":           "http://10.0.0.107:7701", // legacy rewrite where no public URL is set
	} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// remote keeps its legacy meaning (published coordinates, not the public URL).
	if got := getURLs(t, h, "/services?style=remote&remote_host=10.0.0.107", "10.0.0.107:7700", lanPeer); got["jarvis-command-center"] != "http://10.0.0.107:7703" {
		t.Errorf("remote: %v", got)
	}
}

func TestPublicURLSurvivesSyncSelf(t *testing.T) {
	m, h := publicSetup(t)
	m.deps.Config.Ports[pconfig.ListenerCC] = 17703
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := getURLs(t, h, "/services", "10.0.0.107:7700", lanPeer)["jarvis-command-center"]; got != "http://localhost:17703" {
		t.Fatalf("port change not synced: %s", got)
	}
	if got := getURLs(t, h, "/services", "config.example.io", loopPeer)["jarvis-command-center"]; got != "https://command-center.example.io:443" {
		t.Fatalf("public URL lost on restart: %s", got)
	}
	_, body := do(t, h, "GET", "/services/jarvis-command-center", "")
	// The response shape is frozen by the contract suite: no new keys; external_host carries
	// the public host.
	if _, extra := body["external_scheme"]; extra || body["external_host"] != "command-center.example.io" || body["external_port"] != nil ||
		body["host"] != "localhost" || body["scheme"] != "http" {
		t.Fatalf("row: %v", body)
	}
	es, err := m.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Name == "jarvis-command-center" && (e.PublicURL != "https://command-center.example.io" || e.URL != "http://localhost:17703") {
			t.Fatalf("entry: %+v", e)
		}
	}
}

func TestSetPublicURLValidation(t *testing.T) {
	m, _ := publicSetup(t)
	ctx := context.Background()
	for _, c := range []struct{ name, url, want string }{
		{"jarvis-auth", "https://auth.example.io:8443", "https://auth.example.io:8443"},
		{"jarvis-auth", "http://auth.example.io", "http://auth.example.io"},
		{"jarvis-auth", "  https://AUTH.example.io/  ", "https://auth.example.io"},
		{"jarvis-auth", "https://203.0.113.5", "https://203.0.113.5"},
		{"jarvis-auth", "https://[2001:db8::1]:8443", "https://[2001:db8::1]:8443"},
		{"jarvis-mqtt-broker", "mqtts://mqtt.example.io", "mqtts://mqtt.example.io"},
		{"jarvis-mqtt-broker", "ws://mqtt.example.io:8080", "ws://mqtt.example.io:8080"},
		{"jarvis-auth", "", ""}, // clears
	} {
		e, err := m.SetPublicURL(ctx, c.name, c.url)
		if err != nil || e.PublicURL != c.want {
			t.Errorf("%s %q: %+v %v", c.name, c.url, e.PublicURL, err)
		}
	}
	for _, c := range []struct{ name, url string }{
		{"jarvis-auth", "auth.example.io"},                // no scheme
		{"jarvis-auth", "ftp://auth.example.io"},          // scheme
		{"jarvis-auth", "wss://auth.example.io"},          // wrong protocol for an HTTP service
		{"jarvis-mqtt-broker", "https://mqtt.example.io"}, // wrong protocol for the broker
		{"jarvis-auth", "https://auth.example.io/api"},    // path
		{"jarvis-auth", "https://u:p@auth.example.io"},    // user info
		{"jarvis-auth", "https://auth.example.io?x=1"},    // query
		{"jarvis-auth", "https://auth.example.io#f"},      // fragment
		{"jarvis-auth", "https://auth.example.io:0"},      // port
		{"jarvis-auth", "https://auth.example.io:99999"},
		{"jarvis-auth", "https://localhost"}, // loopback
		{"jarvis-auth", "https://127.0.0.1:7701"},
		{"jarvis-auth", "https://[::1]"},
		{"jarvis-auth", "https://bad host.io"},
	} {
		_, err := m.SetPublicURL(ctx, c.name, c.url)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Fields[0].Loc[1] != "public_url" {
			t.Errorf("%s %q: want a public_url validation error, got %v", c.name, c.url, err)
		}
	}
	if _, err := m.SetPublicURL(ctx, "nope", "https://x.example.io"); !errors.Is(err, ErrServiceNotFound) {
		t.Errorf("unknown row: %v", err)
	}
}

func TestAddServiceWithPublicURL(t *testing.T) {
	m, h := publicSetup(t)
	e, err := m.AddService(context.Background(), NewService{Name: "jarvis-recipes-server", URL: "http://10.0.0.9:7030",
		PublicURL: "https://recipes.example.io"})
	if err != nil || e.PublicURL != "https://recipes.example.io" || e.URL != "http://10.0.0.9:7030" {
		t.Fatalf("%+v %v", e, err)
	}
	if got := getURLs(t, h, "/services", "recipes.example.io", loopPeer)["jarvis-recipes-server"]; got != "https://recipes.example.io:443" {
		t.Fatalf("public: %s", got)
	}
	var ve *ValidationError
	if _, err := m.AddService(context.Background(), NewService{Name: "x", URL: "http://10.0.0.9:1", PublicURL: "https://x.io/p"}); !errors.As(err, &ve) {
		t.Fatalf("bad public url accepted: %v", err)
	}
	if _, err := m.byName(context.Background(), "x"); err == nil {
		t.Fatal("row inserted despite the validation error")
	}
}
