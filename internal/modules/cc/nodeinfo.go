package cc

import (
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// handleNodeInfo is GET /api/v0/node/info: the calling node's own identity for its packages, as
// the Python node kept it in config.json (Config.get_str("household_id"), get_dict("home_context")):
// its id, room and household, and the household's home_context ({"location"}, as a conversation
// start returns it; null without one). Nothing about any other node.
func (m *Module) handleNodeInfo(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	room := ""
	if n.row != nil {
		room = n.row.room
	}
	var home any
	if n.HouseholdID != "" {
		if loc := strings.TrimSpace(m.settings.String(r.Context(), settingHouseholdLocation, settings.Scope{HouseholdID: n.HouseholdID})); loc != "" {
			home = map[string]any{"location": loc}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"node_id": n.ID, "household_id": n.HouseholdID, "room": room, "home_context": home})
}
