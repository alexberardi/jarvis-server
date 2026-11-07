package cc

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Playback (docs/agent/PLAYBACK.md piece 3): each node reports what it plays, jarvisd keeps
// the latest report per node in memory (live state, not history) and serves the household's to
// its members, who control a node through it. Nothing here names a service: a report is the
// node hub's generic state.

const (
	// playbackTTL: a report not refreshed for this long is gone (a crashed or offline node; P14).
	// A node resends every 10 s while something plays.
	playbackTTL = 30 * time.Second
	// playbackControlWait: how long a control waits for the node's read-back report (P11).
	playbackControlWait = 3 * time.Second
	// playbackCommandTTL: a control command the node gets later than this is dropped there, so
	// a pause queued for an offline node never plays out when it comes back.
	playbackCommandTTL  = 10 * time.Second
	playbackReportLimit = 16 << 10
	// playbackCoverLimit is the SDK's ARTWORK_MAX_BYTES.
	playbackCoverLimit = 2 << 20
)

var (
	playbackStates  = []string{"playing", "paused", "idle"}
	playbackActions = []string{"play", "pause", "next", "previous", "seek", "volume", "shuffle", "repeat", "like"}
	coverTypes      = []string{"image/jpeg", "image/png", "image/webp"}
)

// validCoverKey: the node's name for a track's cover, 1-64 letters and digits.
func validCoverKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// playbackNow is a node hub's playback.Now: the shown source and its state.
type playbackNow struct {
	Source    string        `json:"source"`
	Name      string        `json:"name"`
	State     playbackState `json:"state"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// playbackState is the SDK's PlaybackState.to_dict(), checked before it is kept.
type playbackState struct {
	State       string   `json:"state"`
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

func (n *playbackNow) valid() string {
	switch {
	case n.Source == "" || len(n.Source) > 100:
		return "source must be 1-100 characters"
	case len(n.Name) > 100:
		return "name must be at most 100 characters"
	case !slices.Contains(playbackStates, n.State.State):
		return "state.state must be playing, paused or idle"
	case len(n.State.MediaTitle) > 500 || len(n.State.MediaArtist) > 500 || len(n.State.MediaAlbum) > 500 || len(n.State.Context) > 500:
		return "text fields must be at most 500 characters"
	case n.State.Repeat != nil && !slices.Contains([]string{"off", "all", "one"}, *n.State.Repeat):
		return "state.repeat must be off, all or one"
	}
	for _, a := range n.State.Actions {
		if !slices.Contains(playbackActions, a) {
			return "state.actions has an unknown action: " + a
		}
	}
	return ""
}

type playbackRecord struct {
	householdID string
	now         playbackNow
	cover       string // the cover key the node reported for this track; "" without one
	received    time.Time
}

// playbackCover is the image a node uploaded for a cover key (P19: the apps get it from jarvisd).
type playbackCover struct {
	key, mime string
	data      []byte
}

// playbackStore holds the latest report and cover per node; waiters hear about the next report.
type playbackStore struct {
	mu      sync.Mutex
	byNode  map[string]playbackRecord
	covers  map[string]playbackCover
	waiters map[string][]chan struct{}
}

func newPlaybackStore() *playbackStore {
	return &playbackStore{byNode: map[string]playbackRecord{}, covers: map[string]playbackCover{}, waiters: map[string][]chan struct{}{}}
}

// put keeps a report (nil: nothing plays) and wakes whoever waits on the node.
func (s *playbackStore) put(nodeID, hh string, now *playbackNow, cover string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now == nil {
		delete(s.byNode, nodeID)
		delete(s.covers, nodeID)
	} else {
		s.byNode[nodeID] = playbackRecord{householdID: hh, now: *now, cover: cover, received: at}
	}
	for _, c := range s.waiters[nodeID] {
		close(c)
	}
	delete(s.waiters, nodeID)
}

// wait returns a channel closed by the node's next report.
func (s *playbackStore) wait(nodeID string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := make(chan struct{})
	s.waiters[nodeID] = append(s.waiters[nodeID], c)
	return c
}

func (s *playbackStore) drop(nodeID string, c chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws := s.waiters[nodeID]
	if i := slices.Index(ws, c); i >= 0 {
		s.waiters[nodeID] = slices.Delete(ws, i, i+1)
	}
}

// get is the node's report if it is fresh and belongs to hh.
func (s *playbackStore) get(nodeID, hh string, now time.Time) (playbackRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byNode[nodeID]
	if !ok || r.householdID != hh {
		return playbackRecord{}, false
	}
	if now.Sub(r.received) > playbackTTL {
		delete(s.byNode, nodeID)
		delete(s.covers, nodeID)
		return playbackRecord{}, false
	}
	return r, true
}

// putCover keeps one cover per node, the latest upload.
func (s *playbackStore) putCover(nodeID string, c playbackCover) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.covers[nodeID] = c
}

// cover is the node's cover for the track it is playing now, if it uploaded that one.
func (s *playbackStore) cover(nodeID, hh string, now time.Time) (playbackCover, bool) {
	r, ok := s.get(nodeID, hh, now)
	if !ok || r.cover == "" {
		return playbackCover{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.covers[nodeID]
	return c, ok && c.key == r.cover
}

func (m *Module) registerPlayback(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/node/playback", m.node(m.handleNodePlayback))
	mux.HandleFunc("PUT "+v0+"/node/playback/cover", m.node(m.handleNodePlaybackCover))
	mux.HandleFunc("GET "+v0+"/mobile/playback", m.user(m.handlePlayback))
	mux.HandleFunc("GET "+v0+"/mobile/playback/cover", m.user(m.handlePlaybackCover))
	mux.HandleFunc("POST "+v0+"/mobile/playback/control", m.user(m.handlePlaybackControl))
}

// --- POST /node/playback: {"now": {...} | null, "cover": "key"?} ---

func (m *Module) handleNodePlayback(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	raw, ok := readRawObjectLimit(w, r, playbackReportLimit)
	if !ok {
		return
	}
	var in struct {
		Now   *playbackNow `json:"now"`
		Cover string       `json:"cover"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		detail(w, http.StatusUnprocessableEntity, "Invalid playback report: "+err.Error())
		return
	}
	if in.Cover != "" && !validCoverKey(in.Cover) {
		detail(w, http.StatusUnprocessableEntity, "Invalid playback report: cover must be 1-64 letters and digits")
		return
	}
	if in.Now != nil {
		if msg := in.Now.valid(); msg != "" {
			detail(w, http.StatusUnprocessableEntity, "Invalid playback report: "+msg)
			return
		}
		if in.Now.State.State == "idle" {
			in.Now = nil
		}
	}
	if n.HouseholdID == "" {
		detail(w, http.StatusConflict, "Node is not in a household")
		return
	}
	m.playback.put(n.ID, n.HouseholdID, in.Now, in.Cover, m.now())
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- PUT /node/playback/cover?key=K: the cover image's bytes, sent before the report naming K ---

func (m *Module) handleNodePlaybackCover(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	key := r.URL.Query().Get("key")
	mime := r.Header.Get("Content-Type")
	switch {
	case !validCoverKey(key):
		detail(w, http.StatusUnprocessableEntity, "key must be 1-64 letters and digits")
		return
	case !slices.Contains(coverTypes, mime):
		detail(w, http.StatusUnsupportedMediaType, "Cover must be image/jpeg, image/png or image/webp")
		return
	case n.HouseholdID == "":
		detail(w, http.StatusConflict, "Node is not in a household")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, playbackCoverLimit))
	if err != nil {
		detail(w, http.StatusRequestEntityTooLarge, "Cover is larger than 2 MiB")
		return
	}
	if len(data) == 0 {
		detail(w, http.StatusUnprocessableEntity, "Cover is empty")
		return
	}
	m.playback.putCover(n.ID, playbackCover{key: key, mime: mime, data: data})
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /mobile/playback?household_id=X: every node of the household that plays ---

func (m *Module) handlePlayback(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	hh := r.URL.Query().Get("household_id")
	if hh == "" {
		detail(w, http.StatusUnprocessableEntity, "household_id is required")
		return
	}
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	nodes, err := m.householdNodes(ctx, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	names := m.nodeNames(ctx, nodes...)
	out := []map[string]any{}
	now := m.now()
	for _, n := range nodes {
		if rec, ok := m.playback.get(n.nodeID, hh, now); ok {
			out = append(out, playbackEntry(n.nodeID, names[n.nodeID], rec))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"playing": out})
}

func playbackEntry(nodeID, nodeName string, rec playbackRecord) map[string]any {
	e := map[string]any{"node_id": nodeID, "node_name": nodeName, "source": rec.now.Source,
		"name": rec.now.Name, "state": rec.now.State, "updated_at": rec.now.UpdatedAt}
	if rec.cover != "" {
		e["cover"] = rec.cover
	}
	return e
}

// --- GET /mobile/playback/cover?node_id=X: the cover of what X plays now (the entry's "cover" busts caches) ---

func (m *Module) handlePlaybackCover(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	row, err := m.nodeByID(ctx, r.URL.Query().Get("node_id"))
	if err != nil || !row.householdID.Valid {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	hh := row.householdID.String
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	c, ok := m.playback.cover(row.nodeID, hh, m.now())
	if !ok {
		detail(w, http.StatusNotFound, "No cover")
		return
	}
	w.Header().Set("Content-Type", c.mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(c.data)
}

// --- POST /mobile/playback/control: {node_id, action, volume?, position?} ---

func (m *Module) handlePlaybackControl(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	nodeID, _ := b.str("node_id", true)
	action, _ := b.str("action", true)
	volume, hasVolume := b.number("volume")
	position, hasPosition := b.number("position")
	if !b.done(w) {
		return
	}
	if !slices.Contains(playbackActions, action) {
		detail(w, http.StatusUnprocessableEntity, "Unknown action: "+action)
		return
	}
	ctx := r.Context()
	row, err := m.nodeByID(ctx, nodeID)
	if err != nil || !row.householdID.Valid {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	hh := row.householdID.String
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	if !m.nodeReachable(row) {
		detail(w, http.StatusConflict, "Node is offline")
		return
	}
	details := map[string]any{"action": action, "expires_at": m.now().Add(playbackCommandTTL).UTC().Format(time.RFC3339)}
	if hasVolume {
		details["volume"] = volume
	}
	if hasPosition {
		details["position"] = position
	}
	reported := m.playback.wait(nodeID)
	m.bus.Command(nodeID, "playback_control", details)
	select {
	case <-reported:
	case <-time.After(playbackControlWait):
		m.playback.drop(nodeID, reported)
	case <-ctx.Done():
		m.playback.drop(nodeID, reported)
		return
	}
	out := map[string]any{"node_id": nodeID, "playing": nil}
	if rec, ok := m.playback.get(nodeID, hh, m.now()); ok {
		out["playing"] = playbackEntry(nodeID, m.nodeNames(ctx, row)[nodeID], rec)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
