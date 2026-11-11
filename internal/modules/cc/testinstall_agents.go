package cc

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// verbRunTestAgents asks the node to run its test installs' background agents now
// (pkgagents.Scheduler.RunTests): a Forge test needn't wait out an agent's interval.
const verbRunTestAgents = "run_test_agents"

// --- POST /nodes/{node_id}/test-install/{request_id}/run-agents ---

func (m *Module) handleRunTestAgents(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	t, err := loadTI(ctx, m.deps.DB.Read, r.PathValue("request_id"), r.PathValue("node_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Test install request not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if err := m.requirePkgHousehold(ctx, auth, t.householdID); err != nil {
		m.writeErr(w, err)
		return
	}
	if t.status != pkgCompleted || !t.completedAt.Valid || parseTS(t.completedAt.String).Before(m.now().Add(-testRunWindow)) {
		detail(w, http.StatusConflict, "The test install isn't running")
		return
	}
	var d struct {
		Agents []string `json:"agents"`
	}
	_ = json.Unmarshal([]byte(t.resultsJSON.String), &d)
	if len(d.Agents) == 0 {
		detail(w, http.StatusConflict, "This draft has no background agents")
		return
	}
	m.bus.Command(t.nodeID, verbRunTestAgents, nil)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "sent", "agents": d.Agents})
}
