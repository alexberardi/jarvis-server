package display

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeSpotify stands in for go-librespot's loopback API: it serves the status given and records every call.
type fakeSpotify struct {
	mu     sync.Mutex
	status string // "" answers 204 (no session)
	calls  []string
	bodies []string
}

func (f *fakeSpotify) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == "GET" && r.URL.Path == "/status" {
		if f.status == "" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.status))
		return
	}
	buf := make([]byte, 256)
	n, _ := r.Body.Read(buf)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(buf[:n]))
}

func (f *fakeSpotify) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

const playingStatus = `{"stopped":false,"paused":false,"buffering":false,"volume":40,"volume_steps":100,"repeat_context":false,"repeat_track":false,"shuffle_context":true,
"track":{"name":"Hysteria","artist_names":["Muse"],"album_name":"Absolution","album_cover_url":"https://i.scdn.co/image/abc","position":61000,"duration":227000}}`

func startSpotifyEngine(t *testing.T, status string) (*Engine, *fakeSpotify) {
	t.Helper()
	e := startEngine(t, t.TempDir())
	f := &fakeSpotify{status: status}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	e.mu.Lock()
	e.spotifyAddr = strings.TrimPrefix(srv.URL, "http://")
	e.mu.Unlock()
	return e, f
}

func TestSpotifyConfigListensOnLoopbackAndUsesTheReceiverName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spotify")
	if err := writeSpotifyConfig(dir, `Lounge "Jarvis"`, 4567); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`device_name: "Lounge \"Jarvis\""`, "address: 127.0.0.1", "port: 4567", "type: zeroconf", "audio_backend: pulseaudio"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
	if info, _ := os.Stat(filepath.Join(dir, "config.yml")); info.Mode().Perm() != 0600 {
		t.Errorf("config mode %v", info.Mode().Perm())
	}
}

func TestSpotifyPollReadsTheTrackAndClearsWithoutASession(t *testing.T) {
	e, f := startSpotifyEngine(t, playingStatus)
	e.pollSpotify()
	e.mu.Lock()
	got := e.spotify
	e.mu.Unlock()
	if !got.Active || got.Paused || got.Title != "Hysteria" || len(got.Artists) != 1 || got.Artists[0] != "Muse" || got.Album != "Absolution" || !got.HasCover || got.PositionMS != 61000 || got.DurationMS != 227000 || got.Volume != 40 || !got.Shuffle {
		t.Fatalf("status %+v", got)
	}
	f.mu.Lock()
	f.status = ""
	f.mu.Unlock()
	e.pollSpotify()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.spotify.Active || e.spotify.Title != "" || e.spotifyCover != "" {
		t.Fatalf("status after the session ended: %+v", e.spotify)
	}
}

func TestSpotifyCoverMustBeOnSpotifysImageHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://i.scdn.co/image/abc":     "https://i.scdn.co/image/abc",
		"http://i.scdn.co/image/abc":      "",
		"https://evil.example/image/abc":  "",
		"https://i.scdn.co.evil.test/a":   "",
		"https://user@i.scdn.co/image/a":  "",
		"https://i.scdn.co:8443/image/ab": "",
		"":                                "",
	} {
		if got := spotifyCoverURL(raw); got != want {
			t.Errorf("spotifyCoverURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestSpotifyButtonsReachTheReceiver(t *testing.T) {
	e, f := startSpotifyEngine(t, playingStatus)
	enable(t, e, "1234")
	for body, want := range map[string]string{
		`{"action":"playpause"}`:                "POST /player/playpause",
		`{"action":"next"}`:                     "POST /player/next",
		`{"action":"prev"}`:                     "POST /player/prev",
		`{"action":"seek","position_ms":90000}`: "POST /player/seek",
		`{"action":"volume","volume":55}`:       "POST /player/volume",
	} {
		if code, out := (shell{path: "/api/spotify", body: body}).do(t, e); code != 200 {
			t.Fatalf("%s = %d %s", body, code, out)
		}
		if !f.called(want) {
			t.Errorf("%s did not make %q (calls %v)", body, want, f.calls)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(strings.Join(f.bodies, "|"), `"position":90000`) || !strings.Contains(strings.Join(f.bodies, "|"), `"volume":55`) {
		t.Errorf("bodies %v", f.bodies)
	}
}

func TestSpotifyButtonsRefuseBadRequests(t *testing.T) {
	e, f := startSpotifyEngine(t, playingStatus)
	enable(t, e, "1234")
	for name, s := range map[string]shell{
		"unknown action":        {path: "/api/spotify", body: `{"action":"stop"}`},
		"seek needs a position": {path: "/api/spotify", body: `{"action":"seek"}`},
		"volume needs a level":  {path: "/api/spotify", body: `{"action":"volume"}`},
		"not json":              {path: "/api/spotify", body: `nope`},
	} {
		if code, _ := s.do(t, e); code != 400 {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := (shell{path: "/api/spotify", body: `{"action":"next"}`, noSession: true}).do(t, e); code != 403 {
		t.Errorf("without the session cookie = %d, want 403", code)
	}
	if code, _ := (shell{path: "/api/spotify", body: `{"action":"next"}`, origin: "http://evil.example"}).do(t, e); code != 403 {
		t.Errorf("from another origin = %d, want 403", code)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Errorf("refused requests still reached the receiver: %v", f.calls)
	}
}

func TestSpotifyButtonsWithoutAReceiverAreABadGateway(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	if code, _ := (shell{path: "/api/spotify", body: `{"action":"next"}`}).do(t, e); code != 502 {
		t.Fatalf("no receiver = %d, want 502", code)
	}
}

func TestStateCarriesTheSpotifyTrack(t *testing.T) {
	e, _ := startSpotifyEngine(t, playingStatus)
	enable(t, e, "1234")
	e.pollSpotify()
	code, body := (shell{method: "GET", path: "/api/state"}).do(t, e)
	if code != 200 || !strings.Contains(body, `"title":"Hysteria"`) || !strings.Contains(body, `"has_cover":true`) {
		t.Fatalf("state = %d %s", code, body)
	}
	if strings.Contains(body, "i.scdn.co") {
		t.Fatalf("state leaks the cover URL: %s", body)
	}
}

func TestVoicePausesAndResumesSpotify(t *testing.T) {
	e, f := startSpotifyEngine(t, playingStatus)
	e.pollSpotify()
	voiceState(e, "listening")
	if !f.called("POST /player/pause") {
		t.Fatal("voice did not pause Spotify")
	}
	voiceState(e, "idle")
	if !f.called("POST /player/resume") {
		t.Fatal("voice did not resume Spotify")
	}
}

func TestVoiceLeavesManuallyPausedSpotifyPaused(t *testing.T) {
	e, f := startSpotifyEngine(t, strings.Replace(playingStatus, `"paused":false`, `"paused":true`, 1))
	e.pollSpotify()
	voiceState(e, "listening")
	voiceState(e, "idle")
	if f.called("POST /player/pause") || f.called("POST /player/resume") {
		t.Fatalf("voice touched a paused Spotify: %v", f.calls)
	}
}

func TestStoppingReceiversForgetsTheSpotifyTrack(t *testing.T) {
	e, _ := startSpotifyEngine(t, playingStatus)
	e.pollSpotify()
	e.mu.Lock()
	e.StopReceivers()
	got, addr := e.spotify, e.spotifyAddr
	e.mu.Unlock()
	if got.Active || addr != "" {
		t.Fatalf("after stop: %+v %q", got, addr)
	}
}

func TestSpotifyCoverNeedsTheSessionAndATrack(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	if code, _ := (shell{method: "GET", path: "/spotify/cover", noSession: true}).do(t, e); code != 403 {
		t.Errorf("without the session cookie = %d, want 403", code)
	}
	if code, _ := (shell{method: "GET", path: "/spotify/cover"}).do(t, e); code != 404 {
		t.Errorf("with nothing playing = %d, want 404", code)
	}
}
