package display

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Spotify Connect: go-librespot (https://github.com/devgianlu/go-librespot) runs as a supervised child like
// the other receivers and shows up as a speaker in the Spotify app. Its REST API listens on loopback only;
// the engine polls it for what is playing and relays the shell's transport buttons to it. Spotify Premium is
// required and the receiver is unofficial, so it is opt-in (spotify_connect) and its failures never stop voice.

// SpotifyStatus is what the shell shows: the current track and the transport state. The Spotify cover art is
// served by the engine (the display browser cannot reach the internet), so the shell only needs HasCover.
type SpotifyStatus struct {
	Active        bool     `json:"active"`
	Paused        bool     `json:"paused"`
	Title         string   `json:"title"`
	Artists       []string `json:"artists"`
	Album         string   `json:"album"`
	HasCover      bool     `json:"has_cover"`
	PositionMS    int64    `json:"position_ms"`
	DurationMS    int64    `json:"duration_ms"`
	Volume        int      `json:"volume"`
	VolumeSteps   int      `json:"volume_steps"`
	Shuffle       bool     `json:"shuffle"`
	RepeatContext bool     `json:"repeat_context"`
	RepeatTrack   bool     `json:"repeat_track"`
	UpdatedAt     int64    `json:"updated_at"` // unix ms of the reading, so the shell can advance the position between polls
}

// spotifyReply is the part of go-librespot's GET /status the display uses.
type spotifyReply struct {
	Stopped       bool `json:"stopped"`
	Paused        bool `json:"paused"`
	Buffering     bool `json:"buffering"`
	Volume        int  `json:"volume"`
	VolumeSteps   int  `json:"volume_steps"`
	RepeatContext bool `json:"repeat_context"`
	RepeatTrack   bool `json:"repeat_track"`
	Shuffle       bool `json:"shuffle_context"`
	Track         *struct {
		Name       string   `json:"name"`
		Artists    []string `json:"artist_names"`
		Album      string   `json:"album_name"`
		CoverURL   string   `json:"album_cover_url"`
		PositionMS int64    `json:"position"`
		DurationMS int64    `json:"duration"`
	} `json:"track"`
}

// writeSpotifyConfig writes go-librespot's config.yml: a speaker named like the other receivers, joined from
// the Spotify app (zeroconf, remembered after the first connection), PulseAudio output, and the API on loopback.
func writeSpotifyConfig(dir, name string, port int) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	quoted, err := json.Marshal(name) // a JSON string is a valid YAML double-quoted scalar
	if err != nil {
		return err
	}
	config := "device_name: " + string(quoted) + "\ndevice_type: speaker\naudio_backend: pulseaudio\nzeroconf_enabled: true\n" +
		"credentials:\n  type: zeroconf\n  zeroconf:\n    persist_credentials: true\n" +
		"server:\n  enabled: true\n  address: 127.0.0.1\n  port: " + strconv.Itoa(port) + "\n"
	return os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0600)
}

// StartSpotify starts the receiver when the configuration asks for it. Callers hold e.mu, as for StartReceiver.
func (e *Engine) StartSpotify(on bool) {
	if !on || e.receivers["spotify"] != nil {
		e.StartReceiver("spotify", on, "go-librespot")
		return
	}
	// shortcut: a free port is picked by binding and releasing it, so another process could take it first;
	// the receiver then fails and shows as failed, and the next apply picks again.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.receiverStatus["spotify"] = "unavailable"
		return
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	dir := filepath.Join(e.root, "spotify")
	if err := writeSpotifyConfig(dir, e.delivery.Configuration.ReceiverName, port); err != nil {
		e.receiverStatus["spotify"] = "unavailable"
		return
	}
	e.spotifyAddr = "127.0.0.1:" + strconv.Itoa(port)
	e.StartReceiver("spotify", true, "go-librespot", "-config_dir", dir)
	if e.receivers["spotify"] != nil && !e.spotifyPolling {
		e.spotifyPolling = true
		go e.watchSpotify()
	}
}

func (e *Engine) watchSpotify() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-tick.C:
			e.pollSpotify()
		}
	}
}

// spotifyCall sends one request to the receiver's loopback API.
func (e *Engine) spotifyCall(method, path string, body any) (*http.Response, error) {
	e.mu.Lock()
	addr := e.spotifyAddr
	e.mu.Unlock()
	if addr == "" {
		return nil, fmt.Errorf("spotify receiver is not running")
	}
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	}
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, reader)
	if err != nil {
		cancel()
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error { defer c.cancel(); return c.ReadCloser.Close() }

// pollSpotify reads the receiver's status once. No session (204) or an unreachable receiver is "inactive".
func (e *Engine) pollSpotify() {
	status, cover := SpotifyStatus{}, ""
	if resp, err := e.spotifyCall("GET", "/status", nil); err == nil {
		var r spotifyReply
		if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r) == nil && r.Track != nil && !r.Stopped {
			cover = spotifyCoverURL(r.Track.CoverURL)
			status = SpotifyStatus{Active: true, Paused: r.Paused, Title: r.Track.Name, Artists: r.Track.Artists, Album: r.Track.Album, HasCover: cover != "",
				PositionMS: r.Track.PositionMS, DurationMS: r.Track.DurationMS, Volume: r.Volume, VolumeSteps: r.VolumeSteps,
				Shuffle: r.Shuffle, RepeatContext: r.RepeatContext, RepeatTrack: r.RepeatTrack, UpdatedAt: time.Now().UnixMilli()}
		}
		resp.Body.Close()
	}
	e.mu.Lock()
	e.spotify, e.spotifyCover = status, cover
	e.mu.Unlock()
}

// spotifyCoverURL returns raw only when it is an https URL on Spotify's image host.
func spotifyCoverURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "i.scdn.co" || u.User != nil || u.Port() != "" {
		return ""
	}
	return u.String()
}

// spotifyActions maps the shell's transport buttons to the receiver's API.
var spotifyActions = map[string]struct{ path string }{
	"playpause": {"/player/playpause"}, "pause": {"/player/pause"}, "resume": {"/player/resume"},
	"next": {"/player/next"}, "prev": {"/player/prev"}, "seek": {"/player/seek"}, "volume": {"/player/volume"},
}

// handleSpotify serves POST /api/spotify, a transport action.
func (e *Engine) handleSpotify(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/spotify":
		var body struct {
			Action   string `json:"action"`
			Position *int64 `json:"position_ms"`
			Volume   *int   `json:"volume"`
			Relative bool   `json:"relative"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
			http.Error(w, "Invalid Spotify request", 400)
			return true
		}
		action, ok := spotifyActions[body.Action]
		if !ok {
			http.Error(w, "Unknown Spotify action", 400)
			return true
		}
		var payload any
		switch body.Action {
		case "seek":
			if body.Position == nil {
				http.Error(w, "Seek needs position_ms", 400)
				return true
			}
			payload = map[string]any{"position": *body.Position, "relative": body.Relative}
		case "volume":
			if body.Volume == nil {
				http.Error(w, "Volume needs volume", 400)
				return true
			}
			payload = map[string]any{"volume": *body.Volume, "relative": body.Relative}
		}
		resp, err := e.spotifyCall("POST", action.path, payload)
		if err != nil {
			http.Error(w, "Spotify receiver unavailable", 502)
			return true
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			http.Error(w, "Spotify receiver refused the request", 502)
			return true
		}
		go e.pollSpotify() // show the change at once instead of at the next tick
		WriteJSON(w, map[string]any{"success": true})
		return true
	}
	return false
}

// duckSpotify pauses Spotify while the user talks to Jarvis and resumes it afterwards, but leaves it
// paused if the user had paused it. Like DuckMedia it must run outside the microphone's callback.
func (e *Engine) duckSpotify(voiceActive bool) {
	e.mu.Lock()
	playing := e.spotify.Active && !e.spotify.Paused
	was := e.spotifyAutoPaused
	e.mu.Unlock()
	switch {
	case voiceActive && playing && !was:
		if resp, err := e.spotifyCall("POST", "/player/pause", nil); err == nil {
			resp.Body.Close()
			e.mu.Lock()
			e.spotifyAutoPaused = true
			e.mu.Unlock()
		}
	case !voiceActive && was:
		e.mu.Lock()
		e.spotifyAutoPaused = false
		e.mu.Unlock()
		if resp, err := e.spotifyCall("POST", "/player/resume", nil); err == nil {
			resp.Body.Close()
		}
	}
}

// serveSpotifyCover serves the current track's cover art. An <img> cannot send the shell's header, so this sits
// outside /api and ServeHTTP checks the session cookie first. Only Spotify's image host is ever fetched.
func (e *Engine) serveSpotifyCover(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	cover := e.spotifyCover
	e.mu.Unlock()
	if cover == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", cover, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		http.NotFound(w, r)
		return
	}
	kind := resp.Header.Get("Content-Type")
	if kind != "image/jpeg" && kind != "image/png" && kind != "image/webp" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 2<<20))
	return
}
