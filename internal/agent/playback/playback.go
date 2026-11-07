// Package playback is the node's playback hub (docs/agent/PLAYBACK.md): the one owner of
// "what is playing here". It knows sources only through Source (a package's
// IJarvisMusicCommand, the display's mpv, a phone over Bluetooth) and never names a service.
//
// The player is the source of truth (P3): the hub keeps only what a source last read back, polls
// each source that plays every Interval, checks idle ones every IdleInterval (a phone's Spotify
// app starting music tells nobody), and drops a source after Failures failed checks in a row, so
// nothing stale is shown (P14). The shown source is the most recently started one still playing,
// else the most recently started paused one. The hub stops no player on its own.
package playback

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"
)

// State is one source's playback state: the SDK's PlaybackState.to_dict().
type State struct {
	State       string   `json:"state"` // playing | paused | idle
	MediaTitle  string   `json:"media_title"`
	MediaArtist string   `json:"media_artist"`
	MediaAlbum  string   `json:"media_album"`
	Context     string   `json:"context"`
	Actions     []string `json:"actions"`
	Position    *float64 `json:"position,omitempty"`
	Duration    *float64 `json:"duration,omitempty"`
	Volume      *int     `json:"volume,omitempty"`
	Shuffle     *bool    `json:"shuffle,omitempty"`
	Repeat      *string  `json:"repeat,omitempty"`
	Liked       *bool    `json:"liked,omitempty"`
}

// Parse reads a source's state as the sidecar returns it; nil (JSON null) is "nothing plays".
func Parse(raw json.RawMessage) (*State, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *State) active() bool { return s != nil && (s.State == "playing" || s.State == "paused") }

// Source is one thing that can play on this node.
type Source interface {
	ID() string   // stable across polls: "pkg:spotify", "display", "bluetooth:<address>"
	Name() string // shown to people: "Spotify", "Bluetooth: Alex's phone"
	Status(ctx context.Context) (*State, error)
	// Control performs one action and returns the state read back afterwards.
	Control(ctx context.Context, action string, args map[string]any) (*State, error)
}

// Now is what the node shows: the chosen source and its state, stamped when it was read.
type Now struct {
	Source    string    `json:"source"`
	Name      string    `json:"name"`
	State     State     `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrNothingPlaying is a control with no source to send it to.
var ErrNothingPlaying = errors.New("nothing is playing")

// Defaults (P14, P17).
const (
	DefaultInterval     = 2 * time.Second
	DefaultIdleInterval = 10 * time.Second
	DefaultFailures     = 3
)

// Hub tracks the node's sources. Set the fields before Run; the zero durations use the defaults.
type Hub struct {
	// Sources lists the sources right now (they come and go: a package swap, a phone connecting).
	Sources func() []Source
	// OnChange gets every change of what is shown (nil: nothing shows). It runs with no lock held.
	OnChange func(*Now)
	Now      func() time.Time

	Interval, IdleInterval time.Duration
	Failures               int

	mu      sync.Mutex
	tracked map[string]*entry
	shown   *Now
	art     artwork
}

type entry struct {
	name      string
	state     *State
	readAt    time.Time
	startedAt time.Time // when it last went to playing
	failures  int
	nextCheck time.Time
}

func (h *Hub) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Hub) interval() time.Duration     { return orDefault(h.Interval, DefaultInterval) }
func (h *Hub) idleInterval() time.Duration { return orDefault(h.IdleInterval, DefaultIdleInterval) }

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run ticks every Interval until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.interval())
	defer t.Stop()
	for {
		h.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick checks each source that is due: a tracked one every Interval, an idle one every
// IdleInterval. A source no longer listed is dropped.
func (h *Hub) Tick(ctx context.Context) {
	var sources []Source
	if h.Sources != nil {
		sources = h.Sources()
	}
	now := h.now()
	listed := map[string]bool{}
	var due []Source
	h.mu.Lock()
	if h.tracked == nil {
		h.tracked = map[string]*entry{}
	}
	for _, s := range sources {
		listed[s.ID()] = true
		e := h.tracked[s.ID()]
		if e == nil || !now.Before(e.nextCheck) {
			due = append(due, s)
		}
	}
	for id := range h.tracked {
		if !listed[id] {
			delete(h.tracked, id)
		}
	}
	h.mu.Unlock()

	for _, s := range due { // no lock: a source may take a while to answer
		st, err := s.Status(ctx)
		h.record(s, st, err, h.now())
	}
	h.publish()
}

// Report takes a state a source pushed (its report()) as if the hub had just read it.
func (h *Hub) Report(s Source, st *State) {
	h.record(s, st, nil, h.now())
	h.publish()
}

// Control sends an action to the shown source and returns what it read back afterwards.
func (h *Hub) Control(ctx context.Context, action string, args map[string]any) (*Now, error) {
	var target Source
	h.mu.Lock()
	shown := h.shown
	h.mu.Unlock()
	if shown != nil && h.Sources != nil {
		for _, s := range h.Sources() {
			if s.ID() == shown.Source {
				target = s
				break
			}
		}
	}
	if target == nil {
		return nil, ErrNothingPlaying
	}
	st, err := target.Control(ctx, action, args)
	if err != nil {
		return nil, err
	}
	h.record(target, st, nil, h.now())
	h.publish()
	return h.Shown(), nil
}

// Shown is what the node shows now, or nil.
func (h *Hub) Shown() *Now {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shown == nil {
		return nil
	}
	c := *h.shown
	return &c
}

func (h *Hub) record(s Source, st *State, err error, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tracked == nil {
		h.tracked = map[string]*entry{}
	}
	id := s.ID()
	e := h.tracked[id]
	idle := &entry{name: s.Name(), nextCheck: now.Add(h.idleInterval())} // nothing shown; look again later
	if err != nil {
		if e == nil || !e.state.active() {
			h.tracked[id] = idle // an idle source that can't answer: nothing to drop
			return
		}
		e.failures++
		e.nextCheck = now.Add(h.interval())
		if e.failures >= orInt(h.Failures, DefaultFailures) {
			h.tracked[id] = idle // it can't confirm it still plays: stop showing it (P14)
		}
		return
	}
	if !st.active() {
		h.tracked[id] = idle
		return
	}
	if e == nil || !e.state.active() {
		e = &entry{startedAt: now}
		h.tracked[id] = e
	}
	if st.State == "playing" && e.state != nil && e.state.State != "playing" {
		e.startedAt = now // resumed: it is the newest thing playing again
	}
	e.name, e.state, e.readAt, e.failures = s.Name(), st, now, 0
	e.nextCheck = now.Add(h.interval())
}

func orInt(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}

// publish picks what is shown and calls OnChange when it changed.
func (h *Hub) publish() {
	h.mu.Lock()
	type cand struct {
		id string
		e  *entry
	}
	var cands []cand
	for id, e := range h.tracked {
		if e.state.active() {
			cands = append(cands, cand{id, e})
		}
	}
	slices.SortFunc(cands, func(a, b cand) int {
		if pa, pb := a.e.state.State == "playing", b.e.state.State == "playing"; pa != pb {
			if pa {
				return -1
			}
			return 1
		}
		if c := b.e.startedAt.Compare(a.e.startedAt); c != 0 {
			return c
		}
		return compareStrings(a.id, b.id)
	})
	var next *Now
	if len(cands) > 0 {
		c := cands[0]
		next = &Now{Source: c.id, Name: c.e.name, State: *c.e.state, UpdatedAt: c.e.readAt}
	}
	changed := !same(h.shown, next)
	h.shown = next
	onChange := h.OnChange
	h.mu.Unlock()
	if changed && onChange != nil {
		onChange(next)
	}
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// same compares what people see; a new read time alone is not a change.
func same(a, b *Now) bool {
	if a == nil || b == nil {
		return a == b
	}
	ja, _ := json.Marshal(a.State)
	jb, _ := json.Marshal(b.State)
	return a.Source == b.Source && a.Name == b.Name && string(ja) == string(jb)
}

// ArtworkSource is a source that can hand over the current track's cover image (P19): the
// bytes, never a URL, so nothing on the node or in the apps fetches from a service.
type ArtworkSource interface {
	Artwork(ctx context.Context) (data []byte, mime string, err error)
}

// artwork is the one cover kept: the shown track's, fetched once per track.
type artwork struct {
	key  string
	data []byte
	mime string
}

// TrackKey names the shown track: a new one means new artwork.
func TrackKey(n *Now) string {
	if n == nil {
		return ""
	}
	return n.Source + "\x00" + n.State.MediaTitle + "\x00" + n.State.MediaArtist + "\x00" + n.State.MediaAlbum
}

// Artwork is the shown track's cover, from the source on the first ask for each track. ok is
// false when nothing is shown, the source has no artwork, or fetching it failed (asked again
// on the next track, not on every request).
func (h *Hub) Artwork(ctx context.Context) (data []byte, mime string, ok bool) {
	shown := h.Shown()
	key := TrackKey(shown)
	if key == "" {
		return nil, "", false
	}
	h.mu.Lock()
	if h.art.key == key {
		a := h.art
		h.mu.Unlock()
		return a.data, a.mime, a.data != nil
	}
	h.mu.Unlock()
	var src ArtworkSource
	if h.Sources != nil {
		for _, s := range h.Sources() {
			if s.ID() == shown.Source {
				src, _ = s.(ArtworkSource)
			}
		}
	}
	a := artwork{key: key}
	if src != nil {
		if b, m, err := src.Artwork(ctx); err == nil && len(b) > 0 {
			a.data, a.mime = b, m
		}
	}
	h.mu.Lock()
	h.art = a
	h.mu.Unlock()
	return a.data, a.mime, a.data != nil
}
