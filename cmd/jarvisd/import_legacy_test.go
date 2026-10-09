package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/alexberardi/jarvis-server/internal/legacyimport"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

// The fixture's legacy secrets (fake): its bcrypt hashes were made with passlib ($2b$12$).
const (
	fixtureH1       = "11111111-1111-4111-8111-111111111111"
	fixturePassword = "legacy-password-1"
	kitchenKey      = "kitchen-node:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// legacyEnv points the command at a temp home and the fixture source.
func legacyEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)
	// Probe a port nothing listens on (a jarvisd on the test box answers on 7700).
	t.Setenv("JARVIS_PORT_CONFIG", strconv.Itoa(freePort(t)))
	t.Setenv("JARVIS_HOST", "127.0.0.1")
	old := legacySource
	legacySource = func(context.Context, legacyimport.PGConfig) (legacyimport.Source, error) {
		return legacyimport.DirSource{Dir: filepath.Join("..", "..", "internal", "legacyimport", "testdata", "legacy")}, nil
	}
	t.Cleanup(func() { legacySource = old })
	return home
}

func TestImportLegacyCommand(t *testing.T) {
	ctx := context.Background()
	home := legacyEnv(t)
	from := "postgres://jarvis:not-a-real-pw@127.0.0.1:5432"
	var out bytes.Buffer

	if err := run(ctx, []string{"import-legacy"}, &out); err == nil || !strings.Contains(err.Error(), "exactly one of") {
		t.Fatalf("no source: %v", err)
	}
	if err := run(ctx, []string{"import-legacy", "--from", from, "--compose", home}, &out); err == nil {
		t.Fatal("both sources accepted")
	}
	if err := run(ctx, []string{"import-legacy", "--from", from, "--accept-head", "auth"}, &out); err == nil || !strings.Contains(err.Error(), "DB=HEAD") {
		t.Fatalf("bad --accept-head: %v", err)
	}

	// A stale setup token, as if jarvisd had started once before the import.
	if err := os.WriteFile(authmod.SetupTokenPath(home), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Dry run: a report, and nothing in the home (not even a database).
	out.Reset()
	if err := run(ctx, []string{"import-legacy", "--from", from}, &out); err != nil {
		t.Fatalf("dry run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "dry run passed") || strings.Contains(out.String(), "not-a-real-pw") ||
		strings.Contains(out.String(), "owner@example.com") {
		t.Fatalf("dry run output:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "jarvis.db")); !os.IsNotExist(err) {
		t.Fatalf("dry run created the database: %v", err)
	}

	// Something answering on the config port (a legacy config-service at T-1) doesn't stop a
	// dry run into a home that never had a database; with a database it refuses.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"status":"ok"}`)) }))
	defer srv.Close()
	busyPort := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	t.Setenv("JARVIS_PORT_CONFIG", busyPort)
	if err := run(ctx, []string{"import-legacy", "--from", from}, &out); err != nil {
		t.Fatalf("dry run next to a running legacy stack: %v", err)
	}
	t.Setenv("JARVIS_PORT_CONFIG", strconv.Itoa(freePort(t)))

	out.Reset()
	if err := run(ctx, []string{"import-legacy", "--from", from, "--apply"}, &out); err != nil {
		t.Fatalf("apply: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "committed in one transaction") || !strings.Contains(out.String(), "removed the old setup token") {
		t.Fatalf("apply output:\n%s", out.String())
	}
	if _, err := os.Stat(authmod.SetupTokenPath(home)); !os.IsNotExist(err) {
		t.Fatal("stale setup token kept")
	}
	logs, _ := filepath.Glob(filepath.Join(home, "import-legacy-*.log"))
	if len(logs) != 1 {
		t.Fatalf("import log: %v", logs)
	}
	// Windows ignores the mode (the data dir's ACL protects it there).
	if fi, err := os.Stat(logs[0]); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("log mode: %v %v", fi.Mode(), err)
	}

	// Refused while something answers on the home's config port.
	t.Setenv("JARVIS_PORT_CONFIG", busyPort)
	if err := run(ctx, []string{"import-legacy", "--from", from, "--apply"}, &out); err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("running: %v", err)
	}
	t.Setenv("JARVIS_PORT_CONFIG", strconv.Itoa(freePort(t)))

	// A second run refuses: the database is no longer fresh.
	out.Reset()
	if err := run(ctx, []string{"import-legacy", "--from", from, "--apply"}, &out); err == nil || !strings.Contains(out.String(), "not fresh") {
		t.Fatalf("second apply: %v\n%s", err, out.String())
	}

	// setup-link: an admin account exists but the wizard never finished.
	dbPath := filepath.Join(home, "jarvis.db")
	if !wizardUnfinished(dbPath) {
		t.Fatal("wizard should be unfinished after an import")
	}

	verifyMigratedStack(t, home)

	d, err := db.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Write.Exec(`CREATE TABLE admin_settings (id INTEGER PRIMARY KEY, key TEXT, value TEXT, household_id TEXT, node_id TEXT, user_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if !wizardUnfinished(dbPath) {
		t.Fatal("no setup.completed row: unfinished")
	}
	if _, err := d.Write.Exec(`INSERT INTO admin_settings (key, value) VALUES ('setup.completed', 'true')`); err != nil {
		t.Fatal(err)
	}
	if wizardUnfinished(dbPath) {
		t.Fatal("setup.completed=true: finished")
	}
	if wizardUnfinished(filepath.Join(t.TempDir(), "none.db")) {
		t.Fatal("no database: not unfinished")
	}
}

// verifyMigratedStack serves the imported database with the real auth and cc modules and
// checks what a migrated install needs: the legacy node key logs in over HTTP and MQTT, a
// legacy password logs in, and a member's app lists the household's node.
func verifyMigratedStack(t *testing.T, home string) {
	ctx, cancel := context.WithCancel(context.Background())
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{}
	for n := range config.DefaultPorts {
		ports[n] = 0
	}
	cfg := config.Config{Home: home, Host: "127.0.0.1", Ports: ports}
	auth := &authmod.Module{RateLimit: authmod.RateLimit{Disabled: true}}
	cc := &ccmod.Module{Auth: auth, Users: auth, Nodes: auth, MQTT: ccmod.MQTTOptions{TCPAddr: "127.0.0.1:0"}}
	r := &module.Runner{Deps: module.Deps{Config: cfg, DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		Modules: []module.Module{auth, cc}}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		d.Close()
	})
	var ccURL, authURL string
	for range 400 {
		if a, c := r.Addr(config.ListenerAuth), r.Addr(config.ListenerCC); a != "" && c != "" && cc.Broker() != nil && cc.Broker().TCPAddr() != "" {
			authURL, ccURL = "http://"+a, "http://"+c
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ccURL == "" {
		t.Fatal("listeners never bound")
	}
	// Setup is closed: an imported superuser exists, and no token file came back.
	if code, body := httpJSON(t, "GET", authURL+"/auth/setup-status", nil, ""); code != 200 || body["needs_setup"] != false {
		t.Fatalf("setup-status: %d %v", code, body)
	}

	// HTTP node auth (cc authNode needs the auth registration, the grant and the cc_nodes row).
	nodeCall := func(key string) (int, map[string]any) {
		return httpJSON(t, "POST", ccURL+"/api/v0/admin/nodes/heartbeat", map[string]any{}, key)
	}
	if code, body := nodeCall(kitchenKey); code != 200 {
		t.Fatalf("legacy node key over HTTP: %d %v", code, body)
	}
	if code, _ := nodeCall("kitchen-node:" + strings.Repeat("x", 64)); code != 401 {
		t.Fatalf("wrong key: %d", code)
	}
	if code, body := nodeCall("orphan-reg:" + strings.Repeat("c", 64)); code != 401 || body["detail"] != "Node not configured locally" {
		t.Fatalf("registered node without a cc row: %d %v", code, body)
	}
	if code, _ := nodeCall("old-node:" + strings.Repeat("c", 64)); code != 401 {
		t.Fatalf("inactive node: %d", code)
	}

	// MQTT broker auth: username node_id, password node_key.
	if rc := mqttConnect(t, cc.Broker().TCPAddr(), "kitchen-node", strings.TrimPrefix(kitchenKey, "kitchen-node:")); rc != 0 {
		t.Fatalf("legacy node key over MQTT: CONNACK %d", rc)
	}
	if rc := mqttConnect(t, cc.Broker().TCPAddr(), "kitchen-node", "wrong"); rc == 0 {
		t.Fatal("MQTT accepted a wrong key")
	}

	// Legacy passwords log in; a member sees the household's nodes in the app.
	code, body := httpJSON(t, "POST", authURL+"/auth/login", map[string]any{"email": "member@example.com", "password": fixturePassword}, "")
	if code != 200 {
		t.Fatalf("member login: %d %v", code, body)
	}
	tok, _ := body["access_token"].(string)
	req, _ := http.NewRequest("GET", ccURL+"/api/v0/admin/nodes?household_id="+fixtureH1, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(raw), `"kitchen-node"`) {
		t.Fatalf("node list: %d %s", resp.StatusCode, raw)
	}
	if code, _ := httpJSON(t, "POST", authURL+"/auth/login", map[string]any{"email": "owner@example.com", "password": fixturePassword}, ""); code != 200 {
		t.Fatalf("superuser login: %d", code)
	}
}

func httpJSON(t *testing.T, method, url string, body any, apiKey string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// mqttConnect sends an MQTT 3.1.1 CONNECT and returns the CONNACK's return code.
func mqttConnect(t *testing.T, addr, user, pass string) byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	pk := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Connect}, ProtocolVersion: 4,
		Connect: packets.ConnectParams{ProtocolName: []byte("MQTT"), Clean: true, Keepalive: 30, ClientIdentifier: "jarvis-node-" + user,
			UsernameFlag: true, Username: []byte(user), PasswordFlag: true, Password: []byte(pass)},
	}
	var buf bytes.Buffer
	if err := pk.ConnectEncode(&buf); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	hb, err := r.ReadByte()
	if err != nil {
		return 255 // closed without a CONNACK: refused
	}
	var ack packets.Packet
	if err := ack.FixedHeader.Decode(hb); err != nil || ack.FixedHeader.Type != packets.Connack {
		t.Fatalf("not a CONNACK: %v", err)
	}
	if ack.FixedHeader.Remaining, _, err = packets.DecodeLength(r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, ack.FixedHeader.Remaining)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	ack.ProtocolVersion = 4
	if err := ack.ConnackDecode(b); err != nil {
		t.Fatal(err)
	}
	return ack.ReasonCode
}
