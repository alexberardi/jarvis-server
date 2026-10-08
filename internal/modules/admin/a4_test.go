package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	logsmod "github.com/alexberardi/jarvis-server/internal/modules/logs"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// --- fakes for the A4 interfaces ---

type fakeRegistry struct {
	mu      sync.Mutex
	entries []configmod.ServiceEntry
	probed  int
}

func (f *fakeRegistry) Services(context.Context) ([]configmod.ServiceEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]configmod.ServiceEntry(nil), f.entries...), nil
}

func (f *fakeRegistry) ProbeAll(_ context.Context, es []configmod.ServiceEntry) map[string]configmod.HealthStatus {
	f.mu.Lock()
	f.probed++
	f.mu.Unlock()
	out := map[string]configmod.HealthStatus{}
	for _, e := range es {
		lat := 1.5
		out[e.Name] = configmod.HealthStatus{Healthy: e.Name != "jarvis-web", LatencyMS: &lat}
	}
	return out
}

func (f *fakeRegistry) AddService(_ context.Context, n configmod.NewService) (configmod.ServiceEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case n.URL == "bad":
		return configmod.ServiceEntry{}, &configmod.ValidationError{Fields: nil}
	case n.Name == "jarvis-auth":
		return configmod.ServiceEntry{}, configmod.ErrServiceManaged
	}
	for _, e := range f.entries {
		if e.Name == n.Name {
			return configmod.ServiceEntry{}, configmod.ErrServiceExists
		}
	}
	e := configmod.ServiceEntry{Name: n.Name, URL: n.URL, HealthPath: "/health"}
	f.entries = append(f.entries, e)
	return e, nil
}

func (f *fakeRegistry) RemoveService(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, e := range f.entries {
		if e.Name == name {
			if e.Managed != "" {
				return configmod.ErrServiceManaged
			}
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return nil
		}
	}
	return configmod.ErrServiceNotFound
}

func (f *fakeRegistry) SetPublicURL(_ context.Context, name, raw string) (configmod.ServiceEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if raw == "bad" {
		return configmod.ServiceEntry{}, &configmod.ValidationError{Fields: nil}
	}
	for i, e := range f.entries {
		if e.Name == name {
			f.entries[i].PublicURL = raw
			return f.entries[i], nil
		}
	}
	return configmod.ServiceEntry{}, configmod.ErrServiceNotFound
}

type fakeApps struct {
	mu   sync.Mutex
	apps map[string]*authmod.AppClient
	keys int
}

func (f *fakeApps) AppClients(context.Context) ([]authmod.AppClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []authmod.AppClient{}
	for _, a := range f.apps {
		out = append(out, *a)
	}
	return out, nil
}

func (f *fakeApps) CreateAppClient(_ context.Context, id, name string) (authmod.AppClient, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.apps[id]; ok {
		return authmod.AppClient{}, "", authmod.ErrAppExists
	}
	a := &authmod.AppClient{AppID: id, Name: name, IsActive: true, CreatedAt: "2026-10-07T00:00:00Z"}
	f.apps[id] = a
	f.keys++
	return *a, fmt.Sprintf("key-%d", f.keys), nil
}

func (f *fakeApps) RotateAppClient(_ context.Context, id string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[id]
	if !ok {
		return "", "", authmod.ErrAppNotFound
	}
	a.IsActive = true
	f.keys++
	return fmt.Sprintf("key-%d", f.keys), "2026-10-07T01:00:00Z", nil
}

func (f *fakeApps) RevokeAppClient(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.apps[id]
	if !ok {
		return authmod.ErrAppNotFound
	}
	a.IsActive = false
	return nil
}

// failTransport fails the test if any request is made.
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected network request: %s %s", r.Method, r.URL)
	return nil, fmt.Errorf("network disabled in this test")
}

type a4Env struct {
	mux  *http.ServeMux
	m    *Module
	logs *logsmod.Module
	reg  *fakeRegistry
	apps *fakeApps
	logb *strings.Builder
	lmu  *sync.Mutex
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (e *a4Env) logText() string {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	return e.logb.String()
}

func newA4(t *testing.T, version string) *a4Env {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, "logs", logsmod.Migrations()); err != nil {
		t.Fatal(err)
	}
	e := &a4Env{logb: &strings.Builder{}, lmu: &sync.Mutex{}}
	log := slog.New(slog.NewTextHandler(lockedWriter{e.lmu, e.logb}, nil))
	e.logs = &logsmod.Module{}
	e.logs.Register(http.NewServeMux(), module.Deps{DB: d, Log: log})
	e.reg = &fakeRegistry{entries: []configmod.ServiceEntry{
		{Name: "jarvis-auth", URL: "http://localhost:7701", Port: 7701, Managed: configmod.ManagedListener},
		{Name: "jarvis-mqtt-broker", URL: "mqtt://localhost:1884", Port: 1884, Managed: configmod.ManagedBroker},
		{Name: "jarvis-pantry", URL: "https://pantry.example:443", Managed: configmod.ManagedSetting},
		{Name: "jarvis-web", URL: "http://10.0.0.5:7722", HealthPath: "/api/health", Description: "web chat"},
	}}
	e.apps = &fakeApps{apps: map[string]*authmod.AppClient{}}
	e.m = &Module{UI: builtUI(), Verify: fakeVerify, Version: version, Logs: e.logs, Registry: e.reg, Apps: e.apps,
		LogPoll: 10 * time.Millisecond, Updates: UpdateOptions{Client: &http.Client{Transport: failTransport{t}}}}
	e.mux = http.NewServeMux()
	e.m.Register(e.mux, module.Deps{DB: d, Log: log,
		Config: pconfig.Config{Home: t.TempDir(), Ports: map[string]int{pconfig.ListenerAdmin: 7710}}})
	if err := e.m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *a4Env) seedLogs(t *testing.T, recs ...logging.Record) {
	t.Helper()
	if err := e.logs.Sink().Write(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
}

func TestA4RoutesAreGated(t *testing.T) {
	e := newA4(t, "v1.2.3")
	for _, c := range []struct {
		method, path, body string
		ok                 int
	}{
		{"GET", "/api/logs", "", 200},
		{"GET", "/api/logs/sources", "", 200},
		{"GET", "/api/connections?health=false", "", 200},
		{"POST", "/api/connections/services", `{"name":"x","url":"http://h:1"}`, 201},
		{"DELETE", "/api/connections/services/x", "", 204},
		{"PUT", "/api/connections/services/jarvis-auth/public_url", `{"public_url":"https://a.example.io"}`, 200},
		{"POST", "/api/connections/apps", `{"app_id":"a1","name":"A"}`, 201},
		{"POST", "/api/connections/apps/a1/rotate", "", 200},
		{"POST", "/api/connections/apps/a1/revoke", "", 200},
		{"GET", "/api/update", "", 200},
		{"POST", "/api/update/check", "", 200},
		{"PUT", "/api/update/settings", `{"enabled":false}`, 200},
	} {
		if w := send(e.mux, c.method, c.path, c.body); w.Code != 401 {
			t.Errorf("anonymous %s %s: %d", c.method, c.path, w.Code)
		}
		if w := send(e.mux, c.method, c.path, c.body, "Authorization", "Bearer member"); w.Code != 403 {
			t.Errorf("member %s %s: %d", c.method, c.path, w.Code)
		}
		if w := send(e.mux, c.method, c.path, c.body, root...); w.Code != c.ok {
			t.Errorf("superuser %s %s: %d %s", c.method, c.path, w.Code, w.Body.String())
		} else if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s %s: no security headers", c.method, c.path)
		}
	}
	// The stream is gated before it starts streaming.
	if w := send(e.mux, "GET", "/api/logs/stream", ""); w.Code != 401 {
		t.Errorf("anonymous stream: %d", w.Code)
	}
	if w := send(e.mux, "GET", "/api/logs/stream", "", "Authorization", "Bearer member"); w.Code != 403 {
		t.Errorf("member stream: %d", w.Code)
	}
}

func TestLogsQueryRoute(t *testing.T) {
	e := newA4(t, "v1.2.3")
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	e.seedLogs(t,
		logging.Record{Time: base, Level: slog.LevelInfo, Message: "started", Source: "jarvisd"},
		logging.Record{Time: base.Add(time.Second), Level: slog.LevelError, Message: "engine crashed", Source: "llm"},
		logging.Record{Time: base.Add(2 * time.Second), Level: slog.LevelWarn, Message: "mic quiet", Source: "jarvis-node",
			Attrs: map[string]any{"node_id": "kitchen"}},
	)
	out := decode(t, send(e.mux, "GET", "/api/logs?limit=2", "", root...))
	logs := out["logs"].([]any)
	first := logs[0].(map[string]any)
	if len(logs) != 2 || first["message"] != "mic quiet" || first["node_id"] != "kitchen" || first["level"] != "WARNING" ||
		first["timestamp"] != "2026-10-07T12:00:02Z" || first["id"] == nil || out["next_cursor"] == nil {
		t.Fatalf("page 1: %v", out)
	}
	out = decode(t, send(e.mux, "GET", "/api/logs?limit=2&cursor="+out["next_cursor"].(string), "", root...))
	if logs := out["logs"].([]any); len(logs) != 1 || logs[0].(map[string]any)["message"] != "started" || out["next_cursor"] != nil {
		t.Fatalf("page 2: %v", out)
	}
	for q, want := range map[string]string{
		"node_id=kitchen":            "mic quiet",
		"service=llm":                "engine crashed",
		"min_level=error":            "engine crashed",
		"level=INFO":                 "started",
		"q=CRASH":                    "engine crashed",
		"until=2026-10-07T12:00:00Z": "started",
	} {
		out := decode(t, send(e.mux, "GET", "/api/logs?"+q, "", root...))
		if logs := out["logs"].([]any); len(logs) != 1 || logs[0].(map[string]any)["message"] != want {
			t.Errorf("%s: %v", q, out)
		}
	}
	for _, q := range []string{"limit=0", "limit=1001", "level=LOUD", "min_level=x", "since=yesterday", "cursor=junk"} {
		if w := send(e.mux, "GET", "/api/logs?"+q, "", root...); w.Code != 422 {
			t.Errorf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	src := decode(t, send(e.mux, "GET", "/api/logs/sources?since=2026-10-07T00:00:00Z", "", root...))
	if fmt.Sprint(src["services"]) != "[jarvis-node jarvisd llm]" || fmt.Sprint(src["nodes"]) != "[kitchen]" {
		t.Fatalf("sources: %v", src)
	}
}

func TestLogsStreamTails(t *testing.T) {
	e := newA4(t, "v1.2.3")
	e.seedLogs(t, logging.Record{Time: time.Now(), Level: slog.LevelError, Message: "before the tail", Source: "jarvisd"})
	srv := httptest.NewServer(e.mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/logs/stream?min_level=WARNING", nil)
	req.Header.Set("Authorization", "Bearer root")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan map[string]any, 10)
	ids := make(chan string, 10)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if id, ok := strings.CutPrefix(line, "id: "); ok {
				ids <- id
			}
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var v map[string]any
				_ = json.Unmarshal([]byte(data), &v)
				events <- v
			}
		}
		close(events)
	}()
	// Only what arrives after connecting, filtered.
	e.seedLogs(t,
		logging.Record{Time: time.Now(), Level: slog.LevelInfo, Message: "too quiet", Source: "jarvisd"},
		logging.Record{Time: time.Now(), Level: slog.LevelWarn, Message: "tail 1", Source: "jarvisd"},
	)
	e.seedLogs(t, logging.Record{Time: time.Now(), Level: slog.LevelError, Message: "tail 2", Source: "llm"})
	var got []string
	for len(got) < 2 {
		select {
		case ev := <-events:
			got = append(got, ev["message"].(string))
		case <-time.After(5 * time.Second):
			t.Fatalf("tail got %v", got)
		}
	}
	if got[0] != "tail 1" || got[1] != "tail 2" {
		t.Fatalf("tail: %v", got)
	}
	<-ids
	lastID := <-ids

	// Resuming after an id replays exactly what followed it.
	e.seedLogs(t, logging.Record{Time: time.Now(), Level: slog.LevelError, Message: "tail 3", Source: "llm"})
	select {
	case ev := <-events:
		if ev["message"] != "tail 3" {
			t.Fatalf("tail 3: %v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no tail 3")
	}
	cancel()
	rctx, rcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer rcancel()
	req2, _ := http.NewRequestWithContext(rctx, "GET", srv.URL+"/api/logs/stream?after="+lastID, nil)
	req2.Header.Set("Authorization", "Bearer root")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp2.Body) // ends at the client timeout
	resp2.Body.Close()
	if !strings.Contains(string(body), `"message":"tail 3"`) || strings.Contains(string(body), `"tail 2"`) {
		t.Fatalf("resume: %s", body)
	}
	if w := send(e.mux, "GET", "/api/logs/stream?after=x", "", root...); w.Code != 422 {
		t.Fatalf("bad after: %d", w.Code)
	}
}

func TestConnectionsPublicURL(t *testing.T) {
	e := newA4(t, "v1.2.3")
	list := func() (map[string]any, map[string]any) {
		out := decode(t, send(e.mux, "GET", "/api/connections?health=false", "", root...))
		return out["listeners"].([]any)[0].(map[string]any), out["external"].([]any)[1].(map[string]any)
	}
	if l, x := list(); l["public_url"] != nil || x["public_url"] != nil {
		t.Fatalf("unset public_url should be null: %v %v", l, x)
	}
	w := send(e.mux, "PUT", "/api/connections/services/jarvis-auth/public_url", `{"public_url":"https://auth.example.io"}`, root...)
	if w.Code != 200 || decode(t, w)["public_url"] != "https://auth.example.io" {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	if l, _ := list(); l["public_url"] != "https://auth.example.io" || l["url"] != "http://localhost:7701" {
		t.Fatalf("listener after set: %v", l)
	}
	for body, code := range map[string]int{`{"public_url":"bad"}`: 422, `nope`: 422} {
		if w := send(e.mux, "PUT", "/api/connections/services/jarvis-auth/public_url", body, root...); w.Code != code {
			t.Errorf("set %s: %d", body, w.Code)
		}
	}
	if w := send(e.mux, "PUT", "/api/connections/services/nope/public_url", `{"public_url":"https://x.io"}`, root...); w.Code != 404 {
		t.Errorf("unknown: %d", w.Code)
	}
	// null clears.
	if w := send(e.mux, "PUT", "/api/connections/services/jarvis-auth/public_url", `{"public_url":null}`, root...); w.Code != 200 ||
		decode(t, w)["public_url"] != nil {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
}

func TestConnectionsRoutes(t *testing.T) {
	e := newA4(t, "v1.2.3")
	out := decode(t, send(e.mux, "GET", "/api/connections", "", root...))
	listeners, external := out["listeners"].([]any), out["external"].([]any)
	if len(listeners) != 2 || len(external) != 2 || out["apps"] == nil {
		t.Fatalf("connections: %v", out)
	}
	if l := listeners[0].(map[string]any); l["name"] != "jarvis-auth" || l["health"].(map[string]any)["healthy"] != true {
		t.Fatalf("listener: %v", l)
	}
	pantry, web := external[0].(map[string]any), external[1].(map[string]any)
	if pantry["removable"] != false || pantry["managed"] != "setting" || web["removable"] != true ||
		web["health"].(map[string]any)["healthy"] != false || web["health_path"] != "/api/health" {
		t.Fatalf("external: %v", external)
	}
	e.reg.probed = 0
	out = decode(t, send(e.mux, "GET", "/api/connections?health=false", "", root...))
	if e.reg.probed != 0 || out["listeners"].([]any)[0].(map[string]any)["health"] != nil {
		t.Fatalf("health=false still probed: %v", out)
	}

	// Registry writes.
	if w := send(e.mux, "POST", "/api/connections/services", `{"name":"sat","url":"http://gpu:7704"}`, root...); w.Code != 201 ||
		decode(t, w)["removable"] != true {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	for body, code := range map[string]int{
		`{"name":"sat","url":"http://gpu:7704"}`:    409,
		`{"name":"jarvis-auth","url":"http://x:1"}`: 409,
		`{"name":"y","url":"bad"}`:                  422,
		`not json`:                                  422,
	} {
		if w := send(e.mux, "POST", "/api/connections/services", body, root...); w.Code != code {
			t.Errorf("add %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for path, code := range map[string]int{"sat": 204, "nope": 404, "jarvis-pantry": 409} {
		if w := send(e.mux, "DELETE", "/api/connections/services/"+path, "", root...); w.Code != code {
			t.Errorf("delete %s: %d", path, w.Code)
		}
	}

	// App clients: the key is in the create/rotate response, once, and never cached or logged.
	w := send(e.mux, "POST", "/api/connections/apps", `{"app_id":"jarvis-web","name":"Web chat"}`, root...)
	created := decode(t, w)
	if w.Code != 201 || created["app_key"] != "key-1" || created["is_active"] != true || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %v", w.Code, created)
	}
	for body, code := range map[string]int{
		`{"app_id":"jarvis-web","name":"again"}`: 409,
		`{"app_id":"bad id!","name":"x"}`:        422,
		`{"app_id":"ok","name":""}`:              422,
	} {
		if w := send(e.mux, "POST", "/api/connections/apps", body, root...); w.Code != code {
			t.Errorf("create %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := send(e.mux, "POST", "/api/connections/apps/jarvis-web/revoke", "", root...); w.Code != 200 || decode(t, w)["is_active"] != false {
		t.Fatalf("revoke: %d", w.Code)
	}
	w = send(e.mux, "POST", "/api/connections/apps/jarvis-web/rotate", "", root...)
	if r := decode(t, w); w.Code != 200 || r["app_key"] != "key-2" || r["is_active"] != true || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate: %d %v", w.Code, r)
	}
	for _, path := range []string{"/api/connections/apps/nope/rotate", "/api/connections/apps/nope/revoke"} {
		if w := send(e.mux, "POST", path, "", root...); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	apps := decode(t, send(e.mux, "GET", "/api/connections?health=false", "", root...))["apps"].([]any)
	if len(apps) != 1 || apps[0].(map[string]any)["app_key"] != nil || apps[0].(map[string]any)["is_active"] != true {
		t.Fatalf("apps list: %v", apps)
	}
	if logs := e.logText(); strings.Contains(logs, "key-1") || strings.Contains(logs, "key-2") {
		t.Fatalf("an app key was logged:\n%s", logs)
	}
}

// fakeGitHub serves a releases list and counts requests.
func fakeGitHub(t *testing.T, status int, releases []map[string]any) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/repos/alexberardi/jarvis-server/releases" || r.Header.Get("User-Agent") == "" {
			t.Errorf("request: %s %v", r.URL, r.Header)
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(releases)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func release(tag string, pre, draft bool, assets ...string) map[string]any {
	var as []map[string]any
	for _, a := range assets {
		as = append(as, map[string]any{"name": a, "browser_download_url": "https://dl.example/" + tag + "/" + a, "size": 1234})
	}
	return map[string]any{"tag_name": tag, "html_url": "https://github.com/alexberardi/jarvis-server/releases/tag/" + tag,
		"body": "notes for " + tag, "draft": draft, "prerelease": pre, "published_at": "2026-10-01T00:00:00Z", "assets": as}
}

func archive(tag string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return "jarvisd-" + tag + "-" + runtime.GOOS + "-" + runtime.GOARCH + ext
}

func installer() string {
	if runtime.GOOS == "windows" {
		return "install.ps1"
	}
	return "install.sh"
}

func TestUpdateOffMakesNoNetworkCall(t *testing.T) {
	t.Setenv(EnvAllowUpdates, "")
	e := newA4(t, "v1.2.3") // its client fails the test on any request
	for _, c := range []struct{ method, path string }{{"GET", "/api/update"}, {"POST", "/api/update/check"}} {
		st := decode(t, send(e.mux, c.method, c.path, "", root...))
		if st["updates_enabled"] != false || st["checked"] != false || st["update_available"] != false ||
			st["up_to_date"] != false || st["reason"] == nil || st["current_version"] != "v1.2.3" || st["latest_version"] != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, st)
		}
	}
	// Turning it on doesn't check by itself; off again stays offline.
	st := decode(t, send(e.mux, "PUT", "/api/update/settings", `{"enabled":true}`, root...))
	if st["updates_enabled"] != true || st["checked"] != false || st["up_to_date"] != false {
		t.Fatalf("enable: %v", st)
	}
	if v := e.m.Settings().Bool(context.Background(), SettingUpdatesEnabled, settings.Scope{}); !v {
		t.Fatal("setting not stored")
	}
	if st := decode(t, send(e.mux, "PUT", "/api/update/settings", `{"enabled":false}`, root...)); st["updates_enabled"] != false {
		t.Fatalf("disable: %v", st)
	}
	_ = decode(t, send(e.mux, "GET", "/api/update", "", root...))
	if w := send(e.mux, "PUT", "/api/update/settings", `{"enabled":"yes"}`, root...); w.Code != 422 {
		t.Fatalf("bad body: %d", w.Code)
	}
}

func TestUpdateCheckAgainstGitHub(t *testing.T) {
	t.Setenv(EnvAllowUpdates, "1") // the env fallback turns checks on
	gh, hits := fakeGitHub(t, 200, []map[string]any{
		release("v2.0.0", false, true, archive("v2.0.0")), // a draft is never offered
		release("v1.4.0-rc.1", true, false, archive("v1.4.0-rc.1")),
		release("v1.3.0", false, false, archive("v1.3.0"), "SHA256SUMS", installer(), "jarvisd-v1.3.0-plan9-mips.tar.gz"),
		release("v1.2.3", false, false, archive("v1.2.3")),
		release("nightly", false, false),
	})
	e := newA4(t, "v1.2.3")
	e.m.Updates = UpdateOptions{APIBase: gh.URL}
	st := decode(t, send(e.mux, "GET", "/api/update", "", root...))
	asset, _ := st["asset"].(map[string]any)
	if st["updates_enabled"] != true || st["checked"] != true || st["latest_version"] != "v1.3.0" || st["update_available"] != true ||
		st["up_to_date"] != false || st["prerelease"] != false || st["release_notes"] != "notes for v1.3.0" ||
		asset == nil || asset["name"] != archive("v1.3.0") || st["checksums_url"] != "https://dl.example/v1.3.0/SHA256SUMS" ||
		st["install_command"] == nil || !strings.Contains(st["install_command"].(string), "https://dl.example/v1.3.0/"+installer()) ||
		st["checked_at"] == nil || st["platform"] != runtime.GOOS+"-"+runtime.GOARCH {
		t.Fatalf("status: %v", st)
	}
	// Cached for an hour; a forced check asks again.
	decode(t, send(e.mux, "GET", "/api/update", "", root...))
	if hits.Load() != 1 {
		t.Fatalf("GET re-checked: %d", hits.Load())
	}
	decode(t, send(e.mux, "POST", "/api/update/check", "", root...))
	if hits.Load() != 2 {
		t.Fatalf("forced check: %d", hits.Load())
	}

	// A prerelease build follows prereleases.
	pre := newA4(t, "v1.4.0-rc.0")
	pre.m.Updates = UpdateOptions{APIBase: gh.URL}
	if st := decode(t, send(pre.mux, "POST", "/api/update/check", "", root...)); st["latest_version"] != "v1.4.0-rc.1" || st["update_available"] != true ||
		st["prerelease"] != true || st["install_command"] != nil {
		t.Fatalf("prerelease: %v", st)
	}

	// The latest stable build is up to date — only after a real check.
	cur := newA4(t, "v1.3.0")
	cur.m.Updates = UpdateOptions{APIBase: gh.URL}
	if st := decode(t, send(cur.mux, "GET", "/api/update", "", root...)); st["up_to_date"] != true || st["update_available"] != false || st["checked"] != true {
		t.Fatalf("current: %v", st)
	}

	// A dev build can't be compared: checked, but neither available nor up to date.
	dev := newA4(t, "")
	dev.m.Updates = UpdateOptions{APIBase: gh.URL}
	if st := decode(t, send(dev.mux, "GET", "/api/update", "", root...)); st["checked"] != true || st["up_to_date"] != false ||
		st["update_available"] != false || st["current_version"] != "dev" || st["reason"] == nil || st["latest_version"] != "v1.3.0" {
		t.Fatalf("dev: %v", st)
	}
}

func TestUpdateCheckFailureIsHonest(t *testing.T) {
	t.Setenv(EnvAllowUpdates, "true")
	gh, _ := fakeGitHub(t, 503, nil)
	e := newA4(t, "v1.2.3")
	e.m.Updates = UpdateOptions{APIBase: gh.URL}
	st := decode(t, send(e.mux, "GET", "/api/update", "", root...))
	if st["checked"] != false || st["up_to_date"] != false || st["update_available"] != false ||
		!strings.Contains(fmt.Sprint(st["reason"]), "HTTP 503") {
		t.Fatalf("failure: %v", st)
	}
	// Unreachable.
	e.m.Updates = UpdateOptions{APIBase: "http://127.0.0.1:1"}
	if st := decode(t, send(e.mux, "POST", "/api/update/check", "", root...)); st["checked"] != false ||
		!strings.Contains(fmt.Sprint(st["reason"]), "Couldn't reach GitHub") {
		t.Fatalf("unreachable: %v", st)
	}
}
