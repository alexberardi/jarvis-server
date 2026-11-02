package display

import (
	"bufio"
	"context"
	"encoding/json"
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

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	displayconfig "github.com/alexberardi/jarvis-server/internal/display"
)

// startEngine starts a display engine over a throwaway root with no graphical session (so no
// browser or receiver is launched), stopped when the test ends.
func startEngine(t *testing.T, root string) *Engine {
	t.Helper()
	t.Setenv("DISPLAY", "")
	e, err := StartDisplay(context.Background(), root, fstest.MapFS{"display.html": {Data: []byte("display")}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// enable gives e an enabled configuration with exit PIN pin; it returns the PIN's verifier.
func enable(t *testing.T, e *Engine, pin string) string {
	t.Helper()
	c, err := displayconfig.MergeConfiguration(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Enabled = true
	v, err := displayconfig.NewVerifier(pin)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.delivery = displayconfig.Delivery{Configuration: c, Verifier: v, Revision: displayconfig.GetRevision(c, v)}
	e.mu.Unlock()
	return v
}

// shell is one request to the engine's local API, as the trusted shell makes it (its session
// cookie and origin) unless the options say otherwise.
type shell struct {
	origin     string // "" sends the engine's own; "-" sends none
	noSession  bool
	method     string
	path, body string
}

func (s shell) do(t *testing.T, e *Engine) (int, string) {
	t.Helper()
	method := s.method
	if method == "" {
		method = "POST"
	}
	req, err := http.NewRequest(method, e.address+s.path, strings.NewReader(s.body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Jarvis-Display", "1")
	if !s.noSession {
		req.AddCookie(&http.Cookie{Name: "display_session", Value: e.secret})
	}
	switch s.origin {
	case "":
		req.Header.Set("Origin", e.address)
	case "-":
	default:
		req.Header.Set("Origin", s.origin)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, string(b)
}

func exitWith(pin string) shell { return shell{path: "/api/exit", body: `{"pin":"` + pin + `"}`} }

func TestThemeOriginCannotExit(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	s := exitWith("1234")
	s.origin = "null" // the sandboxed theme frame
	if code, _ := s.do(t, e); code != 403 {
		t.Fatalf("theme exit = %d", code)
	}
}

func TestExitNeedsTheSessionCookie(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	s := exitWith("1234")
	s.noSession = true
	if code, _ := s.do(t, e); code != 403 {
		t.Fatalf("exit without the session = %d", code)
	}
}

func TestFiveWrongPINsBlockForAMinute(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	for i := 0; i < 5; i++ {
		if code, _ := exitWith("0000").do(t, e); code != 403 {
			t.Fatalf("wrong PIN %d = %d", i+1, code)
		}
	}
	if code, _ := exitWith("1234").do(t, e); code != 429 {
		t.Fatalf("the right PIN while blocked = %d", code)
	}
	e.mu.Lock()
	blocked := time.Until(e.blockedUntil)
	e.mu.Unlock()
	if blocked < 50*time.Second || blocked > time.Minute {
		t.Fatalf("blocked for %v", blocked)
	}
}

// The right PIN closes the display's media but leaves the agent (its context) and voice running.
func TestExitStopsMediaButKeepsVoice(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	e.mu.Lock()
	e.mediaKind, e.state = "website", "speaking"
	e.mu.Unlock()
	if code, body := exitWith("1234").do(t, e); code != 200 {
		t.Fatalf("exit = %d %s", code, body)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.exited || e.mediaKind != "" || e.ctx.Err() != nil || e.state != "speaking" {
		t.Fatalf("after exit: exited %v, media %q, context %v, voice %q", e.exited, e.mediaKind, e.ctx.Err(), e.state)
	}
}

func TestStateNeverCarriesTheVerifier(t *testing.T) {
	e := startEngine(t, t.TempDir())
	v := enable(t, e, "1234")
	code, body := shell{method: "GET", path: "/api/state", origin: "-"}.do(t, e)
	if code != 200 {
		t.Fatalf("state = %d", code)
	}
	if strings.Contains(body, "verifier") || strings.Contains(body, v) {
		t.Fatal("state exposes the PIN verifier")
	}
}

// fakeServer serves delivery d as jarvisd's /node/display and counts acknowledgements.
func fakeServer(t *testing.T, d *displayconfig.Delivery) (*node.CC, *atomic.Int32) {
	t.Helper()
	var acked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "n:k" {
			t.Error("missing node authentication")
		}
		if r.URL.Path == "/api/v0/node/display/ack" {
			acked.Add(1)
			WriteJSON(w, map[string]any{})
			return
		}
		WriteJSON(w, d)
	}))
	t.Cleanup(srv.Close)
	return node.NewCC(srv.URL, node.Creds{NodeID: "n", NodeKey: "k"}), &acked
}

func delivery(t *testing.T, overrides map[string]any) displayconfig.Delivery {
	t.Helper()
	c, err := displayconfig.MergeConfiguration(overrides)
	if err != nil {
		t.Fatal(err)
	}
	return displayconfig.Delivery{Configuration: c, Revision: displayconfig.GetRevision(c, "")}
}

// Every apply is acknowledged, a reconnect's too, so the admin shows the applied revision.
func TestSynchroniseAcknowledgesEveryApply(t *testing.T) {
	e := startEngine(t, t.TempDir())
	d := delivery(t, map[string]any{"idle_seconds": 77})
	cc, acked := fakeServer(t, &d)
	for i := 0; i < 2; i++ {
		if err := e.Synchronise(context.Background(), cc); err != nil {
			t.Fatal(err)
		}
	}
	if acked.Load() != 2 {
		t.Fatalf("acknowledged %d of 2 applies", acked.Load())
	}
}

// The applied configuration is cached, and a later start uses it with no server.
func TestCacheRestoresConfigurationOffline(t *testing.T) {
	root := t.TempDir()
	e := startEngine(t, root)
	d := delivery(t, map[string]any{"idle_seconds": 77})
	cc, _ := fakeServer(t, &d)
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "display", "configuration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cache displayconfig.Delivery
	if err := json.Unmarshal(b, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.Configuration.IdleSeconds != 77 {
		t.Fatalf("cached %+v", cache.Configuration)
	}
	e.Close()
	restored := startEngine(t, root)
	if restored.delivery.Revision != d.Revision {
		t.Fatal("the offline cache was lost")
	}
}

// A delivery that can't be applied (a photo that isn't there) keeps the known-good one.
func TestFailedApplyKeepsKnownGoodConfiguration(t *testing.T) {
	e := startEngine(t, t.TempDir())
	d := delivery(t, map[string]any{"idle_seconds": 77})
	cc, _ := fakeServer(t, &d)
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	d.Configuration.Photos = []string{strings.Repeat("a", 64)}
	d.Revision = displayconfig.GetRevision(d.Configuration, "")
	if err := e.Synchronise(context.Background(), cc); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.delivery.Configuration.IdleSeconds != 77 || len(e.delivery.Configuration.Photos) != 0 {
		t.Fatal("a failed apply replaced the known-good configuration")
	}
}

func TestLocalPlaybackRefusesUnsafeRequests(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	for name, r := range map[string]MediaRequest{
		"file URL":       {Action: "play", URL: "file:///etc/passwd"},
		"path outside":   {Action: "play", URL: "/etc/passwd"},
		"script URL":     {Action: "play", URL: "javascript:alert(1)"},
		"negative seek":  {Action: "seek", Seconds: -1},
		"unknown action": {Action: "shell"},
	} {
		t.Run(name, func(t *testing.T) {
			r.Destination = "local"
			if _, err := e.ControlMedia(context.Background(), r); err == nil {
				t.Fatalf("accepted %+v", r)
			}
		})
	}
}

// fakeMPV answers mpv's IPC on the engine's socket: get_property reads, set_property sets,
// "pause" only.
func fakeMPV(t *testing.T, root string) *atomic.Bool {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(root, "display", "mpv.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
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
				} else if v, ok := request.Command[2].(bool); ok {
					paused.Store(v)
				}
				_ = json.NewEncoder(conn).Encode(map[string]any{"error": "success", "request_id": 1, "data": result})
			}()
		}
	}()
	return &paused
}

// shortRoot is a root whose mpv socket path fits sun_path (t.TempDir can be too long).
func shortRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "jd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func voiceState(e *Engine, s string) {
	e.mu.Lock()
	e.state = s
	e.mu.Unlock()
	e.DuckMedia()
}

func TestVoicePausesAndResumesPlayingMedia(t *testing.T) {
	root := shortRoot(t)
	e := startEngine(t, root)
	paused := fakeMPV(t, root)
	voiceState(e, "listening")
	if !paused.Load() {
		t.Fatal("voice did not pause media")
	}
	voiceState(e, "speaking")
	voiceState(e, "idle")
	if paused.Load() {
		t.Fatal("voice did not resume media")
	}
}

func TestVoiceLeavesManuallyPausedMediaPaused(t *testing.T) {
	root := shortRoot(t)
	e := startEngine(t, root)
	paused := fakeMPV(t, root)
	paused.Store(true)
	voiceState(e, "listening")
	voiceState(e, "idle")
	if !paused.Load() {
		t.Fatal("voice resumed manually paused media")
	}
}

func TestVoiceLevelIsStreamedWithoutCredentials(t *testing.T) {
	e := startEngine(t, t.TempDir())
	e.SetState(voice.CueSpeaking, 0)
	e.Level(.36)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", e.address+"/api/voice-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
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
