// Package display serves the trusted local touch shell and supervises its media processes.
package display

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	shared "github.com/alexberardi/jarvis-server/internal/display"
)

type Engine struct {
	Host                    *host.Host
	returnWindow            string
	mu                      sync.Mutex
	syncMu                  sync.Mutex
	mediaMu                 sync.Mutex
	ducked                  []string
	root                    string
	delivery                shared.Delivery
	cc                      *node.CC
	ui                      fs.FS
	address, secret         string
	state                   string
	level                   float32
	lastInteraction         time.Time
	browser                 *exec.Cmd
	media                   *exec.Cmd
	mediaKind               string
	mediaPaused, autoPaused bool
	exited                  bool
	failures                int
	blockedUntil            time.Time
	receivers               map[string]*exec.Cmd
	receiverStatus          map[string]string
	theme                   *shared.Theme
	statusError             string
	ctx                     context.Context
	cancel                  context.CancelFunc
}

func StartDisplay(ctx context.Context, root string, ui fs.FS) (*Engine, error) {
	if ui == nil {
		return nil, errors.New("display assets were not built")
	}
	ctx, cancel := context.WithCancel(ctx)
	e := &Engine{root: filepath.Join(root, "display"), ui: ui, state: "idle", lastInteraction: time.Now(), receivers: map[string]*exec.Cmd{}, receiverStatus: map[string]string{}, ctx: ctx, cancel: cancel}
	if err := os.MkdirAll(e.root, 0700); err != nil {
		cancel()
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(e.root, "configuration.json")); err == nil {
		if err = json.Unmarshal(b, &e.delivery); err != nil {
			cancel()
			return nil, errors.New("invalid display cache")
		}
		if e.delivery.Revision != shared.GetRevision(e.delivery.Configuration, e.delivery.Verifier) {
			cancel()
			return nil, errors.New("display cache revision mismatch")
		}
		e.LoadTheme()
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		cancel()
		return nil, err
	}
	e.secret = hex.EncodeToString(secret[:])
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		cancel()
		return nil, err
	}
	e.address = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: http.HandlerFunc(e.ServeHTTP), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		e.mu.Lock()
		defer e.mu.Unlock()
		e.StopBrowser()
		e.StopMedia()
		e.StopReceivers()
	}()
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				e.mu.Lock()
				e.EnsureProcesses()
				e.mu.Unlock()
			}
		}
	}()
	return e, nil
}
func (e *Engine) Close()      { e.cancel() }
func (e *Engine) URL() string { return e.address + "/launch?token=" + e.secret }
func (e *Engine) LoadTheme() {
	e.theme = nil
	if e.delivery.Configuration.Theme == "builtin" || e.delivery.Configuration.Theme == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(e.root, e.delivery.Configuration.Theme))
	if err != nil {
		return
	}
	t, err := shared.ReadTheme(b)
	if err == nil {
		e.theme = &t
	}
}
func (e *Engine) Synchronise(ctx context.Context, cc *node.CC) error {
	e.syncMu.Lock()
	defer e.syncMu.Unlock()
	e.mu.Lock()
	e.cc = cc
	e.mu.Unlock()
	var d shared.Delivery
	if err := cc.Do(ctx, "GET", "/api/v0/node/display", nil, &d); err != nil {
		return err
	}
	e.mu.Lock()
	same := d.Revision == e.delivery.Revision
	e.mu.Unlock()
	applyError := ""
	if !same {
		if d.Revision != shared.GetRevision(d.Configuration, d.Verifier) {
			return errors.New("invalid display revision")
		}
		b, _ := json.Marshal(d.Configuration)
		var l map[string]any
		_ = json.Unmarshal(b, &l)
		if _, err := shared.MergeConfiguration(l); err != nil {
			return err
		}
		if d.Configuration.Enabled && d.Verifier == "" {
			return errors.New("exit PIN required")
		}
		ids := append([]string{}, d.Configuration.Photos...)
		if d.Configuration.Theme != "builtin" {
			ids = append(ids, d.Configuration.Theme)
		}
		for _, id := range ids {
			filename := filepath.Join(e.root, id)
			if b, err := os.ReadFile(filename); err == nil && shared.Digest(b) == id {
				continue
			}
			req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(cc.BaseURL, "/")+"/api/v0/node/display/assets/"+id, nil)
			if err != nil {
				return err
			}
			req.Header.Set("X-API-Key", cc.Creds.APIKey())
			resp, err := cc.HTTP.Do(req)
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(io.LimitReader(resp.Body, shared.MaxAssetBytes+1))
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || len(raw) > shared.MaxAssetBytes || shared.Digest(raw) != id {
				applyError = "Display asset download failed"
				break
			}
			if id == d.Configuration.Theme {
				if _, err = shared.ReadTheme(raw); err != nil {
					applyError = "Theme validation failed"
					break
				}
			}
			if err = shared.WriteCache(filename, raw); err != nil {
				applyError = "Display asset cache failed"
				break
			}
		}
		if applyError == "" {
			raw, _ := json.Marshal(d)
			if err := shared.WriteCache(filepath.Join(e.root, "configuration.json"), raw); err != nil {
				applyError = "Display configuration cache failed"
			} else {
				e.mu.Lock()
				e.StopReceivers()
				e.StopBrowser()
				e.delivery = d
				e.exited = false
				e.LoadTheme()
				e.EnsureProcesses()
				e.mu.Unlock()
			}
		}
	}
	return cc.Do(ctx, "POST", "/api/v0/node/display/ack", map[string]any{"revision": d.Revision, "error": applyError}, nil)
}
func (e *Engine) Watch(ctx context.Context, cc *node.CC) {
	_ = e.Synchronise(ctx, cc)
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = e.Synchronise(ctx, cc)
		}
	}
}
func (e *Engine) Capabilities() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{"display": true, "display_enabled": e.delivery.Configuration.Enabled, "display_revision": e.delivery.Revision, "display_status": e.statusError, "display_playercast": e.receiverStatus["playercast"], "display_cast": e.receiverStatus["cast"]}
}
func (e *Engine) SetState(c voice.Cue, _ time.Duration) {
	e.mu.Lock()
	e.state = c.String()
	if c != voice.CueIdle && c != voice.CueFollowUp {
		e.lastInteraction = time.Now()
	}
	e.mu.Unlock()
	// Media IPC must never delay the microphone's state callback.
	go e.DuckMedia()
}
func (e *Engine) Level(v float32) { e.mu.Lock(); e.level = max(0, min(1, v)); e.mu.Unlock() }
func (e *Engine) StreamVoice(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unavailable", 500)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.mu.Lock()
			b, _ := json.Marshal(map[string]any{"voice": e.state, "level": e.level})
			e.mu.Unlock()
			if _, err := w.Write(append(append([]byte("data: "), b...), []byte("\n\n")...)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
func (e *Engine) StopBrowser() {
	if e.browser != nil {
		StopProcess(e.browser)
		e.browser = nil
	}
}
func (e *Engine) StopMedia() {
	if e.media != nil {
		StopProcess(e.media)
		e.media = nil
	}
	e.mediaKind = ""
	e.autoPaused = false
	e.mediaPaused = false
}
func (e *Engine) StopReceivers() {
	for _, cmd := range e.receivers {
		StopProcess(cmd)
	}
	e.receivers = map[string]*exec.Cmd{}
	e.receiverStatus = map[string]string{}
}
func (e *Engine) GetBrowserCommand() *exec.Cmd {
	// The local-only profile has no website credentials and must start with auto-login.
	return exec.CommandContext(e.ctx, "chromium", "--password-store=basic", "--start-fullscreen", "--class=JarvisDisplay", "--no-first-run", "--no-default-browser-check", "--hide-crash-restore-bubble", "--user-data-dir="+filepath.Join(e.root, "browser"), "--proxy-server=http://127.0.0.1:9", "--proxy-bypass-list=<-loopback>;"+strings.TrimPrefix(e.address, "http://"), e.URL())
}
func (e *Engine) EnsureProcesses() {
	c := e.delivery.Configuration
	if !c.Enabled || e.exited {
		e.StopMedia()
		e.StopBrowser()
		e.StopReceivers()
		return
	}
	if os.Getenv("DISPLAY") == "" {
		e.statusError = "Waiting for Xfce login"
		return
	}
	if e.browser == nil {
		cmd := e.GetBrowserCommand()
		SetProcessGroup(cmd)
		if err := cmd.Start(); err != nil {
			e.statusError = "Chromium could not start"
		} else {
			e.statusError = ""
			e.browser = cmd
			go func() {
				_ = cmd.Wait()
				e.mu.Lock()
				if e.browser == cmd {
					e.browser = nil
					e.exited = true
				}
				e.mu.Unlock()
			}()
		}
	}
	e.StartReceiver("playercast", c.Playercast, "playercast", "--name", c.ReceiverName, "--player", "mpv", "--disable-cec")
	e.StartReceiver("cast", c.ExperimentalCast, "openchromecast", "--friendly-name", c.ReceiverName, "--player", "mpv", "--mpv", filepath.Join(e.root, "bin", "mpv"), "--no-tray")
}
func (e *Engine) StartReceiver(key string, on bool, program string, args ...string) {
	if !on {
		e.receiverStatus[key] = "disabled"
		return
	}
	if e.receivers[key] != nil {
		return
	}
	cmd := exec.CommandContext(e.ctx, program, args...)
	binary, _ := os.Executable()
	wrapperDir := filepath.Join(e.root, "bin")
	_ = os.MkdirAll(wrapperDir, 0700)
	wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(binary, "'", "'\"'\"'") + "' display-player \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "mpv"), []byte(wrapper), 0700); err != nil {
		e.receiverStatus[key] = "unavailable"
		return
	}
	cmd.Env = append(os.Environ(), "TMPDIR="+e.root, "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "JARVIS_DISPLAY_ROOT="+e.root)
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		e.receiverStatus[key] = "unavailable"
		return
	}
	e.receivers[key] = cmd
	e.receiverStatus[key] = "running"
	go func() {
		_ = cmd.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.receivers[key] == cmd {
			delete(e.receivers, key)
			e.receiverStatus[key] = "failed"
		}
	}()
}
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != strings.TrimPrefix(e.address, "http://") {
		http.Error(w, "Invalid host", 403)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/launch" {
		if r.URL.Query().Get("token") != e.secret {
			http.Error(w, "Unauthorised", 403)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "display_session", Value: e.secret, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/display.html", 303)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		cookie, err := r.Cookie("display_session")
		origin := r.Header.Get("Origin")
		if err != nil || cookie.Value != e.secret || r.Header.Get("X-Jarvis-Display") != "1" || (origin != "" && origin != e.address) {
			http.Error(w, "Unauthorised", 403)
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/api/voice-stream" {
			e.StreamVoice(w, r)
			return
		}
		e.HandleAPI(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/theme/") {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.theme == nil {
			http.NotFound(w, r)
			return
		}
		b, ok := e.theme.Files[strings.TrimPrefix(r.URL.Path, "/theme/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts")
		// An opaque sandbox origin needs CORS for its local static subresources only.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.ServeContent(w, r, filepath.Base(r.URL.Path), time.Time{}, strings.NewReader(string(b)))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/photo/") {
		id := strings.TrimPrefix(r.URL.Path, "/photo/")
		e.mu.Lock()
		allowed := false
		for _, p := range e.delivery.Configuration.Photos {
			allowed = allowed || id == p
		}
		e.mu.Unlock()
		if !allowed {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(e.root, id))
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	http.FileServer(http.FS(e.ui)).ServeHTTP(w, r)
}
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (e *Engine) HandleAPI(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	d := e.delivery
	cc := e.cc
	e.mu.Unlock()
	if r.URL.Path == "/api/state" && r.Method == "GET" {
		playback := e.GetPlaybackStatus()
		delete(playback, "socket")
		e.mu.Lock()
		theme := ""
		if e.theme != nil {
			theme = "/theme/" + e.theme.Manifest.Entry
		}
		state := map[string]any{"playback": playback, "configuration": d.Configuration, "revision": d.Revision, "voice": e.state, "voice_level": e.level, "last_interaction": e.lastInteraction.UnixMilli(), "media_active": e.media != nil, "media_kind": e.mediaKind, "paused": e.mediaPaused, "receivers": e.receiverStatus, "theme_url": theme, "status": e.statusError}
		WriteJSON(w, state)
		e.mu.Unlock()
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/exit" {
		e.mu.Lock()
		defer e.mu.Unlock()
		if time.Now().Before(e.blockedUntil) {
			w.WriteHeader(429)
			WriteJSON(w, map[string]any{"error": "Too many attempts. Try again in one minute."})
			return
		}
		var body struct {
			PIN string `json:"pin"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128)).Decode(&body)
		if err != nil || shared.CheckPIN(e.delivery.Verifier, body.PIN) != nil {
			e.failures++
			if e.failures >= 5 {
				e.blockedUntil = time.Now().Add(time.Minute)
				e.failures = 0
			}
			w.WriteHeader(403)
			WriteJSON(w, map[string]any{"error": "Incorrect PIN"})
			return
		}
		e.failures = 0
		e.exited = true
		e.StopMedia()
		e.StopReceivers()
		e.StopBrowser()
		WriteJSON(w, map[string]any{"success": true})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/recover" {
		e.mu.Lock()
		e.theme = nil
		e.mu.Unlock()
		WriteJSON(w, map[string]any{"success": true})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/media" {
		var body MediaRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
			http.Error(w, "Invalid media request", 400)
			return
		}
		out, err := e.ControlMedia(r.Context(), body)
		if err != nil {
			w.WriteHeader(400)
			WriteJSON(w, map[string]any{"error": err.Error(), "offer_local": body.Destination != "local"})
			return
		}
		WriteJSON(w, out)
		return
	}
	if cc == nil {
		http.Error(w, "Server offline", 503)
		return
	}
	path := ""
	if r.URL.Path == "/api/devices" && r.Method == "GET" {
		path = "/api/v0/node/display/devices"
	}
	if strings.HasPrefix(r.URL.Path, "/api/devices/") {
		suffix := strings.TrimPrefix(r.URL.Path, "/api/devices/")
		parts := strings.Split(suffix, "/")
		if len(parts) == 2 && len(parts[0]) < 128 && (parts[1] == "state" && r.Method == "GET" || parts[1] == "control" && r.Method == "POST") {
			path = "/api/v0/node/display/devices/" + suffix
		}
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	var body any
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
			http.Error(w, "Invalid action", 400)
			return
		}
	}
	var out any
	if err := cc.Do(r.Context(), r.Method, path, body, &out); err != nil {
		w.WriteHeader(502)
		WriteJSON(w, map[string]any{"error": err.Error()})
		return
	}
	WriteJSON(w, out)
}

var _ voice.Indicator = (*Engine)(nil)
