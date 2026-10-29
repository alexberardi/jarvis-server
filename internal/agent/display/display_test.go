package display

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	shared "github.com/alexberardi/jarvis-server/internal/display"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func TestLocalShellTrustBoundaryAndExit(t *testing.T) {
	t.Setenv("DISPLAY", "")
	e, err := StartDisplay(context.Background(), t.TempDir(), fstest.MapFS{"display.html": {Data: []byte("display")}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	c, _ := shared.MergeConfiguration(nil)
	c.Enabled = true
	v, _ := shared.NewVerifier("1234")
	e.mu.Lock()
	e.delivery = shared.Delivery{Configuration: c, Verifier: v, Revision: shared.GetRevision(c, v)}
	e.mu.Unlock()
	do := func(path, origin, body string, secret bool) int {
		req, _ := http.NewRequest("POST", e.address+path, strings.NewReader(body))
		req.Header.Set("X-Jarvis-Display", "1")
		if secret {
			req.AddCookie(&http.Cookie{Name: "display_session", Value: e.secret})
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := do("/api/exit", "null", `{"pin":"1234"}`, true); code != 403 {
		t.Fatal("theme accessed exit", code)
	}
	if code := do("/api/exit", e.address, `{"pin":"1234"}`, false); code != 403 {
		t.Fatal("missing session accepted", code)
	}
	for i := 0; i < 5; i++ {
		if code := do("/api/exit", e.address, `{"pin":"0000"}`, true); code != 403 {
			t.Fatal(code)
		}
	}
	if code := do("/api/exit", e.address, `{"pin":"1234"}`, true); code != 429 {
		t.Fatal("rate limit", code)
	}
	e.mu.Lock()
	e.blockedUntil = time.Time{}
	e.mediaKind = "website"
	e.state = "speaking"
	e.mu.Unlock()
	code := do("/api/exit", e.address, `{"pin":"1234"}`, true)
	e.mu.Lock()
	exited := e.exited && e.mediaKind == "" && e.ctx.Err() == nil && e.state == "speaking"
	e.mu.Unlock()
	if code != 200 || !exited {
		t.Fatal("exit failed", code)
	}
	req, _ := http.NewRequest("GET", e.address+"/api/state", nil)
	req.AddCookie(&http.Cookie{Name: "display_session", Value: e.secret})
	req.Header.Set("X-Jarvis-Display", "1")
	r, _ := http.DefaultClient.Do(req)
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if strings.Contains(string(b), "verifier") || strings.Contains(string(b), v) {
		t.Fatal("secret exposed")
	}
}
func TestReconnectCachingAndFailedApply(t *testing.T) {
	t.Setenv("DISPLAY", "")
	root := t.TempDir()
	e, err := StartDisplay(context.Background(), root, fstest.MapFS{"display.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	c, _ := shared.MergeConfiguration(map[string]any{"idle_seconds": 77})
	d := shared.Delivery{Configuration: c}
	d.Revision = shared.GetRevision(c, "")
	acked := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "n:k" {
			t.Error("missing node authentication")
		}
		if r.URL.Path == "/api/v0/node/display/ack" {
			acked++
			w.Write([]byte(`{}`))
			return
		}
		WriteJSON(w, d)
	}))
	defer server.Close()
	cc := node.NewCC(server.URL, node.Creds{NodeID: "n", NodeKey: "k"})
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	if acked != 2 {
		t.Fatal("reconnect did not acknowledge")
	}
	p := filepath.Join(root, "display", "configuration.json")
	b, _ := os.ReadFile(p)
	var cache shared.Delivery
	_ = json.Unmarshal(b, &cache)
	if cache.Configuration.IdleSeconds != 77 {
		t.Fatal(cache)
	}
	restored, err := StartDisplay(context.Background(), root, fstest.MapFS{"display.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.delivery.Revision != d.Revision {
		t.Fatal("offline cache was lost")
	}
	d.Configuration.Photos = []string{strings.Repeat("a", 64)}
	d.Revision = shared.GetRevision(d.Configuration, "")
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	if e.delivery.Configuration.IdleSeconds != 77 || len(e.delivery.Configuration.Photos) != 0 {
		t.Fatal("failed apply replaced known-good configuration")
	}
}
func TestMediaValidation(t *testing.T) {
	t.Setenv("DISPLAY", "")
	e, err := StartDisplay(context.Background(), t.TempDir(), fstest.MapFS{"display.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.mu.Lock()
	e.delivery.Configuration.Enabled = true
	e.delivery.Configuration.Destination = "local"
	e.mu.Unlock()
	for _, r := range []MediaRequest{{Action: "play", URL: "file:///etc/passwd"}, {Action: "play", URL: "/etc/passwd"}, {Action: "play", URL: "javascript:alert(1)"}, {Action: "seek", Seconds: -1}, {Action: "shell"}} {
		if _, err := e.ControlMedia(context.Background(), r); err == nil {
			t.Fatal("unsafe media request accepted", r)
		}
	}
}

func TestVoicePausesOnlyPlayingMedia(t *testing.T) {
	t.Setenv("DISPLAY", "")
	root, err := os.MkdirTemp("", "jd")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	e, err := StartDisplay(context.Background(), root, fstest.MapFS{"display.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	socket := filepath.Join(root, "display", "mpv.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var paused atomic.Bool
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Command []any `json:"command"`
				}
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				var result any
				if request.Command[0] == "get_property" {
					result = paused.Load()
				} else {
					paused.Store(request.Command[2].(bool))
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"error": "success", "request_id": 1, "data": result})
			}()
		}
	}()
	e.mu.Lock()
	e.state = "listening"
	e.mu.Unlock()
	e.DuckMedia()
	if !paused.Load() {
		t.Fatal("voice did not pause media")
	}
	e.mu.Lock()
	e.state = "speaking"
	e.mu.Unlock()
	e.DuckMedia()
	e.mu.Lock()
	e.state = "idle"
	e.mu.Unlock()
	e.DuckMedia()
	if paused.Load() {
		t.Fatal("voice did not resume media")
	}
	paused.Store(true)
	e.mu.Lock()
	e.state = "listening"
	e.mu.Unlock()
	e.DuckMedia()
	e.mu.Lock()
	e.state = "idle"
	e.mu.Unlock()
	e.DuckMedia()
	if !paused.Load() {
		t.Fatal("voice resumed manually paused media")
	}
}

func TestVoiceLevelIsStreamedWithoutCredentials(t *testing.T) {
	t.Setenv("DISPLAY", "")
	e, err := StartDisplay(context.Background(), t.TempDir(), fstest.MapFS{"display.html": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	e.SetState(voice.CueSpeaking, 0)
	e.Level(.36)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", e.address+"/api/voice-stream", nil)
	req.AddCookie(&http.Cookie{Name: "display_session", Value: e.secret})
	req.Header.Set("X-Jarvis-Display", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"voice":"speaking"`) || !strings.Contains(line, `"level":0.36`) || strings.Contains(line, "verifier") {
		t.Fatal(line)
	}
}

func TestLocalBrowserDoesNotRequestLoginKeyring(t *testing.T) {
	e := &Engine{ctx: context.Background(), root: t.TempDir(), address: "http://127.0.0.1:1234", secret: "test-only"}
	args := strings.Join(e.GetBrowserCommand().Args, " ")
	if !strings.Contains(args, "--hide-crash-restore-bubble") {
		t.Fatal("browser would show restore popup after restart")
	}
	if !strings.Contains(args, "--password-store=basic") {
		t.Fatal("local browser would request a login keyring")
	}
	if !strings.Contains(args, "--proxy-bypass-list=<-loopback>;127.0.0.1:1234") {
		t.Fatal("local browser lost network isolation")
	}
}
