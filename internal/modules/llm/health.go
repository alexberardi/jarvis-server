package llm

import (
	"errors"
	"net/http"
	"time"
)

// loadingGrace is how long the live label may stay "loading" before /health turns 503
// (health_routes.py _LOADING_GRACE_WINDOW_S).
const loadingGrace = 15 * time.Minute

type slotJSON struct {
	Status string `json:"status"`
	Model  string `json:"model,omitempty"`
	Error  string `json:"error,omitempty"`
}

func slotState(ep Endpoint, err error) slotJSON {
	if err == nil {
		return slotJSON{Status: "ready", Model: ep.Model}
	}
	st := slotJSON{Status: "unavailable", Model: ep.Model, Error: err.Error()}
	var nr *NotReadyError
	if errors.As(err, &nr) {
		st.Status, st.Error = nr.State, nr.Reason
	}
	return st
}

// handleHealth is GET /health (version stamped) and /v1/health. The status code is
// load-bearing: 200 healthy; 200 initializing while live loads (up to 15 min); 200
// not_configured on a fresh install with no model (LD3); 503 degraded when live failed or
// loads past the grace window. model_service keeps the shape admin reads (02 §3.7).
func (m *Module) handleHealth(w http.ResponseWriter, r *http.Request, withVersion bool) {
	liveEP, liveErr := m.resolve(r, LabelLive)
	bgEP, bgErr := m.resolve(r, LabelBackground)
	live, bg := slotState(liveEP, liveErr), slotState(bgEP, bgErr)

	var models []string
	for _, ep := range []Endpoint{liveEP, bgEP} {
		if ep.Model != "" && (len(models) == 0 || models[0] != ep.Model) {
			models = append(models, ep.Model)
		}
	}
	if models == nil {
		models = []string{}
	}
	msStatus := "ok"
	if live.Status != "ready" {
		msStatus = "degraded"
	}
	ms := map[string]any{
		"status":  msStatus,
		"models":  models,
		"aliases": map[string]string{LabelLive: liveEP.Model, LabelBackground: bgEP.Model},
		"slots":   map[string]slotJSON{LabelLive: live, LabelBackground: bg},
	}

	status, body := http.StatusOK, map[string]any{"model_service": ms}
	m.hmu.Lock()
	switch live.Status {
	case "ready":
		m.notReadySince = time.Time{}
		body["status"] = "healthy"
	case StateNotConfigured:
		m.notReadySince = time.Time{}
		body["status"] = "not_configured"
	default:
		if m.notReadySince.IsZero() {
			m.notReadySince = time.Now()
		}
		switch {
		case live.Status == StateFailed || live.Status == "unavailable":
			status, body["status"], body["reason"] = http.StatusServiceUnavailable, "degraded", "Live model failed to load"
		case time.Since(m.notReadySince) < loadingGrace:
			body["status"] = "initializing"
		default:
			status, body["status"], body["reason"] = http.StatusServiceUnavailable, "degraded", "Live model still loading past grace window"
		}
	}
	m.hmu.Unlock()
	if withVersion {
		body["version"] = m.Version
	}
	writeJSON(w, status, body)
}
