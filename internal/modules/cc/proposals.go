package cc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Proposals (proposal_card.py, proposable_action_service.py, proposal_suppressions.py,
// api/proposals.py) and the automation confirm card (signal_automation_executor.py, D7).
//
// Card taps arrive through the doc-13 server-callback plane (POST /callbacks without
// target_node_id), which checks the caller's household membership and then calls the handler
// SignalCallback returns. That plane is not ported yet; these handlers are its registrations
// for jarvis.proposable_action.{execute,dismiss,suppress} and
// jarvis.signal_automation.{execute,dismiss}.

const proposableCommand = "jarvis.proposable_action"

// SignalCallbackContext is ServerCallbackContext for the proposal/automation card taps.
type SignalCallbackContext struct {
	JobID          string
	HouseholdID    string
	UserID         *int64
	Data           map[string]any
	NavigationType string
}

// SignalCallbackResult is ServerCallbackResult (it mirrors the node's POST /result body).
type SignalCallbackResult struct {
	Success     bool
	Error       string
	ContextData map[string]any
}

// SignalCallbackHandler handles one server-plane tap. It never panics on bad input.
type SignalCallbackHandler func(ctx context.Context, sc SignalCallbackContext) SignalCallbackResult

// SignalCallback returns the server-plane handler for (command, callback), for the doc-13
// /callbacks registry.
func (m *Module) SignalCallback(command, callback string) (SignalCallbackHandler, bool) {
	switch command + "." + callback {
	case proposableCommand + ".execute":
		return m.proposalExecute, true
	case proposableCommand + ".dismiss", automationCommand + ".dismiss":
		if command == automationCommand {
			return m.automationDismiss, true
		}
		return func(context.Context, SignalCallbackContext) SignalCallbackResult {
			return SignalCallbackResult{Success: true}
		}, true
	case proposableCommand + ".suppress":
		return m.proposalSuppress, true
	case automationCommand + ".execute":
		return m.automationExecute, true
	}
	return nil, false
}

// SignalCallbackNames lists the (command, callback) pairs SignalCallback serves.
func SignalCallbackNames() [][2]string {
	return [][2]string{
		{proposableCommand, "execute"}, {proposableCommand, "dismiss"}, {proposableCommand, "suppress"},
		{automationCommand, "execute"}, {automationCommand, "dismiss"},
	}
}

// --- the card emitter (proposal_card.py) ---

type proposalCard struct {
	HouseholdID    string
	NodeID         string
	Command        string
	Callback       string
	Params         *pyjson.Object // declared params; stringified at the top level of data
	IdempotencyKey string
	CardTitle      string
	ConfirmLabel   string
	DismissLabel   string
	BlastTier      string
	Summary        string
	Body           string
	UserID         *int64
	Source         string
	Descriptor     string
}

// emitProposalCard is emit_proposal_card: the SDK-shaped tap-to-confirm card (§7.9). D40 Q4:
// "Never suggest this" is enforced here, centrally, for every proposer. Best effort: false on
// any failure.
func (m *Module) emitProposalCard(ctx context.Context, c proposalCard) bool {
	if c.ConfirmLabel == "" {
		c.ConfirmLabel = "Yes"
	}
	if c.DismissLabel == "" {
		c.DismissLabel = "No"
	}
	if c.BlastTier == "" {
		c.BlastTier = "reversible"
	}
	if c.Source != "" && m.suppressed(ctx, c.HouseholdID, c.UserID, c.Command, c.Source) {
		m.deps.Log.Info("cc: proposal suppressed by the user", "command", c.Command, "source", c.Source)
		return false
	}
	confirmData := map[string]any{"_action": map[string]any{
		"target_command": c.Command, "target_callback": c.Callback, "node_id": nullIfBlank(c.NodeID),
		"idempotency_key": c.IdempotencyKey, "blast_tier": c.BlastTier,
	}}
	if c.Params != nil {
		for _, k := range c.Params.Keys() {
			v, _ := c.Params.Get(k)
			confirmData[k] = pyjson.Str(v)
		}
	}
	elements := []any{
		map[string]any{"id": "confirm-" + c.IdempotencyKey, "label": c.ConfirmLabel, "kind": "confirm",
			"command": proposableCommand, "callback": "execute", "target": "server", "data": confirmData,
			"navigation_type": "new_notification"},
		map[string]any{"id": "dismiss-" + c.IdempotencyKey, "label": c.DismissLabel, "kind": "dismiss",
			"command": proposableCommand, "callback": "dismiss", "target": "server",
			"data":            map[string]any{"_action": map[string]any{"idempotency_key": c.IdempotencyKey}},
			"navigation_type": "new_notification"},
	}
	if c.Source != "" || c.Descriptor != "" {
		elements = append(elements, map[string]any{"id": "suppress-" + c.IdempotencyKey, "label": "Never suggest this",
			"kind": "suppress", "command": proposableCommand, "callback": "suppress", "target": "server",
			"data": map[string]any{"_action": map[string]any{"target_command": c.Command, "idempotency_key": c.IdempotencyKey},
				"source": c.Source, "descriptor": c.Descriptor},
			"navigation_type": "new_notification"})
	}
	summary := c.Summary
	if summary == "" {
		summary = c.CardTitle
	}
	target := "household"
	if c.UserID != nil {
		target = "user"
	}
	id := m.postInboxItem(ctx, c.HouseholdID, c.UserID, c.CardTitle, summary, c.Body, "proposal",
		map[string]any{"interactive_elements": elements}, true, target)
	if id == "" {
		m.deps.Log.Warn("cc: emit_proposal_card: inbox post returned no id", "command", c.Command)
		return false
	}
	return true
}

// stableIdempotencyKey is proposal_matcher._stable_idempotency_key: "match:" + sha256 of
// json.dumps({"d", "c", "a"}, sort_keys=True)[:16]. Byte-exact (§11): existing keys and
// suppressions keep matching.
func stableIdempotencyKey(data any, command, action string) string {
	blob := pyjson.Dumps(map[string]any{"d": pySorted(data), "c": command, "a": action}, true)
	h := sha256.Sum256([]byte(blob))
	return "match:" + hex.EncodeToString(h[:])[:16]
}

// emitDirectedProposal is _emit_directed_proposal: resolve the named command against a
// household node's advertised proposable actions and post its card. A command nobody
// advertises is refused (false); the signal is stored regardless. Runs synchronously because
// the response reports `proposed`, bounded by the 6 s resolution cap.
func (m *Module) emitDirectedProposal(ctx context.Context, hh, nodeID, command string, data any, sourceKey string, userID *int64) bool {
	cmd, cb := command, ""
	if i := strings.Index(command, "."); i >= 0 {
		cmd, cb = command[:i], command[i+1:]
	}
	if nodeID == "" { // scope.node_id is already household-checked at ingest
		nodeID = m.resolveNodeForCommand(ctx, hh, cmd, cb)
	}
	if nodeID == "" {
		return false
	}
	rep, err := m.nodeTools(ctx, nodeID, probeTimeout)
	if err != nil {
		m.deps.Log.Warn("cc: directed proposal resolve failed", "command", command, "err", err)
		return false
	}
	var action map[string]any
	if cb != "" {
		action = rep.proposable(cmd, cb)
	} else {
		action = rep.firstProposable(cmd)
	}
	if action == nil {
		return false
	}
	callback := anyStrOr(action["callback"], cmd)
	idemParam, _ := action["idempotency_param"].(string)
	declared := map[string]bool{}
	for _, p := range paramSpecs(action) {
		if n, _ := p["name"].(string); n != "" {
			declared[n] = true
		}
	}
	args, _ := data.(*pyjson.Object)
	if args == nil {
		args = pyjson.NewObject()
	}
	params := pyjson.NewObject()
	for _, k := range args.Keys() {
		if declared[k] && k != idemParam {
			v, _ := args.Get(k)
			params.Set(k, v)
		}
	}
	idem := stableIdempotencyKey(map[string]any{"items": []any{args}}, cmd, callback)
	title := anyStrOr(action["card_title"], "Run "+cmd+"?")
	// Legacy posted directed cards household-wide (no user_id); kept.
	_ = userID
	return m.emitProposalCard(ctx, proposalCard{HouseholdID: hh, NodeID: nodeID, Command: cmd, Callback: callback,
		Params: params, IdempotencyKey: idem, CardTitle: title, Summary: title, Source: sourceKey})
}

func paramSpecs(action map[string]any) []map[string]any {
	l, _ := action["params"].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, e := range l {
		if p, ok := e.(map[string]any); ok {
			out = append(out, p)
		}
	}
	return out
}

// validateAgainstParams is validate_against_params: exactly the declared params, required
// present, enums checked, no coercion.
func validateAgainstParams(raw map[string]any, specs []map[string]any) (map[string]any, error) {
	args := map[string]any{}
	for _, spec := range specs {
		name, _ := spec["name"].(string)
		v, present := raw[name]
		if !present || v == nil || v == "" {
			if pyTruthyValue(spec["required"]) {
				return nil, fmt.Errorf("missing required field '%s'", name)
			}
			continue
		}
		if enum, ok := spec["enum_values"].([]any); ok && len(enum) > 0 {
			s, found := fmt.Sprint(jsonScalar(v)), false
			for _, e := range enum {
				if fmt.Sprint(jsonScalar(e)) == s {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("'%s' must be one of %s", name, pyjson.Repr(toPy(enum)))
			}
		}
		args[name] = v
	}
	return args, nil
}

func jsonScalar(v any) any {
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	return v
}

// toPy converts encoding/json values to pyjson ones for repr.
func toPy(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	pv, err := pyjson.Loads(string(raw))
	if err != nil {
		return v
	}
	return pv
}

// --- suppressions (proposal_suppressions.py) ---

// suppressed is the central "never suggest this" check (D40 Q4), keyed on (household, the
// card's user — or any user for a household-wide card —, command, source).
func (m *Module) suppressed(ctx context.Context, hh string, userID *int64, command, source string) bool {
	q := `SELECT 1 FROM cc_proposal_suppressions WHERE household_id = ? AND command = ? AND source_key = ?`
	args := []any{hh, command, source}
	if userID != nil {
		q += ` AND user_id = ?`
		args = append(args, *userID)
	}
	var one int
	err := m.deps.DB.Read.QueryRowContext(ctx, q+` LIMIT 1`, args...).Scan(&one)
	return err == nil
}

// recordSuppression dedups on (household, user, command, source_key) and refreshes the row.
func (m *Module) recordSuppression(ctx context.Context, hh string, userID *int64, command, source, descriptor string) (string, error) {
	now := dbTime(m.now())
	if source != "" {
		var id string
		err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT id FROM cc_proposal_suppressions
			WHERE household_id = ? AND user_id IS ? AND command = ? AND source_key = ?`, hh, userID, command, source).Scan(&id)
		if err == nil {
			_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_proposal_suppressions
				SET descriptor = COALESCE(?, descriptor), created_at = ? WHERE id = ?`, nullIfBlank(descriptor), now, id)
			return id, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	id := "sup_" + randHex(16)
	_, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_proposal_suppressions
		(id, household_id, user_id, command, source_key, descriptor, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, hh, userID, command, nullIfBlank(source), nullIfBlank(descriptor), now)
	return id, err
}

type suppressionRow struct {
	id, command        string
	source, descriptor sql.NullString
	createdAt          string
}

func (m *Module) listSuppressions(ctx context.Context, hh string, userID *int64, command *string) ([]suppressionRow, error) {
	q := `SELECT id, command, source_key, descriptor, created_at FROM cc_proposal_suppressions WHERE household_id = ?`
	args := []any{hh}
	if userID != nil {
		q += ` AND user_id = ?`
		args = append(args, *userID)
	}
	if command != nil {
		q += ` AND command = ?`
		args = append(args, *command)
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, q+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []suppressionRow
	for rows.Next() {
		var s suppressionRow
		if err := rows.Scan(&s.id, &s.command, &s.source, &s.descriptor, &s.createdAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GET /proposals/suppressions: the detector agent's blocklist signals, household from the node.
func (m *Module) handleSuppressionSignals(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	q := r.URL.Query()
	var errs []string
	command := q.Get("command")
	if _, ok := q["command"]; !ok {
		errs = append(errs, "query -> command: Field required")
	}
	uid, uok := int64(0), false
	if v, ok := q["user_id"]; !ok {
		errs = append(errs, "query -> user_id: Field required")
	} else if _, err := fmt.Sscan(v[0], &uid); err != nil || fmt.Sprint(uid) != strings.TrimSpace(v[0]) {
		errs = append(errs, "query -> user_id: Input should be a valid integer, unable to parse string as an integer")
	} else {
		uok = true
	}
	if len(errs) > 0 || !uok {
		validationError(w, errs...)
		return
	}
	if n.HouseholdID == "" {
		detail(w, http.StatusBadRequest, "Node has no household")
		return
	}
	rows, err := m.listSuppressions(r.Context(), n.HouseholdID, &uid, &command)
	if err != nil {
		m.internalError(w, err)
		return
	}
	seen := map[string]bool{}
	sources, descriptors := []string{}, []string{}
	for _, s := range rows {
		if s.source.String != "" && !seen[s.source.String] {
			seen[s.source.String] = true
			sources = append(sources, s.source.String)
		}
		if s.descriptor.String != "" {
			descriptors = append(descriptors, s.descriptor.String)
		}
	}
	sort.Strings(sources)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"source_keys": sources, "descriptors": descriptors})
}

func (m *Module) handleListMySuppressions(w http.ResponseWriter, r *http.Request, u authn.User) {
	q := r.URL.Query()
	if _, ok := q["household_id"]; !ok {
		validationError(w, "query -> household_id: Field required")
		return
	}
	hh := q.Get("household_id")
	var command *string
	if _, ok := q["command"]; ok {
		c := q.Get("command")
		command = &c
	}
	ctx := r.Context()
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	uid := u.ID
	rows, err := m.listSuppressions(ctx, hh, &uid, command)
	if err != nil {
		m.internalError(w, err)
		return
	}
	out := make([]any, 0, len(rows))
	for _, s := range rows {
		out = append(out, map[string]any{"id": s.id, "command": s.command, "source_key": nullStrSQL(s.source),
			"descriptor": nullStrSQL(s.descriptor), "created_at": naiveTS(s.createdAt)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"suppressions": out})
}

func (m *Module) handleDeleteMySuppression(w http.ResponseWriter, r *http.Request, u authn.User) {
	q := r.URL.Query()
	if _, ok := q["household_id"]; !ok {
		validationError(w, "query -> household_id: Field required")
		return
	}
	hh := q.Get("household_id")
	ctx := r.Context()
	if err := m.requireRole(ctx, u.ID, hh, authn.RoleMember); err != nil {
		m.writeErr(w, err)
		return
	}
	res, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_proposal_suppressions WHERE id = ? AND household_id = ? AND user_id = ?`,
		r.PathValue("suppression_id"), hh, u.ID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		detail(w, http.StatusNotFound, "Suppression not found")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func nullStrSQL(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// --- the proposable-action dispatcher (proposable_action_service.py) ---

func inboxCard(hh, title, summary string) map[string]any {
	meta := map[string]any{}
	if hh != "" {
		meta["household_id"] = hh
	}
	return map[string]any{"inbox": map[string]any{"title": title, "summary": summary, "metadata": meta}}
}

func actionMeta(data map[string]any) map[string]any {
	a, _ := data["_action"].(map[string]any)
	if a == nil {
		return map[string]any{}
	}
	return a
}

// proposalExecute is _handle_execute, steps A–F. Every failure carries a visible card.
func (m *Module) proposalExecute(ctx context.Context, sc SignalCallbackContext) SignalCallbackResult {
	meta := actionMeta(sc.Data)
	command, _ := meta["target_command"].(string)
	callback, _ := meta["target_callback"].(string)
	idem, _ := meta["idempotency_key"].(string)
	raw := map[string]any{}
	for k, v := range sc.Data {
		if k != "_action" {
			raw[k] = v
		}
	}
	hh := sc.HouseholdID
	if command == "" || callback == "" {
		return SignalCallbackResult{Error: "malformed proposal"}
	}
	// (A) fail-closed household gate.
	if !m.proposalsEnabled(ctx, hh) {
		return SignalCallbackResult{Error: "proposals disabled",
			ContextData: inboxCard(hh, "Action not run", "Agent action cards are turned off for this household.")}
	}
	// (B) the node. D4: a card's node must belong to the caller's household.
	nodeID, _ := meta["node_id"].(string)
	if nodeID != "" && !m.nodeInHousehold(ctx, nodeID, hh) {
		m.deps.Log.Warn("cc: proposal refused: node outside the household", "node", nodeID, "household", hh)
		return SignalCallbackResult{Error: "no node",
			ContextData: inboxCard(hh, "Couldn't run that", "No device is available to run it.")}
	}
	if nodeID == "" {
		nodeID = m.resolveNodeForCommand(ctx, hh, command, callback)
	}
	if nodeID == "" {
		return SignalCallbackResult{Error: "no node",
			ContextData: inboxCard(hh, "Couldn't run that", "No device is available to run it.")}
	}
	// (C) opt-in: the command must advertise this callback as proposable.
	var spec map[string]any
	if rep, err := m.nodeTools(ctx, nodeID, probeTimeout); err == nil {
		spec = rep.proposable(command, callback)
	}
	if spec == nil {
		return SignalCallbackResult{Error: "not proposable",
			ContextData: inboxCard(hh, "Couldn't run that", fmt.Sprintf("No device here can run '%s'.", command))}
	}
	// (D) the declared params only.
	args, verr := validateAgainstParams(raw, paramSpecs(spec))
	if verr != nil {
		return SignalCallbackResult{Error: "invalid params: " + verr.Error(),
			ContextData: inboxCard(hh, "Couldn't run that", "Something was missing: "+verr.Error()+".")}
	}
	// (E) idempotency.
	if m.callbackCompleted(ctx, hh, idem) {
		return SignalCallbackResult{Success: true, ContextData: inboxCard(hh, "Already done", "This was already handled.")}
	}
	// (F) the @callback on the node.
	ok, result, errMsg := m.runNodeCallback(ctx, nodeID, hh, sc.UserID, command, callback, args, idem)
	if !ok {
		e, summary := errMsg, errMsg
		if e == "" {
			e, summary = "execution failed", "The device couldn't complete it."
		}
		return SignalCallbackResult{Error: e, ContextData: inboxCard(hh, "Couldn't finish that", summary)}
	}
	spoken, _ := result["message"].(string)
	if spoken == "" {
		spoken = "Done"
	}
	return SignalCallbackResult{Success: true, ContextData: inboxCard(hh, spoken, "")}
}

func (m *Module) callbackCompleted(ctx context.Context, hh, idem string) bool {
	if idem == "" {
		return false
	}
	var one int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT 1 FROM cc_callback_jobs
		WHERE household_id = ? AND idempotency_key = ? AND status = 'completed' LIMIT 1`, hh, idem).Scan(&one)
	return err == nil
}

// runNodeCallback is _execute_target_callback_on_node: a node-plane cc_callback_jobs row
// (navigation_type "stack": this dispatcher owns the user-facing card), the MQTT `callback`
// command, then wait for the row to leave pending. The node reads the job and posts its result
// through the doc-13 callback routes, which update the row.
func (m *Module) runNodeCallback(ctx context.Context, nodeID, hh string, userID *int64, command, callback string,
	args map[string]any, idem string) (bool, map[string]any, string) {
	jobID := uuid4()
	data, _ := json.Marshal(args)
	now := m.now()
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_callback_jobs
		(id, node_id, household_id, user_id, command_name, callback_name, data_json, status, navigation_type,
		 idempotency_key, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 'stack', ?, ?, ?)`,
		jobID, nodeID, hh, userID, command, callback, string(data), nullIfBlank(idem), dbTime(now), dbTime(now.Add(5*time.Minute))); err != nil {
		m.deps.Log.Error("cc: callback job insert failed", "err", err)
		return false, nil, "execution failed"
	}
	m.bus.CommandWithID(nodeID, "callback", nil, jobID)
	deadline := time.NewTimer(callbackTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(callbackPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, nil, "the device didn't respond in time"
		case <-deadline.C:
			return false, nil, "the device didn't respond in time"
		case <-tick.C:
		}
		var status string
		var errMsg, result sql.NullString
		err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT status, error_message, result_context_data_json
			FROM cc_callback_jobs WHERE id = ?`, jobID).Scan(&status, &errMsg, &result)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil, "job vanished"
		}
		if err != nil {
			continue
		}
		switch status {
		case "completed":
			out := map[string]any{}
			if result.Valid && result.String != "" {
				_ = json.Unmarshal([]byte(result.String), &out)
			}
			return true, out, ""
		case "failed":
			if errMsg.String != "" {
				return false, nil, errMsg.String
			}
			return false, nil, "callback failed on the node"
		}
	}
}

// proposalSuppress is _handle_suppress: record a blocklist row; nothing runs.
func (m *Module) proposalSuppress(ctx context.Context, sc SignalCallbackContext) SignalCallbackResult {
	command, _ := actionMeta(sc.Data)["target_command"].(string)
	source, _ := sc.Data["source"].(string)
	descriptor, _ := sc.Data["descriptor"].(string)
	if command == "" || (source == "" && descriptor == "") {
		return SignalCallbackResult{Error: "nothing to suppress"}
	}
	if _, err := m.recordSuppression(ctx, sc.HouseholdID, sc.UserID, command, source, descriptor); err != nil {
		m.deps.Log.Warn("cc: record_suppression failed", "err", err) // never fail a tap on bookkeeping
	}
	return SignalCallbackResult{Success: true, ContextData: inboxCard(sc.HouseholdID, "Won't suggest that again",
		"You won't get more cards like this one. Manage these in the app.")}
}

// --- the automation confirm card (D7) ---

// emitAutomationCard is _emit_confirm_card, hardened by D7: the chosen action is stored
// server-side (cc_automation_actions) and the card carries only its opaque id.
func (m *Module) emitAutomationCard(ctx context.Context, hh string, userID *int64, nodeID, command string,
	args map[string]any, label, instruction string) bool {
	argsJSON, _ := json.Marshal(args)
	h := sha256.Sum256([]byte(pyjson.Dumps(toPy(args), true)))
	idem := "sigauto:" + hh + ":" + command + ":" + hex.EncodeToString(h[:8])
	id := "aa_" + randHex(16)
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO cc_automation_actions
		(id, household_id, node_id, command_name, arguments_json, idempotency_key, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, hh, nodeID, command, string(argsJSON), idem, dbTime(m.now())); err != nil {
		m.deps.Log.Warn("cc: automation action not stored", "err", err)
		return false
	}
	ref := map[string]any{"action_id": id, "idempotency_key": idem}
	elements := []any{
		map[string]any{"id": "confirm-" + idem, "label": "Do it", "kind": "confirm", "command": automationCommand,
			"callback": "execute", "target": "server", "data": map[string]any{"_action": ref}, "navigation_type": "new_notification"},
		map[string]any{"id": "dismiss-" + idem, "label": "Not now", "kind": "dismiss", "command": automationCommand,
			"callback": "dismiss", "target": "server", "data": map[string]any{"_action": ref}, "navigation_type": "new_notification"},
	}
	target := "household"
	if userID != nil {
		target = "user"
	}
	item := m.postInboxItem(ctx, hh, userID, "Confirm automation", label+": "+instruction,
		"Your automation for \""+label+"\" wants to run a sensitive action ("+command+"). Tap to confirm.",
		"proposal", map[string]any{"interactive_elements": elements}, true, target)
	if item == "" {
		_, _ = m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_automation_actions WHERE id = ?`, id)
		return false
	}
	_, _ = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_automation_actions SET inbox_item_id = ? WHERE id = ?`, item, id)
	return true
}

// automationExecute runs exactly the stored action, once, after checking it and its node
// belong to the caller's household (D7). A failed run frees the action for another tap.
func (m *Module) automationExecute(ctx context.Context, sc SignalCallbackContext) SignalCallbackResult {
	id, _ := actionMeta(sc.Data)["action_id"].(string)
	if id == "" {
		return SignalCallbackResult{Error: "malformed automation action"}
	}
	var nodeID, command, argsJSON string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT a.node_id, a.command_name, a.arguments_json
		FROM cc_automation_actions a JOIN cc_nodes n ON n.node_id = a.node_id
		WHERE a.id = ? AND a.household_id = ? AND n.household_id = ?`, id, sc.HouseholdID, sc.HouseholdID).Scan(&nodeID, &command, &argsJSON)
	if err != nil {
		return SignalCallbackResult{Error: "unknown automation action"}
	}
	res, err := m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_automation_actions SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		dbTime(m.now()), id)
	if err != nil {
		return SignalCallbackResult{Error: "dispatch failed"}
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SignalCallbackResult{Success: true, ContextData: map[string]any{"inbox": map[string]any{
			"title": "Already done", "summary": "This was already handled.", "body": ""}}}
	}
	args := map[string]any{}
	dec := json.NewDecoder(strings.NewReader(argsJSON))
	dec.UseNumber()
	_ = dec.Decode(&args)
	out := m.dispatchNodeCommand(ctx, nodeID, command, args, sc.UserID, dispatchTimeout)
	if !dispatchOK(out) {
		_, _ = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_automation_actions SET consumed_at = NULL WHERE id = ?`, id)
		msg, _ := out["error"].(string)
		if msg == "" {
			msg = "dispatch failed"
		}
		return SignalCallbackResult{Error: msg}
	}
	return SignalCallbackResult{Success: true, ContextData: map[string]any{"inbox": map[string]any{
		"title": "Done", "summary": "Ran " + command, "body": ""}}}
}

// automationDismiss closes the card and forgets the stored action.
func (m *Module) automationDismiss(ctx context.Context, sc SignalCallbackContext) SignalCallbackResult {
	if id, _ := actionMeta(sc.Data)["action_id"].(string); id != "" {
		_, _ = m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_automation_actions WHERE id = ? AND household_id = ? AND consumed_at IS NULL`,
			id, sc.HouseholdID)
	}
	return SignalCallbackResult{Success: true}
}
