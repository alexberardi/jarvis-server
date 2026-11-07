package playback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fake is a source whose player is a struct; calls counts Status calls.
type fake struct {
	id, name string
	st       *State
	err      error
	calls    int
	actions  []string
}

func (f *fake) ID() string   { return f.id }
func (f *fake) Name() string { return f.name }
func (f *fake) Status(context.Context) (*State, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.st == nil {
		return nil, nil
	}
	c := *f.st
	return &c, nil
}
func (f *fake) Control(_ context.Context, action string, _ map[string]any) (*State, error) {
	f.actions = append(f.actions, action)
	if action == "pause" && f.st != nil {
		f.st.State = "paused"
	}
	return f.Status(context.Background())
}

func playing(title string) *State { return &State{State: "playing", MediaTitle: title} }

type rig struct {
	hub     *Hub
	clock   time.Time
	sources []Source
	changes []*Now
}

func newRig(sources ...Source) *rig {
	r := &rig{clock: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), sources: sources}
	r.hub = &Hub{
		Sources:  func() []Source { return r.sources },
		Now:      func() time.Time { return r.clock },
		OnChange: func(n *Now) { r.changes = append(r.changes, n) },
	}
	return r
}

// step moves the clock and ticks once.
func (r *rig) step(d time.Duration) {
	r.clock = r.clock.Add(d)
	r.hub.Tick(context.Background())
}

func shownTitle(n *Now) string {
	if n == nil {
		return ""
	}
	return n.State.MediaTitle
}

func TestNothingPlayingShowsNothing(t *testing.T) {
	r := newRig(&fake{id: "pkg:radio", name: "Radio"})
	r.step(0)
	if r.hub.Shown() != nil || len(r.changes) != 0 {
		t.Fatalf("shown = %+v, changes = %v", r.hub.Shown(), r.changes)
	}
}

func TestPlayingSourceIsShownAndPolledEveryInterval(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	r := newRig(radio)
	r.step(0)
	n := r.hub.Shown()
	if n == nil || n.Source != "pkg:radio" || n.Name != "Radio" || n.State.MediaTitle != "Jazz FM" || !n.UpdatedAt.Equal(r.clock) {
		t.Fatalf("shown = %+v", n)
	}
	r.step(time.Second) // not due yet
	r.step(time.Second) // 2 s: due
	if radio.calls != 2 {
		t.Fatalf("status calls = %d, want 2", radio.calls)
	}
	// Re-reading the same state is not a change, even though the read time moved.
	if len(r.changes) != 1 {
		t.Fatalf("changes = %d, want 1", len(r.changes))
	}
}

func TestIdleSourcesAreCheckedEveryIdleInterval(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio"}
	r := newRig(radio)
	r.step(0)
	for i := 0; i < 4; i++ {
		r.step(2 * time.Second) // 8 s in: still within the idle interval
	}
	if radio.calls != 1 {
		t.Fatalf("idle status calls after 8 s = %d, want 1", radio.calls)
	}
	// Something outside Jarvis (the phone's Spotify app) starts it: seen at the next idle check.
	radio.st = playing("Started from the phone")
	r.step(2 * time.Second)
	if radio.calls != 2 || shownTitle(r.hub.Shown()) != "Started from the phone" {
		t.Fatalf("calls = %d, shown = %+v", radio.calls, r.hub.Shown())
	}
}

func TestStoppingOutsideJarvisClearsIt(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	r := newRig(radio)
	r.step(0)
	radio.st = nil // stopped from the phone
	r.step(2 * time.Second)
	if r.hub.Shown() != nil || len(r.changes) != 2 || r.changes[1] != nil {
		t.Fatalf("shown = %+v, changes = %v", r.hub.Shown(), r.changes)
	}
}

func TestASourceThatStopsAnsweringIsDroppedAfterThreeFailures(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	r := newRig(radio)
	r.step(0)
	radio.err = errors.New("player gone")
	r.step(2 * time.Second)
	r.step(2 * time.Second)
	if shownTitle(r.hub.Shown()) != "Jazz FM" {
		t.Fatal("dropped before the third failure")
	}
	r.step(2 * time.Second)
	if r.hub.Shown() != nil {
		t.Fatalf("still shown after three failures: %+v", r.hub.Shown())
	}
	// One good read resets the count.
	radio.err = nil
	r.step(10 * time.Second)
	radio.err = errors.New("blip")
	r.step(2 * time.Second)
	radio.err = nil
	r.step(2 * time.Second)
	r.step(2 * time.Second)
	if shownTitle(r.hub.Shown()) != "Jazz FM" {
		t.Fatalf("a single failure dropped it: %+v", r.hub.Shown())
	}
}

func TestTheMostRecentlyStartedSourceWinsAndTheOlderComesBack(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	phone := &fake{id: "bluetooth:aa", name: "Bluetooth: phone"}
	r := newRig(radio, phone)
	r.step(0)
	phone.st = playing("From the phone")
	r.step(10 * time.Second) // the phone's idle check
	if got := r.hub.Shown(); got.Source != "bluetooth:aa" {
		t.Fatalf("shown = %+v, want the phone", got)
	}
	// Nothing is stopped on its own: the radio is still polled and comes back when the phone stops.
	phone.st = nil
	r.step(2 * time.Second)
	if got := r.hub.Shown(); got == nil || got.Source != "pkg:radio" {
		t.Fatalf("shown = %+v, want the radio again", got)
	}
}

func TestPlayingBeatsPausedAndResumingCountsAsNewest(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	tv := &fake{id: "display", name: "Display", st: &State{State: "paused", MediaTitle: "Film"}}
	r := newRig(radio, tv)
	r.step(0)
	if got := r.hub.Shown(); got.Source != "pkg:radio" {
		t.Fatalf("paused beat playing: %+v", got)
	}
	tv.st.State = "playing" // resumed after the radio started
	r.step(2 * time.Second)
	if got := r.hub.Shown(); got.Source != "display" {
		t.Fatalf("resumed source not newest: %+v", got)
	}
	radio.st, tv.st = &State{State: "paused", MediaTitle: "Jazz FM"}, nil
	r.step(2 * time.Second)
	if got := r.hub.Shown(); got == nil || got.State.State != "paused" {
		t.Fatalf("a paused source alone should still show (so it can be resumed): %+v", got)
	}
}

func TestASourceThatDisappearsIsDropped(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	r := newRig(radio)
	r.step(0)
	r.sources = nil // its package was uninstalled
	r.step(2 * time.Second)
	if r.hub.Shown() != nil {
		t.Fatalf("shown = %+v", r.hub.Shown())
	}
}

func TestReportIsSeenAtOnce(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio"}
	r := newRig(radio)
	r.step(0)
	r.clock = r.clock.Add(time.Second)
	r.hub.Report(radio, playing("Pushed"))
	if shownTitle(r.hub.Shown()) != "Pushed" || radio.calls != 1 {
		t.Fatalf("shown = %+v, calls = %d", r.hub.Shown(), radio.calls)
	}
}

func TestControlGoesToTheShownSourceAndReturnsTheReadBack(t *testing.T) {
	radio := &fake{id: "pkg:radio", name: "Radio", st: playing("Jazz FM")}
	r := newRig(radio)
	if _, err := r.hub.Control(context.Background(), "pause", nil); !errors.Is(err, ErrNothingPlaying) {
		t.Fatalf("control with nothing shown = %v", err)
	}
	r.step(0)
	n, err := r.hub.Control(context.Background(), "pause", nil)
	if err != nil || n.State.State != "paused" || len(radio.actions) != 1 {
		t.Fatalf("control = %+v, %v, actions %v", n, err, radio.actions)
	}
	if got := r.changes[len(r.changes)-1]; got.State.State != "paused" {
		t.Fatalf("last change = %+v", got)
	}
}

func TestParse(t *testing.T) {
	if s, err := Parse(nil); s != nil || err != nil {
		t.Fatalf("nil = %v, %v", s, err)
	}
	if s, err := Parse(json.RawMessage("null")); s != nil || err != nil {
		t.Fatalf("null = %v, %v", s, err)
	}
	s, err := Parse(json.RawMessage(`{"state":"playing","media_title":"t","actions":["pause"],"volume":40}`))
	if err != nil || s.State != "playing" || *s.Volume != 40 || s.Actions[0] != "pause" {
		t.Fatalf("parse = %+v, %v", s, err)
	}
}

// artFake is a source with a cover; fetches counts Artwork calls.
type artFake struct {
	fake
	fetches int
	cover   []byte
}

func (a *artFake) Artwork(context.Context) ([]byte, string, error) {
	a.fetches++
	if a.cover == nil {
		return nil, "", errors.New("no cover")
	}
	return a.cover, "image/jpeg", nil
}

func TestArtworkIsFetchedOncePerTrack(t *testing.T) {
	src := &artFake{fake: fake{id: "pkg:radio", name: "Radio", st: playing("One")}, cover: []byte("jpeg-1")}
	r := newRig(src)
	if _, _, ok := r.hub.Artwork(context.Background()); ok {
		t.Fatal("artwork with nothing shown")
	}
	r.step(0)
	for i := 0; i < 3; i++ {
		b, mime, ok := r.hub.Artwork(context.Background())
		if !ok || string(b) != "jpeg-1" || mime != "image/jpeg" {
			t.Fatalf("artwork = %q %q %v", b, mime, ok)
		}
	}
	if src.fetches != 1 {
		t.Fatalf("fetches = %d, want 1", src.fetches)
	}
	// A new track: fetched again.
	src.st, src.cover = playing("Two"), []byte("jpeg-2")
	r.step(2 * time.Second)
	if b, _, _ := r.hub.Artwork(context.Background()); string(b) != "jpeg-2" || src.fetches != 2 {
		t.Fatalf("new track artwork = %q, fetches %d", b, src.fetches)
	}
	// A failed fetch isn't retried on every request, only on the next track.
	src.st, src.cover = playing("Three"), nil
	r.step(2 * time.Second)
	r.hub.Artwork(context.Background())
	if _, _, ok := r.hub.Artwork(context.Background()); ok || src.fetches != 3 {
		t.Fatalf("failed cover: ok %v, fetches %d", ok, src.fetches)
	}
	// A source without artwork.
	plain := newRig(&fake{id: "pkg:plain", name: "Plain", st: playing("x")})
	plain.step(0)
	if _, _, ok := plain.hub.Artwork(context.Background()); ok {
		t.Fatal("artwork from a source that has none")
	}
}
