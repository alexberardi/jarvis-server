package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
)

// Interactive callbacks (docs/cc/13 §3.6, api/callbacks.py): a tap on a row or button inside an
// inbox card runs either a node command's @callback (node plane: a cc_callback_jobs row, the
// opaque MQTT `callback` command, then the node's authenticated GET + POST result) or a
// server-side handler in jarvisd (server plane: the static map below, run on the durable queue).
//
// Changes from legacy, all decided:
//   - D40 Q9: the server-callback registry is a static map built from the subsystems' handler
//     tables, not a runtime registry.
//   - D40 Q4: a sweeper on the scheduler expires stale pending jobs and deletes finished rows
//     after callbackRetention; a node-plane tap fails fast when MQTT is down, the node is
//     offline or the publish fails, and a new_notification tap that fails fast or expires gets
//     a "couldn't reach" card instead of silence.
//   - §11: the server plane runs as a queue job (cc.callback.server), not a BackgroundTask, and
//     a waiter (proposals' node dispatcher) is woken when a result is recorded.

const (
	callbackTTL       = 5 * time.Minute      // expires_at = created_at + 5 min (CALLBACK_TTL)
	callbackRetention = 7 * 24 * time.Hour   // finished rows are deleted after this (D40 Q4)
	serverCallbackRun = 10 * time.Minute     // queue lease for one server handler (LLM calls)
	callbackSweepRate = time.Minute          // the sweeper trigger's interval
	callbackCategory  = "callback_result"    // default fan-out category
	serverCallbackJob = "cc.callback.server" // queue job type: run one server-plane handler
	callbackSweepJob  = "cc.callback_sweep"  // scheduler trigger + job type
)

// serverGrace: a server-plane job still pending this long after expires_at lost its run (a
// crash mid-handler, the lease ran out): it is recorded failed so the poll terminates.
const serverGrace = serverCallbackRun

var navigationTypes = []string{"stack", "new_notification", "popover"}

// --- the static server-callback map (D40 Q9) ---

// callbackContext is ServerCallbackContext: a snapshot of the job for a server handler.
type callbackContext struct {
	JobID          string
	HouseholdID    string
	UserID         *int64
	Data           map[string]any
	NavigationType string
}

// callbackResult is ServerCallbackResult (it mirrors the node's POST /result body).
type callbackResult struct {
	Success     bool
	Error       string
	ContextData map[string]any
}

// serverCallbackFunc runs one server-plane tap. An error records the job failed with its text
// (a raised exception, legacy).
type serverCallbackFunc func(ctx context.Context, c callbackContext) (callbackResult, error)

func callbackKey(command, callback string) string { return command + "." + callback }

// serverCallbacks is the static registry: every server-plane handler the subsystems built,
// adapted to one dispatcher shape. Built once, after every subsystem is registered.
func (m *Module) serverCallbacks() map[string]serverCallbackFunc {
	m.cbOnce.Do(func() { m.cbMap = m.buildServerCallbacks() })
	return m.cbMap
}

func (m *Module) buildServerCallbacks() map[string]serverCallbackFunc {
	out := map[string]serverCallbackFunc{}
	// Errands (doc 09): approve/refine/discard/approve_replan/stop.
	if m.errands != nil {
		for name, f := range m.errands.Callbacks() {
			f := f
			out[callbackKey(errands.CallbackCommand, name)] = func(ctx context.Context, c callbackContext) (callbackResult, error) {
				r := f(ctx, errands.CallbackContext{JobID: c.JobID, HouseholdID: c.HouseholdID, UserID: derefID(c.UserID),
					Data: c.Data, NavigationType: c.NavigationType})
				return callbackResult(r), nil
			}
		}
	}
	// Phone (doc 11): confirm_call / cancel_call / escalation_answer, keyed "make_phone_call.<cb>".
	if m.phone != nil {
		for key, f := range m.phone.Callbacks() {
			f := f
			out[key] = func(ctx context.Context, c callbackContext) (callbackResult, error) {
				r := f(ctx, phone.CallbackContext{HouseholdID: c.HouseholdID, UserID: derefID(c.UserID), Data: c.Data})
				return callbackResult(r), nil
			}
		}
	}
	// Proposals and the automation confirm card (doc 10).
	for _, p := range SignalCallbackNames() {
		h, ok := m.SignalCallback(p[0], p[1])
		if !ok {
			continue
		}
		out[callbackKey(p[0], p[1])] = func(ctx context.Context, c callbackContext) (callbackResult, error) {
			r := h(ctx, SignalCallbackContext(c))
			return callbackResult(r), nil
		}
	}
	// Errand schedules' list card (doc 08).
	out[callbackKey(ScheduleCallbackCommand, ScheduleCancelCallback)] = func(ctx context.Context, c callbackContext) (callbackResult, error) {
		cd, errMsg, err := m.CancelScheduleTap(ctx, c.HouseholdID, c.UserID, c.Data)
		if err != nil {
			return callbackResult{}, err
		}
		if errMsg != "" {
			return callbackResult{Error: errMsg}, nil
		}
		return callbackResult{Success: true, ContextData: cd}, nil
	}
	return out
}

// ServerCallbackNames lists the registered "<command>.<callback>" keys, sorted
// (registered_server_callbacks).
func (m *Module) ServerCallbackNames() []string {
	var out []string
	for k := range m.serverCallbacks() {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func derefID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// --- result waiters (proposals' node dispatcher waits on a channel, not just the DB) ---

type callbackWaiters struct {
	mu sync.Mutex
	ch map[string]chan struct{}
}

// wait returns a channel closed when jobID's result is recorded.
func (w *callbackWaiters) wait(jobID string) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ch == nil {
		w.ch = map[string]chan struct{}{}
	}
	c, ok := w.ch[jobID]
	if !ok {
		c = make(chan struct{})
		w.ch[jobID] = c
	}
	return c
}

func (w *callbackWaiters) forget(jobID string) {
	w.mu.Lock()
	delete(w.ch, jobID)
	w.mu.Unlock()
}

func (w *callbackWaiters) wake(jobID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if c, ok := w.ch[jobID]; ok {
		close(c)
		delete(w.ch, jobID)
	}
}

// --- registration ---

// registerCallbacks mounts doc 13's callback routes and installs the queue jobs. It runs after
// every subsystem that contributes server handlers.
func (m *Module) registerCallbacks(mux *http.ServeMux) {
	const v0 = "/api/v0"
	mux.HandleFunc("POST "+v0+"/callbacks", m.user(m.handleCreateCallback))
	mux.HandleFunc("GET "+v0+"/callbacks/{job_id}", m.node(m.handleCallbackPayload))
	mux.HandleFunc("POST "+v0+"/callbacks/{job_id}/result", m.node(m.handleCallbackResult))
	mux.HandleFunc("GET "+v0+"/callbacks/{job_id}/status", m.user(m.handleCallbackStatus))
	if m.deps.Queue != nil {
		m.deps.Queue.Register(serverCallbackJob, queue.Handler{
			Run: func(ctx context.Context, j queue.Job) ([]byte, error) {
				m.runServerCallback(ctx, string(j.Payload))
				return nil, nil
			},
			// Handlers are not idempotent (a confirmed call, a launched errand): a run lost to a
			// crash is not retried; the sweeper records it failed (serverGrace).
			Concurrency: 4, MaxAttempts: 1, Lease: serverCallbackRun,
		})
		m.deps.Queue.Register(callbackSweepJob, queue.Handler{Run: func(ctx context.Context, _ queue.Job) ([]byte, error) {
			return nil, m.sweepCallbacks(ctx)
		}})
	}
}

// startCallbacks schedules the sweeper (D27: a trigger, not a loop).
func (m *Module) startCallbacks(ctx context.Context) error {
	if m.deps.Scheduler == nil {
		return nil
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: callbackSweepJob, Kind: scheduler.KindInterval, JobType: callbackSweepJob,
		Spec: scheduler.Spec{Every: callbackSweepRate},
	})
}

// --- the job row ---

type callbackJob struct {
	id, householdID, command, callback, status, nav string
	nodeID                                          sql.NullString
	userID                                          sql.NullInt64
	data, errMsg, result                            sql.NullString
	createdAt, expiresAt                            string
	completedAt                                     sql.NullString
}

func (m *Module) loadCallbackJob(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (*callbackJob, error) {
	var j callbackJob
	err := q.QueryRowContext(ctx, `SELECT id, node_id, household_id, user_id, command_name, callback_name, data_json,
		status, error_message, result_context_data_json, navigation_type, created_at, expires_at, completed_at
		FROM cc_callback_jobs WHERE id = ?`, id).Scan(&j.id, &j.nodeID, &j.householdID, &j.userID, &j.command,
		&j.callback, &j.data, &j.status, &j.errMsg, &j.result, &j.nav, &j.createdAt, &j.expiresAt, &j.completedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (j *callbackJob) user() *int64 {
	if !j.userID.Valid {
		return nil
	}
	v := j.userID.Int64
	return &v
}

// dataMap decodes data_json for a handler (json.Number numbers); malformed reads as {}.
func (j *callbackJob) dataMap() map[string]any {
	out := map[string]any{}
	if j.data.Valid && j.data.String != "" {
		dec := json.NewDecoder(strings.NewReader(j.data.String))
		dec.UseNumber()
		if err := dec.Decode(&out); err != nil || out == nil {
			return map[string]any{}
		}
	}
	return out
}

// jsonObject re-emits a stored JSON object in its own key order; ok=false when absent/malformed.
func jsonObject(s sql.NullString) (json.RawMessage, bool) {
	if !s.Valid || s.String == "" {
		return nil, false
	}
	v, err := pyjson.Loads(s.String)
	if err != nil {
		return nil, false
	}
	if _, isObj := v.(*pyjson.Object); !isObj {
		return nil, false
	}
	return pyjson.ToJSON(v), true
}

func (j *callbackJob) expired(now time.Time) bool {
	return j.status == "pending" && now.After(parseTS(j.expiresAt))
}

func (m *Module) markExpired(ctx context.Context, id string) {
	if _, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_callback_jobs SET status = 'expired'
		WHERE id = ? AND status = 'pending'`, id); err != nil {
		m.deps.Log.Warn("cc: callback expire failed", "job", id, "err", err)
	}
}

// --- POST /callbacks ---

func (m *Module) handleCreateCallback(w http.ResponseWriter, r *http.Request, u authn.User) {
	b, po, ok := readBodyPy(w, r)
	if !ok {
		return
	}
	command, cok := b.str("command_name", true)
	if cok {
		b.strLen("command_name", command, 1, 128)
	}
	callback, cbok := b.str("callback_name", true)
	if cbok {
		b.strLen("callback_name", callback, 1, 128)
	}
	b.object("data", false)
	if v, present := b.m["data"]; present && v == nil {
		b.fail("data", "Input should be a valid dictionary")
	}
	target, _ := b.optStrPtr("target_node_id")
	if target != nil {
		b.strLen("target_node_id", *target, 1, 0)
	}
	hh, _ := b.optStrPtr("household_id")
	if hh != nil {
		b.strLen("household_id", *hh, 1, 0)
	}
	nav := "new_notification"
	if v, present := b.m["navigation_type"]; present {
		s, isStr := v.(string)
		if !isStr || !contains(navigationTypes, s) {
			b.fail("navigation_type", "Input should be 'stack', 'new_notification' or 'popover'")
		} else {
			nav = s
		}
	}
	if !b.done(w) {
		return
	}
	dataJSON := "{}"
	if dv, ok := po.Get("data"); ok && dv != nil {
		dataJSON = pyjson.Dumps(dv, true)
	}
	ctx := r.Context()
	nj := newCallbackJob{command: command, callback: callback, dataJSON: dataJSON, nav: nav, userID: &u.ID}

	if target == nil {
		m.createServerCallback(w, r, u, nj, hh)
		return
	}
	n, err := m.nodeByID(ctx, *target)
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Target node not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	if !n.householdID.Valid || n.householdID.String == "" {
		detail(w, http.StatusBadRequest, "Target node has no household")
		return
	}
	// D5: the caller must be a member of the TARGET node's household, among all their memberships.
	if err := m.requireRole(ctx, u.ID, n.householdID.String, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	nj.householdID, nj.node = n.householdID.String, n
	job, err := m.startNodeCallback(ctx, nj)
	if err != nil && !errors.Is(err, errNodeUnreachable) { // unreachable: 201 with the failed job (D40 Q4)
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("cc: callback job created", "job", job.id, "node", n.nodeID, "command", command,
		"callback", callback, "nav", nav, "user", u.ID, "status", job.status)
	writeCallbackCreated(w, job)
}

func (m *Module) createServerCallback(w http.ResponseWriter, r *http.Request, u authn.User, nj newCallbackJob, hh *string) {
	// The handler must exist before a row is created: an unroutable tap fails at the tap.
	if _, ok := m.serverCallbacks()[callbackKey(nj.command, nj.callback)]; !ok {
		detail(w, http.StatusBadRequest, fmt.Sprintf("No server-side handler registered for %s.%s", nj.command, nj.callback))
		return
	}
	if hh == nil || *hh == "" {
		detail(w, http.StatusBadRequest, "household_id is required when target_node_id is omitted")
		return
	}
	ctx := r.Context()
	if err := m.requireRole(ctx, u.ID, *hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	nj.householdID = *hh
	job, err := m.insertCallbackJob(ctx, nj)
	if err != nil {
		m.internalError(w, err)
		return
	}
	m.dispatchServerCallback(job.id)
	m.deps.Log.Info("cc: server callback job created", "job", job.id, "command", nj.command,
		"callback", nj.callback, "nav", nj.nav, "user", u.ID)
	writeCallbackCreated(w, job)
}

func writeCallbackCreated(w http.ResponseWriter, j *callbackJob) {
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"id": j.id, "status": j.status, "navigation_type": j.nav, "created_at": pyNaive(parseTS(j.createdAt)),
	})
}

type newCallbackJob struct {
	command, callback, dataJSON, nav, householdID, idempotencyKey string
	userID                                                        *int64
	node                                                          *nodeRow // nil: server plane
}

func (m *Module) insertCallbackJob(ctx context.Context, nj newCallbackJob) (*callbackJob, error) {
	now := m.now()
	j := &callbackJob{id: uuid4(), householdID: nj.householdID, command: nj.command, callback: nj.callback,
		status: "pending", nav: nj.nav, createdAt: dbTime(now), expiresAt: dbTime(now.Add(callbackTTL)),
		data: sql.NullString{String: nj.dataJSON, Valid: true}}
	var nodeID any
	if nj.node != nil {
		nodeID = nj.node.nodeID
		j.nodeID = sql.NullString{String: nj.node.nodeID, Valid: true}
	}
	if nj.userID != nil {
		j.userID = sql.NullInt64{Int64: *nj.userID, Valid: true}
	}
	_, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_callback_jobs
		(id, node_id, household_id, user_id, command_name, callback_name, data_json, status, navigation_type,
		 idempotency_key, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		j.id, nodeID, j.householdID, nj.userID, j.command, j.callback, nj.dataJSON, j.nav,
		nullIfBlank(nj.idempotencyKey), j.createdAt, j.expiresAt)
	if err != nil {
		return nil, err
	}
	return j, nil
}

// errNodeUnreachable is a node-plane job failed at creation (D40 Q4 fail fast).
var errNodeUnreachable = errors.New("cc: node unreachable")

// startNodeCallback inserts a node-plane job and publishes [{"command":"callback","details":
// {"request_id": id}}] (the opaque id only). When MQTT is down, the node is offline or the
// publish fails, the job is recorded failed at once (D40 Q4), with a "couldn't reach" card
// for a new_notification tap; the returned job then has status "failed".
func (m *Module) startNodeCallback(ctx context.Context, nj newCallbackJob) (*callbackJob, error) {
	job, err := m.insertCallbackJob(ctx, nj)
	if err != nil {
		return nil, err
	}
	reason := ""
	switch {
	case m.bus == nil || !m.bus.Available():
		reason = "MQTT is not available"
	case !nj.node.online(m.now()):
		reason = "Node is offline"
	default:
		payload := []map[string]any{{"command": "callback", "details": map[string]any{"request_id": job.id}}}
		if err := m.bus.Publish(nj.node.nodeID, "commands", payload); err != nil {
			m.deps.Log.Warn("cc: callback not delivered", "job", job.id, "node", nj.node.nodeID, "err", err)
			reason = "Could not deliver the callback to the node"
		}
	}
	if reason == "" {
		return job, nil
	}
	m.recordCallbackResult(ctx, job, false, reason, nil, "")
	m.postUnreachableCard(ctx, job, nj.node, "Your tap didn't go through: the device is offline or unreachable. Try again once it's back.")
	job.status = "failed"
	return job, errNodeUnreachable
}

// postUnreachableCard tells a new_notification tapper their tap went nowhere (D40 Q4). Inline
// taps (stack/popover) see the failure on their status poll instead.
func (m *Module) postUnreachableCard(ctx context.Context, job *callbackJob, n *nodeRow, summary string) {
	if job.nav != "new_notification" {
		return
	}
	name := job.nodeID.String
	if n != nil && n.room != "" {
		name = n.room
	}
	target := "household"
	if job.userID.Valid {
		target = "user"
	}
	m.postInboxItem(ctx, job.householdID, job.user(), "Couldn't reach "+name, summary, "", callbackCategory,
		map[string]any{"node_id": job.nodeID.String, "household_id": job.householdID}, false, target)
}

// --- the server plane ---

// dispatchServerCallback runs the job off the request: a queue job (survives a restart, capped
// concurrency), or a goroutine when the module has no queue.
func (m *Module) dispatchServerCallback(jobID string) {
	if m.deps.Queue != nil {
		if _, err := m.deps.Queue.Enqueue(context.Background(), serverCallbackJob, []byte(jobID), queue.Options{}); err == nil {
			return
		} else {
			m.deps.Log.Error("cc: server callback enqueue failed; running inline", "job", jobID, "err", err)
		}
	}
	go m.runServerCallback(context.Background(), jobID)
}

// runServerCallback is _run_server_callback_job. It never leaves the job pending: a missing
// handler, an error or a panic records failed (the anti-vanishing rule).
func (m *Module) runServerCallback(ctx context.Context, jobID string) {
	job, err := m.loadCallbackJob(ctx, m.deps.DB.Read, jobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			m.deps.Log.Error("cc: server callback job vanished before execution", "job", jobID)
		} else {
			m.deps.Log.Error("cc: server callback load failed", "job", jobID, "err", err)
		}
		return
	}
	if job.status != "pending" || job.nodeID.Valid {
		return // already ran (a duplicate dispatch) or not a server-plane job
	}
	h, ok := m.serverCallbacks()[callbackKey(job.command, job.callback)]
	if !ok {
		m.recordCallbackResult(ctx, job, false, "Server-side handler no longer registered", nil, "")
		return
	}
	res, err := safeCallback(ctx, h, callbackContext{JobID: job.id, HouseholdID: job.householdID, UserID: job.user(),
		Data: job.dataMap(), NavigationType: job.nav})
	if err != nil {
		m.deps.Log.Error("cc: server callback failed", "command", job.command, "callback", job.callback, "job", job.id, "err", err)
		m.recordCallbackResult(context.WithoutCancel(ctx), job, false, err.Error(), nil, "")
		return
	}
	m.recordCallbackResult(context.WithoutCancel(ctx), job, res.Success, res.Error, res.ContextData, "")
}

func safeCallback(ctx context.Context, h serverCallbackFunc, c callbackContext) (res callbackResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%v", p)
		}
	}()
	return h(ctx, c)
}

// --- the shared result recorder ---

// recordCallbackResult is _record_callback_result, shared by both planes: mark the row, wake a
// waiter, then (new_notification + success + context_data.inbox.title) fan out an inbox card.
// errMsg "" stores NULL; nil contextData leaves the column as it was. sourceNode is the executing
// node (stamped into the card's metadata) or "" for the server plane.
func (m *Module) recordCallbackResult(ctx context.Context, job *callbackJob, success bool, errMsg string,
	contextData map[string]any, sourceNode string) {
	status := "failed"
	if success {
		status = "completed"
	}
	now := m.now()
	q := `UPDATE cc_callback_jobs SET status = ?, error_message = ?, completed_at = ?`
	args := []any{status, nullIfBlank(errMsg), dbTime(now)}
	if contextData != nil {
		var enc any
		if raw, err := json.Marshal(contextData); err == nil {
			enc = string(raw)
		} else {
			m.deps.Log.Warn("cc: callback context_data not serialisable, dropping", "job", job.id, "err", err)
		}
		q += `, result_context_data_json = ?`
		args = append(args, enc)
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, q+` WHERE id = ?`, append(args, job.id)...); err != nil {
		m.deps.Log.Error("cc: callback result write failed", "job", job.id, "err", err)
		return
	}
	job.status = status
	job.errMsg = sql.NullString{String: errMsg, Valid: errMsg != ""}
	job.completedAt = sql.NullString{String: dbTime(now), Valid: true}
	m.cbWait.wake(job.id)
	m.deps.Log.Info("cc: callback job result", "job", job.id, "status", status, "nav", job.nav, "error", errMsg)

	if job.nav != "new_notification" || !success || contextData == nil {
		return
	}
	inbox, _ := contextData["inbox"].(map[string]any)
	title, _ := inbox["title"].(string)
	if inbox == nil || title == "" {
		return
	}
	metadata := map[string]any{}
	switch md := inbox["metadata"].(type) {
	case map[string]any:
		for k, v := range md {
			metadata[k] = v
		}
	case nil:
	default:
		if cbTruthy(md) { // dict(<non-dict>) raised: the fan-out was skipped
			m.deps.Log.Warn("cc: callback inbox fan-out failed: metadata is not a dict", "job", job.id)
			return
		}
	}
	if sourceNode != "" {
		if _, ok := metadata["node_id"]; !ok {
			metadata["node_id"] = sourceNode
		}
	}
	target, _ := inbox["target_type"].(string)
	if target != "user" && target != "household" {
		target = "household"
		if job.userID.Valid {
			target = "user" // the tapping user is known: only they get the buzz
		}
	}
	summary, _ := inbox["summary"].(string)
	bodyText, _ := inbox["body"].(string)
	category, _ := inbox["category"].(string)
	if category == "" {
		category = callbackCategory
	}
	m.postInboxItem(ctx, job.householdID, job.user(), title, summary, bodyText, category, metadata,
		cbTruthy(inbox["create_push_notification"]), target)
}

// --- node routes ---

// ownJob loads a job the calling node owns; anything else is a 404 (no existence leak, §7.10).
// A server-plane row (node_id NULL) never matches.
func (m *Module) ownJob(w http.ResponseWriter, r *http.Request, n *nodeCtx) (*callbackJob, bool) {
	job, err := m.loadCallbackJob(r.Context(), m.deps.DB.Read, r.PathValue("job_id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!job.nodeID.Valid || job.nodeID.String != n.ID)) {
		detail(w, http.StatusNotFound, "Callback job not found")
		return nil, false
	}
	if err != nil {
		m.internalError(w, err)
		return nil, false
	}
	return job, true
}

// handleCallbackPayload is the node's authenticated payload fetch (its verification step).
func (m *Module) handleCallbackPayload(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	job, ok := m.ownJob(w, r, n)
	if !ok {
		return
	}
	switch job.status {
	case "pending", "completed", "failed":
	default:
		detail(w, http.StatusGone, "Callback job is "+job.status)
		return
	}
	if job.expired(m.now()) {
		m.markExpired(r.Context(), job.id)
		detail(w, http.StatusGone, "Callback job expired")
		return
	}
	data, ok := jsonObject(job.data)
	if !ok {
		if job.data.Valid && job.data.String != "" {
			m.deps.Log.Error("cc: callback job has malformed data_json", "job", job.id)
		}
		data = json.RawMessage("{}")
	}
	var user any
	if job.userID.Valid {
		user = job.userID.Int64
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"job_id": job.id, "command_name": job.command, "callback_name": job.callback, "data": data,
		"user_id": user, "voice_command": "cb:" + job.callback, "conversation_id": "callback:" + job.id,
	})
}

// handleCallbackResult is the node's outcome post.
func (m *Module) handleCallbackResult(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	var success bool
	switch v, present := b.m["success"]; {
	case !present:
		b.fail("success", "Field required")
	case v == nil:
		b.fail("success", "Input should be a valid boolean")
	default:
		success, _ = b.boolean("success")
	}
	errMsg, _ := b.optStrPtr("error")
	contextData, _ := b.object("context_data", false)
	if !b.done(w) {
		return
	}
	job, ok := m.ownJob(w, r, n)
	if !ok {
		return
	}
	e := ""
	if errMsg != nil {
		e = *errMsg
	}
	m.recordCallbackResult(r.Context(), job, success, e, contextData, n.ID)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": job.id, "status": job.status, "completed_at": naiveTS(job.completedAt.String),
	})
}

// --- the mobile status poll ---

func (m *Module) handleCallbackStatus(w http.ResponseWriter, r *http.Request, u authn.User) {
	ctx := r.Context()
	job, err := m.loadCallbackJob(ctx, m.deps.DB.Read, r.PathValue("job_id"))
	if errors.Is(err, sql.ErrNoRows) {
		detail(w, http.StatusNotFound, "Callback job not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	// Membership in the job's household, among all the caller's memberships (D5).
	if err := m.requireRole(ctx, u.ID, job.householdID, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	if job.expired(m.now()) {
		m.markExpired(ctx, job.id)
		job.status = "expired"
	}
	var cd any
	if v, ok := jsonObject(job.result); ok {
		cd = v
	} else if job.result.Valid && job.result.String != "" {
		m.deps.Log.Warn("cc: callback job has malformed result_context_data_json", "job", job.id)
	}
	var completed, errMsg any
	if job.completedAt.Valid {
		completed = naiveTS(job.completedAt.String)
	}
	if job.errMsg.Valid {
		errMsg = job.errMsg.String
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"id": job.id, "status": job.status, "navigation_type": job.nav, "completed_at": completed,
		"error_message": errMsg, "context_data": cd,
	})
}

// --- the sweeper (D40 Q4) ---

// sweepCallbacks expires node-plane jobs past expires_at (with a "couldn't reach" card for a
// new_notification tap), fails server-plane jobs that lost their run, and deletes finished rows
// older than callbackRetention.
func (m *Module) sweepCallbacks(ctx context.Context) error {
	now := m.now()
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT id FROM cc_callback_jobs
		WHERE status = 'pending' AND ((node_id IS NOT NULL AND expires_at < ?) OR (node_id IS NULL AND expires_at < ?))`,
		dbTime(now), dbTime(now.Add(-serverGrace)))
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		job, err := m.loadCallbackJob(ctx, m.deps.DB.Read, id)
		if err != nil || job.status != "pending" {
			continue
		}
		if !job.nodeID.Valid {
			m.recordCallbackResult(ctx, job, false, "Server callback did not complete", nil, "")
			continue
		}
		res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_callback_jobs SET status = 'expired'
			WHERE id = ? AND status = 'pending'`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		m.cbWait.wake(id)
		node, _ := m.nodeByID(ctx, job.nodeID.String)
		m.postUnreachableCard(ctx, job, node, "The device didn't answer in time, so your tap didn't run. Try again.")
	}
	_, err = m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_callback_jobs WHERE status != 'pending' AND created_at < ?`,
		dbTime(now.Add(-callbackRetention)))
	return err
}

// --- small helpers ---

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// cbTruthy is Python truthiness for values decoded by encoding/json (json.Number, plain maps) or
// built by Go handlers.
func cbTruthy(v any) bool {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return err != nil || f != 0
	case map[string]any:
		return len(x) > 0
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return pyTruthyValue(v)
}
