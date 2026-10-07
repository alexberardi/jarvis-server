package cc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Routines (docs/cc/08, app/api/routines.py): server-owned, per-household bundles of node
// commands. Three audiences share these routes:
//
//   - mobile (provisioning auth: the admin key, or a member's JWT): CRUD and run-now under
//     /households/{hh}/routines;
//   - the node (X-API-Key): GET /nodes/{id}/routines, the pull-on-nudge source, args
//     flattened to {k: v};
//   - the scheduler: a routine with an enabled schedule is a trigger (D25, D27).
//
// Routines execute ON THE NODE (D24): run-now and scheduled runs publish a `routine` command
// carrying the full definition, wait for the node's result on /device-control-results, and
// never speak (D25). Every mutation nudges the household's active nodes to re-pull. The server
// owns every definition (D44): the node defaults are seeded as rows once per household.

const (
	routineFireJob       = "cc.routine_fire"
	routineTriggerPrefix = "cc.routine:"
)

// routineWait is how long a run waits for the node's result (D40 08.Q7; a var for tests).
var routineWait = 60 * time.Second

// validLengths, sorted: the 422 detail prints Python's sorted(set).
var validLengths = []string{"long", "medium", "short"}

// routineState is the module's routine runtime: the single-flight set (D40 08.Q10) and the
// errand hook a fired schedule calls (doc 09 sets it).
type routineState struct {
	mu      sync.Mutex
	running map[string]bool
	// scheduleFire drafts the errand plan for a fired schedule (doc 09's
	// draft_errand_plan_detached). Nil until errands land: the fire is logged and dropped.
	scheduleFire func(ctx context.Context, s ScheduledErrand) error
}

// acquire marks a routine running; false when a run is already in flight.
func (s *routineState) acquire(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *routineState) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// routineDefinitions adds the settings routines read. smart_home.primary_node_id belongs to
// doc 07; it is declared here only when nothing else declared it yet.
func routineDefinitions(defs []settings.Definition) []settings.Definition {
	if slices.ContainsFunc(defs, func(d settings.Definition) bool { return d.Key == settingPrimaryNode }) {
		return defs
	}
	return append(defs, settings.Definition{Key: settingPrimaryNode, Category: "smart_home", Type: settings.String,
		Default: "", Description: "The household's primary node: the default target for routine run-now and " +
			"scheduled runs (and doc 07's node preference)."})
}

// registerRoutines mounts the routine and schedule routes and the scheduler job handlers.
func (m *Module) registerRoutines(mux *http.ServeMux) {
	m.rt = &routineState{running: map[string]bool{}}
	const v0 = "/api/v0"
	mux.HandleFunc("GET "+v0+"/households/{household_id}/routines", m.handleListRoutines)
	mux.HandleFunc("POST "+v0+"/households/{household_id}/routines", m.handleCreateRoutine)
	mux.HandleFunc("GET "+v0+"/households/{household_id}/routines/{routine_id}", m.handleGetRoutine)
	mux.HandleFunc("PATCH "+v0+"/households/{household_id}/routines/{routine_id}", m.handlePatchRoutine)
	mux.HandleFunc("DELETE "+v0+"/households/{household_id}/routines/{routine_id}", m.handleDeleteRoutine)
	mux.HandleFunc("POST "+v0+"/households/{household_id}/routines/{routine_id}/run-now", m.handleRunRoutineNow)
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/routines", m.node(m.handleNodePullRoutines))
	// Errand schedules (mobile_schedules.py).
	mux.HandleFunc("GET "+v0+"/mobile/household/{household_id}/schedules", m.user(m.handleListSchedules))
	mux.HandleFunc("POST "+v0+"/mobile/household/{household_id}/schedules/{schedule_id}/cancel", m.user(m.handleCancelSchedule))

	if q := m.deps.Queue; q != nil {
		// One attempt: a retry would re-run a routine the node may already have run.
		q.Register(routineFireJob, queue.Handler{Run: m.runRoutineFire, Concurrency: 4, MaxAttempts: 1})
		q.Register(scheduleFireJob, queue.Handler{Run: m.runScheduleFire, MaxAttempts: 3})
		q.Register(schedulePurgeJob, queue.Handler{Run: m.runSchedulePurge})
	}
}

// startRoutines reconciles the triggers with the stored routines and schedules (rows
// imported or written while a trigger write failed) and schedules the terminal-schedule purge.
func (m *Module) startRoutines(ctx context.Context) error {
	if err := m.reconcileRoutineTriggers(ctx); err != nil {
		return err
	}
	if err := m.reconcileScheduleTriggers(ctx); err != nil {
		return err
	}
	return m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: schedulePurgeJob, Kind: scheduler.KindInterval, JobType: schedulePurgeJob,
		Spec: scheduler.Spec{Every: 24 * time.Hour, StartNow: true},
	})
}

// --- shapes ---

type routineArg struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// routineStep is the mobile-native step stored in cc_routines.steps.
type routineStep struct {
	Command string       `json:"command"`
	Args    []routineArg `json:"args"`
	Label   string       `json:"label"`
}

// routineSchedule is the mobile-facing schedule JSON (RoutineSchedule.model_dump(), in its
// field order). last_fired_at is server-written.
type routineSchedule struct {
	Type            string  `json:"type"`
	Cron            *string `json:"cron"`
	IntervalSeconds *int64  `json:"interval_seconds"`
	Timezone        string  `json:"timezone"`
	TargetNodeID    *string `json:"target_node_id"`
	Enabled         bool    `json:"enabled"`
	LastFiredAt     *string `json:"last_fired_at"`
}

// timing is what decides when the schedule fires; a save that keeps it keeps the trigger
// (and its next fire), fixing the reset-on-every-save bug (§8.4).
func (s *routineSchedule) timing() string {
	if s == nil {
		return ""
	}
	cron, iv := "", int64(0)
	if s.Cron != nil {
		cron = *s.Cron
	}
	if s.IntervalSeconds != nil {
		iv = *s.IntervalSeconds
	}
	return fmt.Sprintf("%s|%s|%d|%s|%t", s.Type, cron, iv, s.Timezone, s.Enabled)
}

func (s *routineSchedule) target() string {
	if s == nil || s.TargetNodeID == nil {
		return ""
	}
	return *s.TargetNodeID
}

// location is the zone the cron evaluates in. D40 08.Q6 wants the target node's zone, then
// the household's; jarvisd stores neither yet, so the schedule's own zone (validated at save)
// is used, as legacy did. An empty zone is UTC.
func (s *routineSchedule) location() *time.Location {
	if s.Timezone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(s.Timezone); err == nil {
		return loc
	}
	return time.UTC
}

type routineRow struct {
	id, householdID, slug, name     string
	triggerPhrases, steps           string
	responseInstruction, respLength string
	schedule                        sql.NullString
	enabled                         bool
	createdAt, updatedAt            string
}

const routineCols = `id, household_id, slug, name, trigger_phrases, steps, response_instruction, response_length,
	schedule, enabled, created_at, updated_at`

func scanRoutine(s scanner) (*routineRow, error) {
	var r routineRow
	err := s.Scan(&r.id, &r.householdID, &r.slug, &r.name, &r.triggerPhrases, &r.steps, &r.responseInstruction,
		&r.respLength, &r.schedule, &r.enabled, &r.createdAt, &r.updatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// sched parses the stored schedule (nil when absent or unreadable).
func (r *routineRow) sched() *routineSchedule {
	if !r.schedule.Valid || r.schedule.String == "" {
		return nil
	}
	var s routineSchedule
	s.Enabled = true // a stored schedule without the key is enabled (schedule.get("enabled", True))
	if err := json.Unmarshal([]byte(r.schedule.String), &s); err != nil {
		return nil
	}
	return &s
}

// rawJSON is a stored JSON column as-is, or def when it isn't valid JSON.
func rawJSON(s string, def string) json.RawMessage {
	if s != "" && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage(def)
}

// mobile is _routine_to_mobile: args stay the [{key, value}] array.
func (r *routineRow) mobile() map[string]any {
	var sched any
	if r.schedule.Valid && r.schedule.String != "" {
		sched = rawJSON(r.schedule.String, "null")
	}
	length := r.respLength
	if length == "" {
		length = "short"
	}
	return map[string]any{
		"id": r.id, "slug": r.slug, "name": r.name,
		"trigger_phrases":      rawJSON(r.triggerPhrases, "[]"),
		"steps":                rawJSON(r.steps, "[]"),
		"response_instruction": r.responseInstruction,
		"response_length":      length,
		"schedule":             sched,
		"enabled":              r.enabled,
		"created_at":           naiveTS(r.createdAt),
		"updated_at":           naiveTS(r.updatedAt),
	}
}

// nodeRoutine is the node-native definition (_routine_to_node), the shape the node pull and
// the D24 `routine` command carry.
type nodeRoutine struct {
	TriggerPhrases      json.RawMessage `json:"trigger_phrases"`
	Steps               []nodeStep      `json:"steps"`
	ResponseInstruction string          `json:"response_instruction"`
	ResponseLength      string          `json:"response_length"`
}

type nodeStep struct {
	Command any            `json:"command"`
	Args    map[string]any `json:"args"`
	Label   any            `json:"label"`
}

func (r *routineRow) node() nodeRoutine {
	var steps []map[string]any
	dec := json.NewDecoder(strings.NewReader(r.steps))
	dec.UseNumber()
	_ = dec.Decode(&steps)
	out := nodeRoutine{
		TriggerPhrases: rawJSON(r.triggerPhrases, "[]"), Steps: make([]nodeStep, 0, len(steps)),
		ResponseInstruction: r.responseInstruction, ResponseLength: r.respLength,
	}
	if out.ResponseLength == "" {
		out.ResponseLength = "short"
	}
	for _, s := range steps {
		label, ok := s["label"]
		if !ok {
			label = ""
		}
		out.Steps = append(out.Steps, nodeStep{Command: s["command"], Args: flattenArgs(s["args"]), Label: label})
	}
	return out
}

// flattenArgs is _flatten_args: [{key, value}] → {k: v}. A string value starting with [ or {
// is decoded as JSON (resolved_datetimes → ["today"]), falling back to the raw string; every
// other value stays as stored and the node coerces it.
func flattenArgs(v any) map[string]any {
	out := map[string]any{}
	list, _ := v.([]any)
	for _, p := range list {
		pair, ok := p.(map[string]any)
		if !ok {
			continue
		}
		key, _ := pair["key"].(string)
		if key == "" {
			continue
		}
		val, present := pair["value"]
		if !present {
			val = ""
		}
		if s, isStr := val.(string); isStr && (strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{")) {
			dec := json.NewDecoder(strings.NewReader(s))
			dec.UseNumber()
			var decoded any
			if err := dec.Decode(&decoded); err == nil && !dec.More() {
				val = decoded
			}
		}
		out[key] = val
	}
	return out
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify is _slugify: lowercased, non-alphanumeric runs to "_", fallback "routine".
func slugify(name string) string {
	s := strings.Trim(slugRE.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_"), "_")
	if s == "" {
		return "routine"
	}
	return s
}

// uniqueSlug is _unique_slug inside the write transaction: a collision gets _2, _3, ...
func uniqueSlug(ctx context.Context, tx *sql.Tx, householdID, name string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT slug FROM cc_routines WHERE household_id = ?`, householdID)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return "", err
		}
		taken[s] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	base := slugify(name)
	if !taken[base] {
		return base, nil
	}
	for i := 2; ; i++ {
		if s := fmt.Sprintf("%s_%d", base, i); !taken[s] {
			return s, nil
		}
	}
}

// --- request bodies (RoutineCreate / RoutineUpdate, pydantic-shaped) ---

type routineInput struct {
	name                           *string
	triggerPhrases                 []string
	steps                          []routineStep
	responseInstruction, respLen   *string
	schedule                       *routineSchedule
	hasPhrases, hasSteps, hasSched bool
	enabled                        *bool
}

// parseRoutineBody validates a create (patch=false: defaults apply, name required) or a
// PATCH body (every field optional; a null leaves the field alone, except schedule: null
// clears it).
func parseRoutineBody(b *body, patch bool) routineInput {
	var in routineInput
	if s, ok := b.str("name", !patch); ok {
		in.name = &s
	}
	if v, present := b.m["trigger_phrases"]; present && (v != nil || !patch) {
		if v == nil {
			b.fail("trigger_phrases", "Input should be a valid list")
		} else if l, ok := b.strList("trigger_phrases"); ok {
			in.triggerPhrases, in.hasPhrases = l, true
		}
	}
	if v, present := b.m["steps"]; present && (v != nil || !patch) {
		in.steps, in.hasSteps = parseSteps(b, v)
	}
	if s, ok := b.str("response_instruction", false); ok {
		in.responseInstruction = &s
	} else if !patch && b.has("response_instruction") && b.m["response_instruction"] == nil {
		b.fail("response_instruction", "Input should be a valid string")
	}
	if s, ok := b.str("response_length", false); ok {
		in.respLen = &s
	} else if !patch && b.has("response_length") && b.m["response_length"] == nil {
		b.fail("response_length", "Input should be a valid string")
	}
	if v, present := b.m["schedule"]; present {
		in.hasSched = true
		if v != nil {
			in.schedule = parseSchedule(b, v)
		}
	}
	if v, present := b.m["enabled"]; present && v != nil {
		if e, ok := b.boolean("enabled"); ok {
			in.enabled = &e
		}
	} else if present && !patch {
		b.fail("enabled", "Input should be a valid boolean")
	}
	return in
}

func parseSteps(b *body, v any) ([]routineStep, bool) {
	list, ok := v.([]any)
	if !ok {
		b.fail("steps", "Input should be a valid list")
		return nil, false
	}
	out := make([]routineStep, 0, len(list))
	valid := true
	for i, e := range list {
		obj, isObj := e.(map[string]any)
		sb := &body{m: obj, loc: fmt.Sprintf("%s -> steps -> %d", b.loc, i), errs: b.errs}
		if !isObj {
			*b.errs = append(*b.errs, sb.loc+": Input should be a valid dictionary or object to extract fields from")
			valid = false
			continue
		}
		st := routineStep{Args: []routineArg{}}
		cmd, okCmd := sb.str("command", true)
		st.Command = cmd
		valid = valid && okCmd
		if l, ok := sb.str("label", false); ok {
			st.Label = l
		} else if sb.has("label") && obj["label"] == nil {
			sb.fail("label", "Input should be a valid string")
			valid = false
		}
		if av, present := obj["args"]; present {
			al, isList := av.([]any)
			if !isList {
				sb.fail("args", "Input should be a valid list")
				valid = false
			}
			for j, a := range al {
				ao, isObj := a.(map[string]any)
				ab := &body{m: ao, loc: fmt.Sprintf("%s -> args -> %d", sb.loc, j), errs: b.errs}
				if !isObj {
					*b.errs = append(*b.errs, ab.loc+": Input should be a valid dictionary or object to extract fields from")
					valid = false
					continue
				}
				k, okK := ab.str("key", true)
				arg := routineArg{Key: k}
				if val, ok := ab.str("value", false); ok {
					arg.Value = val
				} else if ab.has("value") {
					if ao["value"] == nil {
						ab.fail("value", "Input should be a valid string")
					}
					valid = false
				}
				valid = valid && okK
				st.Args = append(st.Args, arg)
			}
		}
		out = append(out, st)
	}
	return out, valid
}

func parseSchedule(b *body, v any) *routineSchedule {
	obj, ok := v.(map[string]any)
	if !ok {
		b.fail("schedule", "Input should be a valid dictionary or object to extract fields from")
		return nil
	}
	sb := &body{m: obj, loc: b.loc + " -> schedule", errs: b.errs}
	s := &routineSchedule{Timezone: "UTC", Enabled: true}
	s.Type, _ = sb.str("type", true)
	s.Cron, _ = sb.optStrPtr("cron")
	if n, ok := sb.integer("interval_seconds", false); ok {
		s.IntervalSeconds = &n
	}
	if sb.has("timezone") {
		if tz, ok := sb.str("timezone", true); ok {
			s.Timezone = tz
		}
	}
	s.TargetNodeID, _ = sb.optStrPtr("target_node_id")
	if sb.has("enabled") {
		if e, ok := sb.boolean("enabled"); ok {
			s.Enabled = e
		} else if obj["enabled"] == nil {
			sb.fail("enabled", "Input should be a valid boolean")
		}
	}
	s.LastFiredAt, _ = sb.optStrPtr("last_fired_at")
	return s
}

// --- validation and serialization (422s, as legacy HTTPExceptions) ---

func validateLength(v string) error {
	if !slices.Contains(validLengths, v) {
		return fail(http.StatusUnprocessableEntity, "response_length must be one of ['long', 'medium', 'short']")
	}
	return nil
}

// validateSchedule is _validate_schedule plus what legacy only found at fire time (D40 08.Q6,
// §11): a cron that doesn't parse and an unknown zone are 422s at save.
func validateSchedule(s *routineSchedule) error {
	switch s.Type {
	case "cron":
		if s.Cron == nil || strings.TrimSpace(*s.Cron) == "" {
			return fail(http.StatusUnprocessableEntity, "cron schedule requires a 'cron' expression")
		}
		if _, err := scheduler.ParseCron(*s.Cron); err != nil {
			return fail(http.StatusUnprocessableEntity, "cron schedule has an invalid 'cron' expression: "+*s.Cron)
		}
	case "interval":
		if s.IntervalSeconds == nil || *s.IntervalSeconds <= 0 {
			return fail(http.StatusUnprocessableEntity, "interval schedule requires interval_seconds > 0")
		}
	default:
		return fail(http.StatusUnprocessableEntity, "schedule.type must be 'cron' or 'interval'")
	}
	if s.Timezone != "" {
		if _, err := time.LoadLocation(s.Timezone); err != nil || s.Timezone == "Local" {
			return fail(http.StatusUnprocessableEntity, fmt.Sprintf("schedule.timezone '%s' is not a known time zone", s.Timezone))
		}
	}
	return nil
}

// scheduleJSON is _schedule_to_json: validate, default the target to the household's primary
// node (at save time), and keep the stored last_fired_at when the client didn't send one (the
// mobile editor resends the schedule without it on every save, §8.4). A target that is a node
// of ANOTHER household is refused: a routine never runs outside its household (D24).
func (m *Module) scheduleJSON(ctx context.Context, hh string, s *routineSchedule, prev *routineSchedule) (sql.NullString, error) {
	if s == nil {
		return sql.NullString{}, nil
	}
	if err := validateSchedule(s); err != nil {
		return sql.NullString{}, err
	}
	if s.TargetNodeID == nil || *s.TargetNodeID == "" {
		if p := m.settings.String(ctx, settingPrimaryNode, settings.Scope{HouseholdID: hh}); p != "" {
			s.TargetNodeID = &p
		} else {
			s.TargetNodeID = nil
		}
	} else if n, err := m.nodeByID(ctx, *s.TargetNodeID); err == nil && n.householdID.String != hh {
		return sql.NullString{}, fail(http.StatusUnprocessableEntity, "schedule.target_node_id is not a node in this household")
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, err
	}
	if s.LastFiredAt == nil && prev != nil {
		s.LastFiredAt = prev.LastFiredAt
	}
	b, err := json.Marshal(s)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: string(b), Valid: true}, nil
}

// --- handlers ---

// pathHouseholdAccess is verify_provisioning_auth + require_household_access for the path's
// household (householdAccess, smarthome.go). ok=false means it wrote the error.
func (m *Module) pathHouseholdAccess(w http.ResponseWriter, r *http.Request, auth provAuth) bool {
	if err := m.householdAccess(r.Context(), auth, r.PathValue("household_id")); err != nil {
		m.writeErr(w, err)
		return false
	}
	return true
}

func (m *Module) routineByID(ctx context.Context, hh, id string) (*routineRow, error) {
	r, err := scanRoutine(m.deps.DB.Read.QueryRowContext(ctx,
		`SELECT `+routineCols+` FROM cc_routines WHERE id = ? AND household_id = ?`, id, hh))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fail(http.StatusNotFound, "Routine not found")
	}
	return r, err
}

func (m *Module) handleListRoutines(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	if err := m.seedRoutines(ctx, hh); err != nil {
		m.internalError(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT `+routineCols+` FROM cc_routines WHERE household_id = ? ORDER BY created_at, rowid`, hh)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		rt, err := scanRoutine(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out = append(out, rt.mobile())
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"routines": out})
}

func (m *Module) handleCreateRoutine(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	in := parseRoutineBody(b, false)
	if !b.done(w) || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	if in.name == nil || strings.TrimSpace(*in.name) == "" {
		detail(w, http.StatusUnprocessableEntity, "name is required")
		return
	}
	length := "short"
	if in.respLen != nil {
		length = *in.respLen
	}
	if err := validateLength(length); err != nil {
		m.writeErr(w, err)
		return
	}
	if err := m.seedRoutines(ctx, hh); err != nil {
		m.internalError(w, err)
		return
	}
	sched, err := m.scheduleJSON(ctx, hh, in.schedule, nil)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	phrases := in.triggerPhrases
	if phrases == nil {
		phrases = []string{}
	}
	steps := in.steps
	if steps == nil {
		steps = []routineStep{}
	}
	pj, _ := json.Marshal(phrases)
	sj, _ := json.Marshal(steps)
	instr := ""
	if in.responseInstruction != nil {
		instr = *in.responseInstruction
	}
	enabled := in.enabled == nil || *in.enabled
	id, now := uuid4(), dbTime(m.now())
	err = m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		slug, err := uniqueSlug(ctx, tx, hh, *in.name)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO cc_routines (id, household_id, slug, name, trigger_phrases, steps,
			response_instruction, response_length, schedule, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, hh, slug, strings.TrimSpace(*in.name), string(pj), string(sj), instr, length, sched, enabled, now, now)
		return err
	})
	if err != nil {
		m.internalError(w, err)
		return
	}
	rt, err := m.routineByID(ctx, hh, id)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	m.syncRoutineTrigger(ctx, rt, "", true)
	m.nudgeRoutines(ctx, hh)
	httpx.WriteJSON(w, http.StatusCreated, rt.mobile())
}

func (m *Module) handleGetRoutine(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	rt, err := m.routineByID(r.Context(), r.PathValue("household_id"), r.PathValue("routine_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rt.mobile())
}

func (m *Module) handlePatchRoutine(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok {
		return
	}
	b, _, ok := readBody(w, r, false)
	if !ok {
		return
	}
	in := parseRoutineBody(b, true)
	if !b.done(w) || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	rt, err := m.routineByID(ctx, hh, r.PathValue("routine_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	prevTiming := m.routineTiming(rt)
	// The slug never changes: it is the node's data key.
	if in.name != nil && *in.name != "" {
		rt.name = strings.TrimSpace(*in.name)
	}
	if in.hasPhrases {
		pj, _ := json.Marshal(in.triggerPhrases)
		rt.triggerPhrases = string(pj)
	}
	if in.hasSteps {
		sj, _ := json.Marshal(in.steps)
		rt.steps = string(sj)
	}
	if in.responseInstruction != nil {
		rt.responseInstruction = *in.responseInstruction
	}
	if in.respLen != nil {
		if err := validateLength(*in.respLen); err != nil {
			m.writeErr(w, err)
			return
		}
		rt.respLength = *in.respLen
	}
	if in.hasSched {
		if rt.schedule, err = m.scheduleJSON(ctx, hh, in.schedule, rt.sched()); err != nil {
			m.writeErr(w, err)
			return
		}
	}
	if in.enabled != nil {
		rt.enabled = *in.enabled
	}
	_, err = m.deps.DB.Write.ExecContext(ctx, `UPDATE cc_routines SET name = ?, trigger_phrases = ?, steps = ?,
		response_instruction = ?, response_length = ?, schedule = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		rt.name, rt.triggerPhrases, rt.steps, rt.responseInstruction, rt.respLength, rt.schedule, rt.enabled,
		dbTime(m.now()), rt.id)
	if err != nil {
		m.internalError(w, err)
		return
	}
	if rt, err = m.routineByID(ctx, hh, rt.id); err != nil {
		m.writeErr(w, err)
		return
	}
	m.syncRoutineTrigger(ctx, rt, prevTiming, false)
	m.nudgeRoutines(ctx, hh)
	httpx.WriteJSON(w, http.StatusOK, rt.mobile())
}

func (m *Module) handleDeleteRoutine(w http.ResponseWriter, r *http.Request) {
	auth, ok := m.authProvisioning(w, r)
	if !ok || !m.pathHouseholdAccess(w, r, auth) {
		return
	}
	ctx, hh := r.Context(), r.PathValue("household_id")
	rt, err := m.routineByID(ctx, hh, r.PathValue("routine_id"))
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM cc_routines WHERE id = ?`, rt.id); err != nil {
		m.internalError(w, err)
		return
	}
	m.deleteRoutineTrigger(ctx, rt.id)
	m.nudgeRoutines(ctx, hh)
	w.WriteHeader(http.StatusNoContent)
}

// handleNodePullRoutines is the node's pull-on-nudge source: the household's ENABLED
// routines, slug-keyed, args flattened. A node may only pull its own (403 Node mismatch); a
// node with no household gets an empty set.
func (m *Module) handleNodePullRoutines(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
	if r.PathValue("node_id") != n.ID {
		detail(w, http.StatusForbidden, "Node mismatch")
		return
	}
	out := map[string]nodeRoutine{}
	if n.HouseholdID == "" {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"routines": out})
		return
	}
	ctx := r.Context()
	if err := m.seedRoutines(ctx, n.HouseholdID); err != nil {
		m.internalError(w, err)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx,
		`SELECT `+routineCols+` FROM cc_routines WHERE household_id = ? AND enabled = 1`, n.HouseholdID)
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		rt, err := scanRoutine(rows)
		if err != nil {
			m.internalError(w, err)
			return
		}
		out[rt.slug] = rt.node()
	}
	if err := rows.Err(); err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"routines": out})
}

// nudgeRoutines is publish_routines_sync: a routines_changed nudge (no routine data) to every
// active node in the household. A publish failure never fails the request.
func (m *Module) nudgeRoutines(ctx context.Context, hh string) {
	if !m.bus.Available() {
		m.deps.Log.Warn("cc: MQTT unavailable; routines sync nudge skipped", "household", hh)
		return
	}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT node_id FROM cc_nodes WHERE household_id = ? AND is_active = 1`, hh)
	if err != nil {
		m.deps.Log.Warn("cc: routines sync nudge: list nodes", "household", hh, "err", err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	payload := map[string]any{"event": "routines_changed", "household_id": hh}
	for _, id := range ids {
		if err := m.bus.Publish(id, "routines/sync", payload); err != nil {
			m.deps.Log.Warn("cc: routines/sync publish failed", "node", id, "err", err)
		}
	}
	m.deps.Log.Info("cc: routines/sync nudged", "nodes", len(ids), "household", hh)
}

// --- D44: the node defaults, seeded once per household ---

type seedRoutine struct {
	name, length, instruction string
	phrases                   []string
	steps                     []routineStep
}

func seedStep(command, label string, args ...string) routineStep {
	st := routineStep{Command: command, Label: label, Args: []routineArg{}}
	for i := 0; i+1 < len(args); i += 2 {
		st.Args = append(st.Args, routineArg{Key: args[i], Value: args[i+1]})
	}
	return st
}

// defaultRoutines are node-setup's RoutineCommand._default_routines, in the mobile-native
// shape (array args stored as JSON strings, as the editor stores them).
var defaultRoutines = []seedRoutine{
	{name: "Good Morning", length: "short",
		phrases:     []string{"good morning", "morning routine", "start my day"},
		instruction: "Give a cheerful morning briefing with weather and calendar highlights.",
		steps: []routineStep{
			seedStep("control_device", "lights", "floor", "Downstairs", "action", "turn_on"),
			seedStep("get_weather", "weather", "resolved_datetimes", `["today"]`),
			seedStep("get_calendar_events", "calendar", "resolved_datetimes", `["today"]`),
		}},
	{name: "Good Night", length: "short",
		phrases:     []string{"good night", "bedtime", "going to bed", "time for bed"},
		instruction: "Brief goodnight with tomorrow's first appointment if any.",
		steps: []routineStep{
			seedStep("control_device", "lights", "floor", "Downstairs", "action", "turn_off"),
			seedStep("get_calendar_events", "tomorrow", "resolved_datetimes", `["tomorrow"]`),
		}},
	{name: "Morning Briefing", length: "medium",
		phrases: []string{"morning briefing", "daily briefing", "give me my briefing", "what's happening today",
			"catch me up", "daily update"},
		instruction: "Deliver a morning briefing in a natural, flowing narrative style. " +
			"Start with today's weather, then mention calendar events, " +
			"then summarize the top news headlines. Sound like a personal " +
			"news anchor, not a list of bullet points.",
		steps: []routineStep{
			seedStep("get_weather", "weather", "resolved_datetimes", `["today"]`),
			seedStep("get_calendar_events", "calendar", "resolved_datetimes", `["today"]`),
			seedStep("get_news", "news", "category", "general", "count", "3"),
		}},
	{name: "Nightly Briefing", length: "medium",
		phrases: []string{"nightly briefing", "evening briefing", "nightly update", "evening update",
			"what happened today", "end of day briefing"},
		instruction: "Deliver an evening briefing in a calm, winding-down tone. " +
			"Start with tomorrow's weather outlook, then mention any " +
			"calendar events for tomorrow, then summarize today's top " +
			"news headlines. Keep it relaxed and conversational.",
		steps: []routineStep{
			seedStep("get_weather", "tomorrow_weather", "resolved_datetimes", `["tomorrow"]`),
			seedStep("get_calendar_events", "tomorrow_calendar", "resolved_datetimes", `["tomorrow"]`),
			seedStep("get_news", "news", "category", "general", "count", "3"),
		}},
}

// seedRoutines seeds the defaults the first time a household's routines are touched. A slug
// the household already has (e.g. an imported "Good Morning") is left alone, and a default
// the user later deletes stays deleted.
func (m *Module) seedRoutines(ctx context.Context, hh string) error {
	if hh == "" {
		return nil
	}
	var seeded int
	if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_routine_seeds WHERE household_id = ?`, hh).Scan(&seeded); err != nil || seeded > 0 {
		return err
	}
	now := dbTime(m.now())
	return m.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO cc_routine_seeds (household_id, seeded_at) VALUES (?, ?)
			ON CONFLICT (household_id) DO NOTHING`, hh, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		for _, d := range defaultRoutines {
			pj, _ := json.Marshal(d.phrases)
			sj, _ := json.Marshal(d.steps)
			if _, err := tx.ExecContext(ctx, `INSERT INTO cc_routines (id, household_id, slug, name, trigger_phrases,
				steps, response_instruction, response_length, schedule, enabled, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, 1, ?, ?) ON CONFLICT (household_id, slug) DO NOTHING`,
				uuid4(), hh, slugify(d.name), d.name, string(pj), string(sj), d.instruction, d.length, now, now); err != nil {
				return err
			}
		}
		return nil
	})
}
