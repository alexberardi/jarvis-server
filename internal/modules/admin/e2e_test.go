package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

// gatewayStack runs the real auth module and the admin module under the runner on
// ephemeral ports, wired the way cmd/jarvisd wires them. It returns the admin base URL.
func gatewayStack(t *testing.T) (string, *authmod.Module, pconfig.Config) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]int{}
	for n := range pconfig.DefaultPorts {
		ports[n] = 0
	}
	cfg := pconfig.Config{Home: t.TempDir(), Host: "127.0.0.1", Ports: ports}
	auth := &authmod.Module{RateLimit: authmod.RateLimit{Disabled: true}}
	r := &module.Runner{
		Deps: module.Deps{Config: cfg, DB: d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
		// Admin first: the handler lookup must not depend on registration order.
		Modules: []module.Module{&Module{UI: builtUI(), Verify: auth.VerifyUser}, auth},
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		d.Close()
	})
	for range 400 {
		if a := r.Addr(pconfig.ListenerAdmin); a != "" {
			return "http://" + a, auth, cfg
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("admin listener never bound")
	return "", nil, cfg
}

func call(t *testing.T, method, url string, body any, hdr ...string) (int, any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGatewayEndToEnd(t *testing.T) {
	base, _, cfg := gatewayStack(t)

	if code, out := call(t, "GET", base+"/api/auth/setup-status", nil); code != 200 || out.(map[string]any)["needs_setup"] != true {
		t.Fatalf("setup-status: %d %v", code, out)
	}
	// First-run setup through the gateway needs the token jarvisd wrote (AD2).
	setup := map[string]any{"email": "root@example.com", "password": "password1"}
	if code, _ := call(t, "POST", base+"/api/auth/setup", setup); code != 401 {
		t.Fatalf("setup without the token: %d", code)
	}
	var tok []byte
	for range 400 { // auth's Start runs once the listeners are bound
		if b, err := os.ReadFile(authmod.SetupTokenPath(cfg.Home)); err == nil {
			tok = b
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if code, out := call(t, "POST", base+"/api/auth/setup", setup, authmod.SetupTokenHeader, strings.TrimSpace(string(tok))); code != 201 {
		t.Fatalf("setup: %d %v", code, out)
	}
	code, out := call(t, "POST", base+"/api/auth/login", map[string]any{"email": "root@example.com", "password": "password1"})
	if code != 200 {
		t.Fatalf("login: %d %v", code, out)
	}
	login := out.(map[string]any)
	root := []string{"Authorization", "Bearer " + login["access_token"].(string)}

	code, out = call(t, "GET", base+"/api/admin/users", nil, root...)
	users, _ := out.([]any)
	if code != 200 || len(users) != 1 || users[0].(map[string]any)["email"] != "root@example.com" {
		t.Fatalf("users: %d %v", code, out)
	}
	if code, out := call(t, "GET", base+"/api/auth/me", nil, root...); code != 200 || out.(map[string]any)["email"] != "root@example.com" {
		t.Fatalf("me: %d %v", code, out)
	}
	if code, _ := call(t, "GET", base+"/api/admin/users", nil); code != 401 {
		t.Fatalf("anonymous users: %d", code)
	}

	code, out = call(t, "POST", base+"/api/auth/refresh", map[string]any{"refresh_token": login["refresh_token"]})
	if code != 200 {
		t.Fatalf("refresh: %d %v", code, out)
	}
	// Arg-less JSON POST (I9) reaches the module as {} and issues a temp password.
	uid := users[0].(map[string]any)["id"]
	req, _ := http.NewRequest("POST", base+"/api/admin/users/"+jsonNum(uid)+"/temp-password", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(root[0], root[1])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("temp-password with an empty JSON body: %d", resp.StatusCode)
	}
}

func jsonNum(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
