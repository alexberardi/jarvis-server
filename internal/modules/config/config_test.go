package config

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

const token = "s3cret-admin"

func setup(t *testing.T, served ...string) (*Module, http.Handler) {
	t.Helper()
	return start(t, openDB(t), false, served...)
}

// setupSinglePort is setup with Config.SinglePort: the mounted listeners have no port of
// their own.
func setupSinglePort(t *testing.T, served ...string) (*Module, http.Handler) {
	t.Helper()
	return start(t, openDB(t), true, served...)
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "config", Migrations()); err != nil {
		t.Fatal(err)
	}
	return d
}

// start registers and starts a module on d, as jarvisd does at each boot.
func start(t *testing.T, d *db.DB, singlePort bool, served ...string) (*Module, http.Handler) {
	t.Helper()
	ports := map[string]int{}
	for k, v := range pconfig.DefaultPorts {
		ports[k] = v
	}
	m := &Module{Served: served, AdminToken: token}
	mux := http.NewServeMux()
	m.Register(mux, module.Deps{
		Config: pconfig.Config{Host: "0.0.0.0", Ports: ports, SinglePort: singlePort},
		DB:     d,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m, mux
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var m map[string]any
	json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func services(m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, s := range m["services"].([]any) {
		x := s.(map[string]any)
		out[x["name"].(string)] = x
	}
	return out
}

func TestInfoAndHealth(t *testing.T) {
	_, h := setup(t)
	if c, m := do(t, h, "GET", "/info", ""); c != 200 || m["service"] != "jarvis-config-service" {
		t.Fatalf("%d %v", c, m)
	}
	if c, m := do(t, h, "GET", "/health", ""); c != 200 || m["status"] != "ok" {
		t.Fatalf("%d %v", c, m)
	}
}

func TestSelfRegistration(t *testing.T) {
	m, h := setup(t, pconfig.ListenerConfig, pconfig.ListenerAuth)
	_, body := do(t, h, "GET", "/services", "")
	byName := services(body)
	if len(byName) != 2 {
		t.Fatalf("services %v", byName)
	}
	auth := byName["jarvis-auth"]
	if auth["host"] != "localhost" || auth["port"] != float64(7701) || auth["url"] != "http://localhost:7701" {
		t.Fatalf("auth row %v", auth)
	}
	id := auth["id"]
	// Restarting keeps the row (and its id); a port change is picked up.
	m.deps.Config.Ports[pconfig.ListenerAuth] = 17701
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, h, "GET", "/services/jarvis-auth", "")
	if body["id"] != id || body["port"] != float64(17701) {
		t.Fatalf("after restart %v", body)
	}
}

func TestURLStyles(t *testing.T) {
	_, h := setup(t, pconfig.ListenerAuth)
	// A satellite row with published coordinates.
	c, _ := do(t, h, "POST", "/services", `{"name":"jarvis-web","host":"web","port":7722}`, "X-Admin-Token", token)
	if c != 201 {
		t.Fatal(c)
	}
	cases := map[string]map[string]string{
		"/services":                  {"jarvis-auth": "http://localhost:7701", "jarvis-web": "http://web:7722"},
		"/services?style=dockerized": {"jarvis-auth": "http://host.docker.internal:7701", "jarvis-web": "http://web:7722"},
		"/services?style=external&remote_host=10.0.0.5":      {"jarvis-auth": "http://10.0.0.5:7701"},
		"/services?style=remote&remote_host=jarvis.local":    {"jarvis-auth": "http://jarvis.local:7701"},
		"/services/jarvis-auth?style=external&remote_host=h": {"": "http://h:7701"},
	}
	for path, want := range cases {
		c, body := do(t, h, "GET", path, "")
		if c != 200 {
			t.Fatalf("%s: %d", path, c)
		}
		if url, ok := want[""]; ok {
			if body["url"] != url {
				t.Errorf("%s: url %v want %s", path, body["url"], url)
			}
			continue
		}
		got := services(body)
		for name, url := range want {
			if got[name]["url"] != url {
				t.Errorf("%s %s: url %v want %s", path, name, got[name]["url"], url)
			}
		}
	}
	c, body := do(t, h, "GET", "/services?style=bogus", "")
	detail, _ := body["detail"].([]any)
	if c != 422 || len(detail) != 1 || detail[0].(map[string]any)["loc"].([]any)[1] != "style" {
		t.Fatalf("bad style: %d %v", c, body)
	}
}

func TestTimestampsLookLikeLegacy(t *testing.T) {
	_, h := setup(t, pconfig.ListenerAuth)
	_, body := do(t, h, "GET", "/services/jarvis-auth", "")
	ts, _ := body["created_at"].(string)
	if strings.HasSuffix(ts, "Z") || !strings.Contains(ts, "T") {
		t.Fatalf("created_at %q: want a naive ISO timestamp like Python emitted", ts)
	}
}

func TestAdminWrites(t *testing.T) {
	_, h := setup(t)
	auth := []string{"X-Admin-Token", token}

	if c, _ := do(t, h, "POST", "/services", `{"name":"x","host":"h","port":1}`); c != 422 {
		t.Errorf("missing token: %d", c)
	}
	if c, m := do(t, h, "POST", "/services", `{"name":"x","host":"h","port":1}`, "X-Admin-Token", "nope"); c != 401 || m["detail"] != "Invalid admin token" {
		t.Errorf("bad token: %d %v", c, m)
	}
	if c, _ := do(t, h, "POST", "/services", `{"name":"x","host":"h","port":1}`, "X-Admin-Token", "change-me"); c != 401 {
		t.Errorf("placeholder token accepted: %d", c)
	}
	for _, bad := range []string{
		`{"name":"x","host":"http://evil","port":1}`,
		`{"name":"x","host":"h:1234","port":1}`,
		`{"name":"x","host":"h","port":70000}`,
		`{"name":"x","host":"h","port":1,"scheme":"ftp"}`,
		`{"host":"h","port":1}`,
	} {
		if c, _ := do(t, h, "POST", "/services", bad, auth...); c != 422 {
			t.Errorf("%s: %d, want 422", bad, c)
		}
	}
	if c, m := do(t, h, "POST", "/services", `{"name":"sat","host":"[fe80::1]","port":7704,"scheme":"https"}`, auth...); c != 201 || m["url"] != "https://[fe80::1]:7704" {
		t.Fatalf("create: %d %v", c, m)
	}
	if c, m := do(t, h, "POST", "/services", `{"name":"sat","host":"h","port":1}`, auth...); c != 409 || m["detail"] != "Service 'sat' already exists" {
		t.Fatalf("dup: %d %v", c, m)
	}
	if c, m := do(t, h, "PUT", "/services/sat", `{"port":7705,"description":"gpu box"}`, auth...); c != 200 || m["port"] != float64(7705) || m["description"] != "gpu box" || m["host"] != "[fe80::1]" {
		t.Fatalf("update: %d %v", c, m)
	}
	if c, m := do(t, h, "PUT", "/services/nope", `{"port":1}`, auth...); c != 404 || m["detail"] != "Service 'nope' not found" {
		t.Fatalf("update missing: %d %v", c, m)
	}
	if c, _ := do(t, h, "DELETE", "/services/sat", "", auth...); c != 204 {
		t.Fatalf("delete: %d", c)
	}
	if c, _ := do(t, h, "DELETE", "/services/sat", "", auth...); c != 404 {
		t.Fatalf("delete again: %d", c)
	}
}

func TestUnconfiguredAdminToken(t *testing.T) {
	m, h := setup(t)
	m.AdminToken = ""
	if c, body := do(t, h, "DELETE", "/services/x", "", "X-Admin-Token", "anything"); c != 500 || body["detail"] != "Admin token not configured on server" {
		t.Fatalf("%d %v", c, body)
	}
}

func TestHealthProbes(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(503)
	}))
	defer up.Close()
	port := up.Listener.Addr().(*tcpAddr).Port
	_, h := setup(t)
	auth := []string{"X-Admin-Token", token}
	do(t, h, "POST", "/services", `{"name":"up","host":"127.0.0.1","port":`+itoa(port)+`}`, auth...)
	do(t, h, "POST", "/services", `{"name":"sick","host":"127.0.0.1","port":`+itoa(port)+`,"health_path":"/bad"}`, auth...)
	do(t, h, "POST", "/services", `{"name":"down","host":"127.0.0.1","port":1}`, auth...)

	c, body := do(t, h, "GET", "/services/health", "")
	if c != 200 || body["healthy_count"] != float64(1) || body["total_count"] != float64(3) {
		t.Fatalf("%d %v", c, body)
	}
	svcs := body["services"].(map[string]any)
	if svcs["sick"].(map[string]any)["error"] != "HTTP 503" || svcs["down"].(map[string]any)["error"] != "Connection refused" {
		t.Fatalf("errors %v", svcs)
	}
	if c, body := do(t, h, "GET", "/services/up/health", ""); c != 200 || body["healthy"] != true || body["latency_ms"] == nil {
		t.Fatalf("single: %d %v", c, body)
	}

	// Single port: the mounted cc row is probed at the config port + its prefix.
	ccUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cc/health" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(404)
	}))
	defer ccUp.Close()
	m, h := setupSinglePort(t)
	m.deps.Config.Ports[pconfig.ListenerConfig] = ccUp.Listener.Addr().(*tcpAddr).Port
	m.Served = []string{pconfig.ListenerCC}
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, body := do(t, h, "GET", "/services/jarvis-command-center/health", ""); c != 200 || body["healthy"] != true {
		t.Fatalf("single port: %d %v", c, body)
	}
}

type tcpAddr = net.TCPAddr

func itoa(i int) string { return strconv.Itoa(i) }

// A served listener whose row pointed at an external server (the legacy recipes add-on) is
// taken over, with a warning naming the old address.
func TestSelfRegistrationTakesOverExternalRow(t *testing.T) {
	m, h := setup(t)
	if c, _ := do(t, h, "POST", "/services", `{"name":"jarvis-recipes-server","host":"10.0.0.30","port":7030}`, "X-Admin-Token", token); c != 201 {
		t.Fatal(c)
	}
	var logs strings.Builder
	m.deps.Log = slog.New(slog.NewTextHandler(&logs, nil))
	m.Served = []string{pconfig.ListenerRecipes}
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, h, "GET", "/services/jarvis-recipes-server", "")
	if body["host"] != "localhost" || body["port"] != float64(7030) {
		t.Fatalf("row %v", body)
	}
	if !strings.Contains(logs.String(), "was=10.0.0.30:7030") {
		t.Fatalf("no takeover warning: %s", logs.String())
	}
	logs.Reset()
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "taking over") {
		t.Fatalf("warned again for its own row: %s", logs.String())
	}
}

// A10d V2: after rolling back from a version with the recipes module to one without, the
// registry still listed jarvis-recipes-server at localhost:7030 with nothing listening. A
// jarvisd drops its own rows for listeners it doesn't serve, and only those.
func TestSelfRegistrationDropsUnservedRows(t *testing.T) {
	m, h := setup(t, pconfig.ListenerConfig, pconfig.ListenerRecipes)
	if c, _ := do(t, h, "POST", "/services", `{"name":"my-addon","host":"localhost","port":9000}`, "X-Admin-Token", token); c != 201 {
		t.Fatal(c)
	}
	m.External = func(context.Context) map[string]string {
		return map[string]string{"jarvis-pantry": "https://pantry.example.org"}
	}
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, h, "GET", "/services", "")
	if _, ok := services(body)["jarvis-recipes-server"]; !ok {
		t.Fatalf("recipes not registered: %v", body)
	}

	// The older version: no recipes listener.
	m.Served = []string{pconfig.ListenerConfig}
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, h, "GET", "/services", "")
	svc := services(body)
	if _, ok := svc["jarvis-recipes-server"]; ok {
		t.Errorf("an unserved listener's row was kept: %v", svc["jarvis-recipes-server"])
	}
	for _, keep := range []string{"jarvis-config-service", "my-addon", "jarvis-pantry"} {
		if _, ok := svc[keep]; !ok {
			t.Errorf("%s was dropped", keep)
		}
	}
}

// D48: the Pantry clients install from is listed in /services from cc's pantry.base_url.
func TestExternalServices(t *testing.T) {
	m, h := setup(t)
	url := "https://pantry.example.org"
	m.External = func(context.Context) map[string]string {
		return map[string]string{"jarvis-pantry": url, "jarvis-bad": "not a url"}
	}
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, h, "GET", "/services", "")
	svc := services(body)
	if got := svc["jarvis-pantry"]["url"]; got != "https://pantry.example.org:443" {
		t.Errorf("pantry url %v (%v)", got, svc["jarvis-pantry"])
	}
	if _, ok := svc["jarvis-bad"]; ok {
		t.Error("a malformed URL must be skipped")
	}
	// A changed setting moves the row on the next sync.
	url = "http://10.0.0.5:7721"
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, h, "GET", "/services", "")
	if got := services(body)["jarvis-pantry"]["url"]; got != "http://10.0.0.5:7721" {
		t.Errorf("moved pantry url %v", got)
	}
}

func allListeners() []string {
	var out []string
	for l := range ServiceNames {
		out = append(out, l)
	}
	return out
}

// In single-port mode jarvisd's mounted listeners are the config port + their prefix.
func TestSinglePortRegistry(t *testing.T) {
	m, h := setupSinglePort(t, allListeners()...)
	m.MQTTPort = 1884
	if err := m.syncSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	cc := "jarvis-command-center"
	cases := []struct {
		name, path, svc string
		want            map[string]any
	}{
		{"default", "/services", cc, map[string]any{"port": float64(7700), "path": "/cc", "url": "http://localhost:7700/cc"}},
		{"remote", "/services?style=remote&remote_host=10.0.0.5", cc, map[string]any{"url": "http://10.0.0.5:7700/cc"}},
		{"dockerized", "/services?style=dockerized", cc, map[string]any{"url": "http://host.docker.internal:7700/cc"}},
		{"external without public URL", "/services?style=external&remote_host=10.0.0.5", cc, map[string]any{"url": "http://10.0.0.5:7700/cc"}},
		{"config service", "/services", "jarvis-config-service", map[string]any{"port": float64(7700), "path": nil, "url": "http://localhost:7700"}},
		{"admin", "/services", "jarvis-admin", map[string]any{"port": float64(7710), "path": nil, "url": "http://localhost:7710"}},
		{"mqtt", "/services", "jarvis-mqtt-broker", map[string]any{"port": float64(1884), "path": nil, "url": "mqtt://localhost:1884"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := do(t, h, "GET", c.path, "")
			if code != 200 {
				t.Fatalf("%d %v", code, body)
			}
			row, ok := services(body)[c.svc]
			if !ok {
				t.Fatalf("no %s in %v", c.svc, body)
			}
			for k, v := range c.want {
				if got, ok := row[k]; !ok || got != v {
					t.Errorf("%s %s = %v, want %v", c.svc, k, got, v)
				}
			}
		})
	}
	t.Run("no trailing slash", func(t *testing.T) {
		_, body := do(t, h, "GET", "/services", "")
		for name, row := range services(body) {
			if u, _ := row["url"].(string); strings.HasSuffix(u, "/") {
				t.Errorf("%s url %q ends in /", name, u)
			}
		}
	})
}

// Turning single-port mode off again puts the rows back on their own ports, without a path.
func TestSinglePortSwitchBack(t *testing.T) {
	d := openDB(t)
	start(t, d, true, pconfig.ListenerConfig, pconfig.ListenerCC)
	_, h := start(t, d, false, pconfig.ListenerConfig, pconfig.ListenerCC)
	_, body := do(t, h, "GET", "/services/jarvis-command-center", "")
	if body["port"] != float64(7703) || body["path"] != nil || body["url"] != "http://localhost:7703" {
		t.Fatalf("after switching back: %v", body)
	}
}

// A public base URL is answered as entered: the path is the LAN side's.
func TestSinglePortPublicURLWins(t *testing.T) {
	m, h := setupSinglePort(t, pconfig.ListenerConfig, pconfig.ListenerCC)
	if _, err := m.SetPublicURL(context.Background(), "jarvis-command-center", "https://cc.example.io"); err != nil {
		t.Fatal(err)
	}
	_, body := do(t, h, "GET", "/services/jarvis-command-center?style=external&remote_host=10.0.0.5", "")
	if body["url"] != "https://cc.example.io:443" {
		t.Fatalf("external: %v", body)
	}
	_, body = do(t, h, "GET", "/services/jarvis-command-center?style=remote&remote_host=10.0.0.5", "")
	if body["url"] != "http://10.0.0.5:7700/cc" {
		t.Fatalf("remote: %v", body)
	}
}
