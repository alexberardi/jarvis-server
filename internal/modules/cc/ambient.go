package cc

import (
	"net/http"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Ambient-noise calibration (doc 05 §3.9): the node verifies the command, measures, and posts
// the result; mobile polls. Results live in memory for 5 minutes (calibration, not history).

const ambientTTL = 300 * time.Second

type ambientEntry struct {
	nodeID      string
	issued      time.Time
	result      map[string]any // nil until the node posts
	completedAt string
}

type ambientStore struct {
	mu sync.Mutex
	m  map[string]*ambientEntry
}

func newAmbientStore() *ambientStore { return &ambientStore{m: map[string]*ambientEntry{}} }

func (s *ambientStore) gcLocked(now time.Time) {
	for k, e := range s.m {
		if now.Sub(e.issued) > ambientTTL {
			delete(s.m, k)
		}
	}
}

func (m *Module) handleTriggerAmbient(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, _, ok := readBody(w, r, true)
	if !ok {
		return
	}
	dur, has := b.number("duration_seconds")
	if !b.done(w) {
		return
	}
	if !has || dur == 0 {
		dur = 3.0
	}
	dur = max(1.0, min(10.0, dur))
	id := r.PathValue("node_id")
	// D4: the household check legacy lacked (§8.4).
	if _, err := m.requireNodeAccess(r.Context(), u, id, authn.RoleMember, "Not authorized"); err != nil {
		m.writeErr(w, err)
		return
	}
	rid := uuid4()
	now := m.now()
	m.ambient.mu.Lock()
	m.ambient.gcLocked(now)
	m.ambient.m[rid] = &ambientEntry{nodeID: id, issued: now}
	m.ambient.mu.Unlock()
	m.bus.CommandWithID(id, "measure_ambient_noise", map[string]any{"duration_seconds": dur}, rid)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"request_id": rid, "status": "sent"})
}

var ambientFloatFields = []string{"duration_seconds", "p50_rms", "p75_rms", "p95_rms", "max_rms"}

func (m *Module) handleAmbientResult(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	id, rid := r.PathValue("node_id"), r.PathValue("request_id")
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	res := map[string]any{}
	if !b.has("success") {
		b.fail("success", "Field required")
	} else if v, ok := b.boolean("success"); ok {
		res["success"] = v
	}
	for _, f := range ambientFloatFields {
		res[f] = nil
		if v, ok := b.number(f); ok {
			res[f] = v
		}
	}
	for _, f := range []string{"chunks", "suggested_silence_threshold"} {
		res[f] = nil
		if v, ok := b.integer(f, false); ok {
			res[f] = v
		}
	}
	res["error"] = nil
	if v, ok := b.str("error", false); ok {
		res["error"] = v
	}
	if !b.done(w) {
		return
	}
	if n.ID != id {
		detail(w, http.StatusForbidden, "Node may only post results for itself")
		return
	}
	now := m.now()
	m.ambient.mu.Lock()
	defer m.ambient.mu.Unlock()
	m.ambient.gcLocked(now)
	e, ok := m.ambient.m[rid]
	// D4: only a measurement CC asked this node for.
	if !ok || e.nodeID != id {
		detail(w, http.StatusNotFound, "Not found")
		return
	}
	e.result = res
	e.completedAt = now.UTC().Format("2006-01-02T15:04:05.000000") + "Z"
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (m *Module) handlePollAmbient(w http.ResponseWriter, r *http.Request, u authn.User) {
	id, rid := r.PathValue("node_id"), r.PathValue("request_id")
	ctx := r.Context()
	if _, err := m.requireNodeAccess(ctx, u, id, authn.RoleMember, "Not authorized"); err != nil {
		if se, ok := err.(*statusErr); ok && se.status == http.StatusNotFound {
			detail(w, http.StatusNotFound, "Not found")
			return
		}
		m.writeErr(w, err)
		return
	}
	m.ambient.mu.Lock()
	m.ambient.gcLocked(m.now())
	e, ok := m.ambient.m[rid]
	var result map[string]any
	var completed string
	if ok {
		result, completed = e.result, e.completedAt
	}
	m.ambient.mu.Unlock()
	if ok && e.nodeID != id {
		detail(w, http.StatusNotFound, "Not found")
		return
	}
	if result == nil {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"request_id": rid, "status": "pending", "completed_at": nil, "result": nil})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"request_id": rid, "status": "completed", "completed_at": completed, "result": result})
}
