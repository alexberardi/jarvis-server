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

// nodeToolsWait: how long GET /mobile/nodes/{id}/tools waits for the node's report (§5; a var
// for tests).
var nodeToolsWait = 10 * time.Second

// handleNodeTools asks a node for its tools, commands and installed packages (doc 12 §3.6).
// CC publishes report_tools on the node's commands topic (no `trusted`, D4; the node verifies
// it through /commands/{rid}/verify) and waits for the node's POST to
// /mobile/node-tool-reports/{rid}, which the shared result sink hands to this request's slot
// (node auth, rid bound to this node). A node that doesn't answer yields 200 with empty lists
// (D40 12.Q9), now logged.
func (m *Module) handleNodeTools(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	nodeID := r.PathValue("node_id")
	n, err := m.nodeByID(ctx, nodeID)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	// D40 12.Q5: a node with no household fails closed (legacy skipped the check).
	if !n.householdID.Valid || n.householdID.String == "" {
		detail(w, http.StatusForbidden, "Not authorized")
		return
	}
	if err := m.requireRole(ctx, u.ID, n.householdID.String, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}

	out := map[string]any{"client_tools": []any{}, "available_commands": []any{}, "installed_packages": []any{}}
	report, ok := m.requestNodeTools(ctx, nodeID)
	if !ok {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	// D8: a report missing a key degrades to an empty list (legacy raised a KeyError → 500).
	for k := range out {
		if v, ok := report[k]; ok && v != nil {
			out[k] = v
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) requestNodeTools(ctx context.Context, nodeID string) (map[string]any, bool) {
	if !m.bus.Available() {
		m.deps.Log.Warn("cc: node tools unavailable: MQTT not available", "node", nodeID)
		return nil, false
	}
	rid := uuid4()
	m.bus.Expect(rid, nodeID)
	defer m.bus.Drop(rid)
	m.bus.CommandWithID(nodeID, "report_tools", map[string]any{"reply_request_id": rid}, rid)
	wctx, cancel := context.WithTimeout(ctx, nodeToolsWait)
	defer cancel()
	raw, err := m.bus.Await(wctx, rid)
	if err != nil {
		m.deps.Log.Warn("cc: node did not report its tools; answering empty", "node", nodeID, "request_id", rid, "err", err)
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // opaque pass-through: keep numbers as the node wrote them
	var report map[string]any
	if err := dec.Decode(&report); err != nil || len(report) == 0 {
		return nil, false
	}
	return report, true
}
