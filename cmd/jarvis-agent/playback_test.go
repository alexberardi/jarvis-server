package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/playback"
)

func TestDisplayState(t *testing.T) {
	if displayState(map[string]any{"active": false}) != nil {
		t.Fatal("inactive display counted as playing")
	}
	if displayState(map[string]any{"active": true, "controllable": false}) != nil {
		t.Fatal("a player it can't control counted as playing")
	}
	st := displayState(map[string]any{"active": true, "controllable": true, "paused": true, "position": 12.5, "duration": 90.0})
	if st == nil || st.State != "paused" || *st.Position != 12.5 || *st.Duration != 90 || len(st.Actions) != 3 {
		t.Fatalf("state = %+v", st)
	}
	if st := displayState(map[string]any{"active": true, "controllable": true, "paused": false}); st.State != "playing" {
		t.Fatalf("state = %+v", st)
	}
}

func TestPlaybackReporterTiming(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	p := newPlaybackReporter(func() *node.CC { return nil }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.now = func() time.Time { return clock }
	if _, ok := p.due(); ok {
		t.Fatal("sent with nothing to say")
	}
	playing := &playback.Now{Source: "pkg:radio", State: playback.State{State: "playing"}}
	p.update(playing)
	if n, ok := p.due(); !ok || n != playing {
		t.Fatalf("first change not sent: %v %v", n, ok)
	}
	// A burst of changes inside a second: held, then the latest goes.
	p.update(&playback.Now{Source: "pkg:radio", State: playback.State{State: "paused"}})
	if _, ok := p.due(); ok {
		t.Fatal("sent twice within a second")
	}
	clock = clock.Add(time.Second)
	if n, ok := p.due(); !ok || n.State.State != "paused" {
		t.Fatalf("latest change not sent: %v %v", n, ok)
	}
	// No change: resent every 10 s while something plays, so jarvisd keeps it (30 s TTL).
	clock = clock.Add(9 * time.Second)
	if _, ok := p.due(); ok {
		t.Fatal("resent too soon")
	}
	clock = clock.Add(time.Second)
	if _, ok := p.due(); !ok {
		t.Fatal("not resent after 10 s")
	}
	// Stopped: nil goes once, then nothing more.
	clock = clock.Add(time.Second)
	p.update(nil)
	if n, ok := p.due(); !ok || n != nil {
		t.Fatalf("stop not sent: %v %v", n, ok)
	}
	clock = clock.Add(time.Minute)
	if _, ok := p.due(); ok {
		t.Fatal("kept sending after the stop")
	}
}

type countingSource struct{ controls []map[string]any }

func (*countingSource) ID() string   { return "pkg:radio" }
func (*countingSource) Name() string { return "Radio" }
func (*countingSource) Status(context.Context) (*playback.State, error) {
	return &playback.State{State: "playing", Actions: []string{"volume"}}, nil
}
func (c *countingSource) Control(_ context.Context, _ string, args map[string]any) (*playback.State, error) {
	c.controls = append(c.controls, args)
	return &playback.State{State: "playing", Actions: []string{"volume"}}, nil
}

func TestPlaybackVerbDropsExpiredCommands(t *testing.T) {
	clock := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	src := &countingSource{}
	hub := &playback.Hub{Sources: func() []playback.Source { return []playback.Source{src} }, Now: func() time.Time { return clock }}
	hub.Tick(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep := newPlaybackReporter(func() *node.CC { return nil }, nil, log)
	verb := playbackVerb(hub, rep, func() time.Time { return clock }, log)

	verb(context.Background(), nil, map[string]any{"action": "volume", "volume": 150.0, "expires_at": clock.Add(time.Second).Format(time.RFC3339)})
	if len(src.controls) != 1 || src.controls[0]["volume"] != 100 {
		t.Fatalf("controls = %v", src.controls)
	}
	verb(context.Background(), nil, map[string]any{"action": "volume", "volume": 10.0, "expires_at": clock.Add(-time.Second).Format(time.RFC3339)})
	if len(src.controls) != 1 {
		t.Fatalf("an expired control ran: %v", src.controls)
	}
}

// coverJarvisd records the playback requests a node sends.
type coverJarvisd struct {
	mu   sync.Mutex
	reqs []string // "PUT /api/v0/node/playback/cover?key=… image/jpeg <body>" or "POST … <cover>"
	fail bool
}

func (j *coverJarvisd) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	j.mu.Lock()
	defer j.mu.Unlock()
	if r.Method == "PUT" && j.fail {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"detail":"x"}`)), Header: http.Header{}}, nil
	}
	line := r.Method + " " + r.URL.RequestURI()
	if r.Method == "PUT" {
		line += " " + r.Header.Get("Content-Type") + " " + string(b)
	} else {
		var body struct {
			Cover string `json:"cover"`
		}
		_ = json.Unmarshal(b, &body)
		line += " cover=" + body.Cover
	}
	j.reqs = append(j.reqs, line)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}, nil
}

func (j *coverJarvisd) take() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.reqs
	j.reqs = nil
	return r
}

func TestPlaybackReporterUploadsTheCoverOncePerTrack(t *testing.T) {
	j := &coverJarvisd{}
	cc := &node.CC{BaseURL: "http://jarvisd.test", HTTP: &http.Client{Transport: j}}
	asks := 0
	cover := []byte("img")
	art := func(context.Context) ([]byte, string, bool) { asks++; return cover, "image/jpeg", cover != nil }
	p := newPlaybackReporter(func() *node.CC { return cc }, art, slog.New(slog.DiscardHandler))
	track := func(title string) *playback.Now {
		return &playback.Now{Source: "pkg:radio", Name: "Radio", State: playback.State{State: "playing", MediaTitle: title}}
	}
	ctx := context.Background()

	p.send(ctx, track("One"))
	got := j.take()
	if len(got) != 2 || !strings.HasPrefix(got[0], "PUT /api/v0/node/playback/cover?key=") || !strings.HasSuffix(got[0], " image/jpeg img") {
		t.Fatalf("first send = %q", got)
	}
	key := strings.Fields(strings.TrimPrefix(got[0], "PUT /api/v0/node/playback/cover?key="))[0]
	if got[1] != "POST /api/v0/node/playback cover="+key {
		t.Fatalf("report = %q, want cover %s", got[1], key)
	}
	// The resend names the same cover without uploading it again.
	p.send(ctx, track("One"))
	if got := j.take(); len(got) != 1 || got[0] != "POST /api/v0/node/playback cover="+key || asks != 1 {
		t.Fatalf("resend = %q (asked %d times)", got, asks)
	}
	// A new track without artwork: no upload, no cover in the report.
	cover = nil
	p.send(ctx, track("Two"))
	if got := j.take(); len(got) != 1 || got[0] != "POST /api/v0/node/playback cover=" {
		t.Fatalf("no-cover track = %q", got)
	}
	// A failed upload is tried again on the next send, and the report doesn't name it meanwhile.
	cover, j.fail = []byte("img3"), true
	p.send(ctx, track("Three"))
	if got := j.take(); len(got) != 1 || got[0] != "POST /api/v0/node/playback cover=" {
		t.Fatalf("failed upload = %q", got)
	}
	j.fail = false
	p.send(ctx, track("Three"))
	if got := j.take(); len(got) != 2 || !strings.HasSuffix(got[0], " img3") {
		t.Fatalf("retry = %q", got)
	}
}
