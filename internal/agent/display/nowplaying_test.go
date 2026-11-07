package display

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/playback"
)

// radio is a playback source with a cover; controls records what the view sent.
type radio struct {
	id       string
	st       playback.State
	cover    []byte
	controls []string
}

func (r *radio) ID() string   { return r.id }
func (r *radio) Name() string { return "Radio" }
func (r *radio) Status(context.Context) (*playback.State, error) {
	c := r.st
	return &c, nil
}
func (r *radio) Control(_ context.Context, action string, args map[string]any) (*playback.State, error) {
	r.controls = append(r.controls, action)
	if action == "pause" {
		r.st.State = "paused"
	}
	if v, ok := args["volume"].(int); ok {
		r.st.Volume = &v
	}
	c := r.st
	return &c, nil
}
func (r *radio) Artwork(context.Context) ([]byte, string, error) { return r.cover, "image/jpeg", nil }

func withPlayback(t *testing.T, e *Engine, src playback.Source) *playback.Hub {
	t.Helper()
	hub := &playback.Hub{Sources: func() []playback.Source { return []playback.Source{src} }}
	hub.Tick(context.Background())
	e.SetPlayback(hub)
	return hub
}

func stateNowPlaying(t *testing.T, e *Engine) map[string]any {
	t.Helper()
	code, body := shell{method: "GET", path: "/api/state"}.do(t, e)
	if code != 200 {
		t.Fatalf("state = %d %s", code, body)
	}
	var s struct {
		NowPlaying map[string]any `json:"now_playing"`
	}
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	return s.NowPlaying
}

func TestNowPlayingInState(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	if stateNowPlaying(t, e) != nil {
		t.Fatal("now playing without a hub")
	}
	withPlayback(t, e, &radio{id: "pkg:radio", st: playback.State{State: "playing", MediaTitle: "Jazz FM", Actions: []string{"pause"}}})
	n := stateNowPlaying(t, e)
	if n == nil || n["source"] != "pkg:radio" || n["state"].(map[string]any)["media_title"] != "Jazz FM" {
		t.Fatalf("now_playing = %v", n)
	}
	// The display's own player is already on the screen: no now-playing view for it.
	withPlayback(t, e, &radio{id: "display", st: playback.State{State: "playing"}})
	if n := stateNowPlaying(t, e); n != nil {
		t.Fatalf("now_playing for the display's own player: %v", n)
	}
}

func TestNowPlayingControls(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	if code, _ := (shell{path: "/api/now-playing", body: `{"action":"pause"}`}).do(t, e); code != 409 {
		t.Fatalf("control with nothing playing = %d", code)
	}
	src := &radio{id: "pkg:radio", st: playback.State{State: "playing", Actions: []string{"pause", "volume"}}}
	withPlayback(t, e, src)

	code, body := shell{path: "/api/now-playing", body: `{"action":"volume","volume":150}`}.do(t, e)
	if code != 200 || !strings.Contains(body, `"volume":100`) {
		t.Fatalf("volume = %d %s", code, body)
	}
	code, body = shell{path: "/api/now-playing", body: `{"action":"pause"}`}.do(t, e)
	if code != 200 || !strings.Contains(body, `"state":"paused"`) {
		t.Fatalf("pause = %d %s", code, body)
	}
	for _, bad := range []string{`{"action":"status"}`, `{"action":"rewind"}`, `not json`} {
		if code, _ := (shell{path: "/api/now-playing", body: bad}).do(t, e); code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	// The API's usual guards hold: no session, another origin.
	if code, _ := (shell{path: "/api/now-playing", body: `{"action":"pause"}`, noSession: true}).do(t, e); code != 403 {
		t.Fatalf("no session = %d", code)
	}
	if code, _ := (shell{path: "/api/now-playing", body: `{"action":"pause"}`, origin: "http://evil.example"}).do(t, e); code != 403 {
		t.Fatalf("other origin = %d", code)
	}
	if len(src.controls) != 2 {
		t.Fatalf("controls = %v", src.controls)
	}
}

func TestNowPlayingCoverNeedsTheSession(t *testing.T) {
	e := startEngine(t, t.TempDir())
	enable(t, e, "1234")
	cover := shell{method: "GET", path: "/now-playing/cover"}
	if code, _ := cover.do(t, e); code != 403 {
		t.Fatalf("cover without a hub = %d", code)
	}
	src := &radio{id: "pkg:radio", st: playback.State{State: "playing", MediaTitle: "x"}, cover: []byte("\xff\xd8\xffcover")}
	withPlayback(t, e, src)
	if code, body := cover.do(t, e); code != 200 || body != "\xff\xd8\xffcover" {
		t.Fatalf("cover = %d %q", code, body)
	}
	if code, _ := (shell{method: "GET", path: "/now-playing/cover", noSession: true}).do(t, e); code != 403 {
		t.Fatalf("cover without the session = %d", code)
	}
	withPlayback(t, e, &radio{id: "pkg:plain", st: playback.State{State: "playing"}})
	if code, _ := cover.do(t, e); code != 404 {
		t.Fatalf("no artwork = %d, want 404", code)
	}
}
