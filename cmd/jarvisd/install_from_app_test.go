package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/update"
)

func TestParseInstallFromAppArgs(t *testing.T) {
	o, err := parseInstallFromAppArgs([]string{"--run-as", "alex", "--no-firewall", "--url-file", "/tmp/u", "--qr"}, io.Discard)
	if err != nil || o.runAs != "alex" || !o.noFirewall || o.urlFile != "/tmp/u" || !o.qr || o.linkWait != time.Minute {
		t.Fatalf("%+v %v", o, err)
	}
	for _, bad := range [][]string{{"extra"}, {"--bin", "relative/jarvisd"}, {"--nope"}} {
		if _, err := parseInstallFromAppArgs(bad, io.Discard); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !filepath.IsAbs(defaultAppBinary()) {
		t.Errorf("default binary %q is not absolute", defaultAppBinary())
	}
}

// fakeBinary is a "binary" whose version is its content.
func fakeBinary(t *testing.T, path, v string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(v), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readVersion(_ context.Context, path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func TestPlaceBinary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "bundle", "jarvisd")
	dst := filepath.Join(dir, "bin", "jarvisd")
	prev := update.Paths{Exe: dst}.Prev()
	fakeBinary(t, src, "v0.2.0")
	var snaps []string
	snap := func(_ context.Context, have string, _ io.Writer) { snaps = append(snaps, have) }
	content := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	// Fresh: copied, executable, nothing snapshotted.
	if err := placeBinary(ctx, src, dst, "v0.2.0", readVersion, snap, io.Discard); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dst); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0) || content(dst) != "v0.2.0" {
		t.Fatalf("fresh copy: %v %v %q", st, err, content(dst))
	}
	if len(snaps) != 0 {
		t.Fatalf("fresh install snapshotted %v", snaps)
	}

	// Same version: untouched.
	if err := placeBinary(ctx, src, dst, "v0.2.0", readVersion, snap, io.Discard); err != nil || len(snaps) != 0 {
		t.Fatalf("same version: %v %v", err, snaps)
	}
	if _, err := os.Stat(prev); err == nil {
		t.Fatal("same version kept a .prev")
	}

	// A newer one installed (self-updated): kept.
	fakeBinary(t, dst, "v0.3.0")
	var out bytes.Buffer
	if err := placeBinary(ctx, src, dst, "v0.2.0", readVersion, snap, &out); err != nil || content(dst) != "v0.3.0" {
		t.Fatalf("newer installed: %v %q", err, content(dst))
	}
	if !strings.Contains(out.String(), "keeping jarvisd v0.3.0") {
		t.Errorf("output %q", out.String())
	}

	// An older one: snapshot, replaced, the old one kept as .prev.
	fakeBinary(t, dst, "v0.1.0")
	if err := placeBinary(ctx, src, dst, "v0.2.0", readVersion, snap, io.Discard); err != nil {
		t.Fatal(err)
	}
	if content(dst) != "v0.2.0" || content(prev) != "v0.1.0" || len(snaps) != 1 || snaps[0] != "v0.1.0" {
		t.Fatalf("older installed: dst %q prev %q snaps %v", content(dst), content(prev), snaps)
	}
	// A dev build over a release (not comparable): replaced too.
	if err := placeBinary(ctx, src, dst, "dev", readVersion, snap, io.Discard); err != nil || content(prev) != "v0.2.0" {
		t.Fatalf("dev over release: %v prev %q", err, content(prev))
	}

	// Something that doesn't run in the way: refused, left alone.
	fakeBinary(t, dst, "broken")
	broken := func(context.Context, string) (string, error) { return "", os.ErrPermission }
	if err := placeBinary(ctx, src, dst, "v0.2.0", broken, snap, io.Discard); err == nil || content(dst) != "broken" {
		t.Fatalf("unusable binary: %v %q", err, content(dst))
	}
	if entries, _ := os.ReadDir(filepath.Dir(dst)); len(entries) != 2 { // jarvisd + jarvisd.prev
		t.Errorf("leftovers: %v", entries)
	}
}

func TestSameFile(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	fakeBinary(t, a, "x")
	if !sameFile(a, filepath.Join(dir, ".", "a")) || sameFile(a, filepath.Join(dir, "b")) {
		t.Fatal("sameFile")
	}
}

// closedPort is a TCP port nothing listens on, so setup-link's health probe fails fast.
func closedPort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestWaitSetupLink(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	ports := map[string]int{}
	for k, v := range config.DefaultPorts {
		ports[k] = v
	}
	ports[config.ListenerConfig] = closedPort(t)
	ports[config.ListenerAdmin] = 7710
	cfg := config.Config{Home: home, Host: "127.0.0.1", Ports: ports}

	// Nothing yet and no time left: setup-link's answer as it stands (the admin URL).
	url, _, err := waitSetupLink(ctx, cfg, 0, time.Millisecond)
	if err != nil || url != "http://127.0.0.1:7710/" {
		t.Fatalf("no token: %q %v", url, err)
	}

	// The token is written: the setup link.
	if err := os.WriteFile(authmod.SetupTokenPath(home), []byte("tok123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	url, msg, err := waitSetupLink(ctx, cfg, time.Hour, time.Millisecond)
	if err != nil || url != "http://127.0.0.1:7710/setup#token=tok123" || !strings.Contains(msg, "tok123") {
		t.Fatalf("token: %q %q %v", url, msg, err)
	}

	// No token but an admin account (a reinstall): the admin URL at once.
	os.Remove(authmod.SetupTokenPath(home))
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(cfg.DBPath()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE auth_users (id INTEGER PRIMARY KEY, is_superuser INTEGER); INSERT INTO auth_users (is_superuser) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if url, _, err = waitSetupLink(ctx, cfg, time.Hour, time.Millisecond); err != nil || url != "http://127.0.0.1:7710/" {
		t.Fatalf("admin exists: %q %v", url, err)
	}

	// Waiting ends with the context.
	os.Remove(cfg.DBPath())
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := waitSetupLink(cctx, cfg, time.Hour, time.Hour); err == nil {
		t.Fatal("a cancelled wait returned a link")
	}
}

func TestRunningVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"status":"ok","version":"v1.2.3"}`)
	}))
	defer srv.Close()
	if v := runningVersion(context.Background(), srv.URL+"/health"); v != "v1.2.3" {
		t.Fatalf("version %q", v)
	}
	if v := runningVersion(context.Background(), srv.URL+"/nope"); v != "" {
		t.Fatalf("404 gave %q", v)
	}
	if v := runningVersion(context.Background(), fmt.Sprintf("http://127.0.0.1:%d/health", closedPort(t))); v != "" {
		t.Fatalf("closed port gave %q", v)
	}
	// Anything short of a healthy, current service on the same home reinstalls.
	st := serviceStatus{Healthy: true}
	st.Installed, st.Running, st.Home, st.Account = true, true, "/h", "alex"
	for name, c := range map[string]struct {
		mod         func(*serviceStatus)
		home, runAs string
	}{
		"stale":      {mod: func(s *serviceStatus) { s.Stale = true }},
		"unhealthy":  {mod: func(s *serviceStatus) { s.Healthy = false }},
		"other home": {home: "/other"},
		"run-as":     {runAs: "alex"},
	} {
		s := st
		if c.mod != nil {
			c.mod(&s)
		}
		if upToDate(context.Background(), s, c.home, c.runAs) {
			t.Errorf("%s: up to date", name)
		}
	}
}

// When the config doesn't load, the fresh-install port check still covers the MQTT broker and
// mDNS from the environment.
func TestPortsConfigFallbackKeepsBrokerAndMDNS(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	for _, k := range []string{"JARVIS_MQTT_ADDR", "JARVIS_MQTT_WS_ADDR", "JARVIS_MDNS"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("JARVIS_PORT_OFFSET", "bogus")
	if _, err := config.Load(); err == nil {
		t.Fatal("want the bogus offset to fail Load")
	}
	ports := doctorPorts(portsConfig())
	for _, want := range []doctor.Port{{Name: "mqtt", Port: 1884, Proto: "tcp"}, {Name: "mqtt-ws", Port: 9883, Proto: "tcp"}, {Name: "mdns", Port: 5353, Proto: "udp"}} {
		if !slices.Contains(ports, want) {
			t.Errorf("%+v missing from %v", want, ports)
		}
	}
}
