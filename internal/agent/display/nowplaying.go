package display

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/agent/playback"
)

// The full-screen now-playing view (docs/agent/PLAYBACK.md piece 4). It shows the playback
// hub's state for any source; nothing here names a service.

// SetPlayback gives the display the node's playback hub.
func (e *Engine) SetPlayback(h *playback.Hub) { e.playback.Store(h) }

// nowPlaying is what the view shows: the hub's shown source, except the display's own player,
// whose video is already on the screen.
func (e *Engine) nowPlaying() *playback.Now {
	hub := e.playback.Load()
	if hub == nil {
		return nil
	}
	n := hub.Shown()
	if n == nil || n.Source == "display" {
		return nil
	}
	return n
}

// handleNowPlaying serves POST /api/now-playing: one of the view's buttons. It answers with
// the state read back from the player.
func (e *Engine) handleNowPlaying(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action   string   `json:"action"`
		Volume   *float64 `json:"volume"`
		Position *float64 `json:"position"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil {
		w.WriteHeader(400)
		WriteJSON(w, map[string]any{"error": "Invalid request"})
		return
	}
	if !slices.Contains(playback.Actions, body.Action) || body.Action == "status" {
		w.WriteHeader(400)
		WriteJSON(w, map[string]any{"error": "Unknown action"})
		return
	}
	if e.nowPlaying() == nil {
		w.WriteHeader(409)
		WriteJSON(w, map[string]any{"error": "Nothing is playing"})
		return
	}
	args := map[string]any{}
	if body.Volume != nil {
		args["volume"] = max(0, min(100, int(*body.Volume)))
	}
	if body.Position != nil {
		args["position"] = max(0, *body.Position)
	}
	now, err := e.playback.Load().Control(r.Context(), body.Action, args)
	if errors.Is(err, playback.ErrNothingPlaying) {
		w.WriteHeader(409)
		WriteJSON(w, map[string]any{"error": "Nothing is playing"})
		return
	}
	if err != nil {
		w.WriteHeader(502)
		WriteJSON(w, map[string]any{"error": "The player didn't respond: " + err.Error()})
		return
	}
	WriteJSON(w, map[string]any{"now_playing": now})
}
