package nodesettings

import (
	"context"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

// Routines are owned by command-center, per household; the node keeps a copy as the routine
// command's data (N:services/routine_sync_service.py). jarvisd nudges routines/sync
// ({event: routines_changed, household_id}) when they change, and the node pulls the whole set
// from GET /api/v0/nodes/{id}/routines ({routines: {slug: definition}}) then, and on connect.

// TopicRoutinesSync is the nudge's sub-topic.
const TopicRoutinesSync = "routines/sync"

// RoutineCommand is the command whose data holds the routines (routine/<slug>).
const RoutineCommand = "routine"

// DefaultRoutines are the routine command's built-in slugs (N:commands/routine_command.py
// _default_routines): a pull never prunes them.
var DefaultRoutines = []string{"good_morning", "good_night", "morning_briefing", "nightly_briefing"}

// Routines pulls the household's routines into one node's store.
type Routines struct {
	NodeID string
	Store  *Store
	// Protected lists the slugs a pull keeps even when the server no longer has them, beyond
	// DefaultRoutines: the routines installed Pantry packages ship. nil: none.
	Protected func() []string
	Log       *slog.Logger

	mu sync.Mutex // one pull at a time
}

// Pull rewrites the routine records from command-center's set and returns how many it applied:
// each server routine is saved, and a stored one the server no longer has is removed unless it
// is protected. When command-center can't be reached or answers an error, nothing changes.
func (r *Routines) Pull(ctx context.Context, api API) (int, error) {
	var resp struct {
		Routines map[string]Object `json:"routines"`
	}
	if err := api.Do(ctx, http.MethodGet, "/api/v0/nodes/"+url.PathEscape(r.NodeID)+"/routines", nil, &resp); err != nil {
		r.Log.Warn("routines: pull failed; keeping the routines this node has", "err", err)
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, slug := range slices.Sorted(maps.Keys(resp.Routines)) { // a stable order in the store
		if err := r.Store.SaveData(RoutineCommand, slug, resp.Routines[slug], nil); err != nil {
			return 0, err
		}
	}
	keep := map[string]bool{}
	for _, s := range DefaultRoutines {
		keep[s] = true
	}
	if r.Protected != nil {
		for _, s := range r.Protected() {
			keep[s] = true
		}
	}
	for _, rec := range r.Store.Records(RoutineCommand, time.Now()) {
		if _, onServer := resp.Routines[rec.Key]; onServer || keep[rec.Key] {
			continue
		}
		if _, err := r.Store.DeleteData(RoutineCommand, rec.Key); err != nil {
			return 0, err
		}
		r.Log.Info("routines: removed one the household no longer has", "slug", rec.Key)
	}
	r.Log.Info("routines: pulled from command-center", "count", len(resp.Routines))
	return len(resp.Routines), nil
}

// Handle is the routines/sync nudge: it carries no routines, so any message pulls the set.
func (r *Routines) Handle(ctx context.Context, api API, _ []byte) {
	_, _ = r.Pull(ctx, api)
}
