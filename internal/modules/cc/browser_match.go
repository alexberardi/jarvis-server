package cc

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// browserMatchTimeout bounds the node's answer; the web polls again (a var for tests).
var browserMatchTimeout = 5 * time.Second

// maxMatchPattern bounds the pattern a caller sends (it comes from an inbox card's webview_pick).
const maxMatchPattern = 500

func (m *Module) registerBrowserMatch(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/mobile/nodes/{node_id}/browser/matches", m.user(m.handleBrowserMatches))
}

// handleBrowserMatches is the web's "Find ID" (docs/webapp/03 step 3, decision
// 2026-11-15-web-pick-reads-your-tabs): the values `pattern` captures from the URLs of the tabs
// open on the caller's own computer. Only the member who paired the node may ask: tabs are
// personal, so a household admin gets 404 as for any node that isn't theirs. The node matches
// on its side and answers values and titles only.
func (m *Module) handleBrowserMatches(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" || len(pattern) > maxMatchPattern {
		validationError(w, "query -> pattern: a pattern of 1 to 500 characters is required")
		return
	}
	n, err := m.nodeByID(ctx, r.PathValue("node_id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !(n.ownerUserID.Valid && n.ownerUserID.Int64 == u.ID)) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !m.bus.Available() {
		detail(w, http.StatusServiceUnavailable, "MQTT client not available")
		return
	}
	rctx, cancel := context.WithTimeout(ctx, browserMatchTimeout)
	defer cancel()
	raw, err := m.bus.Request(rctx, n.nodeID, "browser/match-tabs", map[string]any{"pattern": pattern})
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// An agent too old to know the topic never answers: the same as a computer that's off.
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": false, "error": map[string]string{
			"code": "no_answer", "message": "This computer didn't answer. Is the Jarvis agent running?"}})
		return
	}
	var out map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&out); err != nil || out == nil {
		detail(w, http.StatusBadGateway, "Node returned invalid response")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
