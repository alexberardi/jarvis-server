package errands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// The workflow engine (workflow_engine.py + errand_executor.py, docs/cc/09 §3.4–3.6), built
// on the durable queue (D27, 09.Q2 option c on b): every drive and resume of a run is one
// cc.errand.resume job, the atomic waiting→running claim is the dedup, and the step handlers
// are a small StepHandler interface. A run starts "waiting" with no signal (waiting_on NULL)
// and the first job claims it, so a restart before the first step simply runs it, while a run
// caught mid-step fails with a card (D40 09.Q3).

// Signal kinds a run can wait on. "start" is the initial drive.
const (
	signalStart    = "start"
	signalPhone    = "phone_call"
	signalApproval = "approval"
)

// Suspend is a deferred step pausing the run on a signal.
type Suspend struct {
	Cursor  int    // the next step to run on resume
	Kind    string // phone_call | approval
	Key     string // the correlation key (a phone session id)
	Results []Result
}

// runCtx is the per-run context threaded to the handlers (WorkflowContext). It replaces the
// legacy synthetic conversation-cache entry: server tools get an explicit Turn (§11).
type runCtx struct {
	workflowID  string
	householdID string
	nodeID      string
	userID      *int64
	goal        string
	timezone    string
	convID      string
	results     *[]Result
	dateCtx     *dates.Context
}

// StepHandler is one capability plane. Handlers are tried in order and the first match runs
// the step: phone → replan → server tool → node (the catch-all).
type StepHandler interface {
	Match(s *Service, command string) bool
	// Run returns the step's outcome, or a Suspend when a deferred step paused the run.
	Run(ctx context.Context, s *Service, rc *runCtx, command string, args *pyjson.Object, label string, i int) (Result, *Suspend)
}

func (s *Service) handlers() []StepHandler {
	return []StepHandler{phoneHandler{}, replanHandler{}, serverHandler{}, nodeHandler{}}
}

// runStep is run_step: a handler panic becomes a failed outcome (one bad step never escapes).
func (s *Service) runStep(ctx context.Context, rc *runCtx, cmd string, args *pyjson.Object, label string, i int) (r Result, susp *Suspend) {
	var h StepHandler
	for _, c := range s.handlers() {
		if c.Match(s, cmd) {
			h = c
			break
		}
	}
	defer func() {
		if p := recover(); p != nil {
			s.log().Error("errands: step crashed", "command", cmd, "panic", p)
			r, susp = Result{Command: cmd, Label: label, Error: fmt.Sprint(p)}, nil
		}
	}()
	r, susp = h.Run(ctx, s, rc, cmd, args, label, i)
	if susp == nil && r.Label == "" {
		r.Label = label
	}
	return r, susp
}

// --- phone (deferred) ---

type phoneHandler struct{}

func (phoneHandler) Match(_ *Service, cmd string) bool { return cmd == cmdPhone }

func (phoneHandler) Run(ctx context.Context, s *Service, rc *runCtx, cmd string, args *pyjson.Object, label string, i int) (Result, *Suspend) {
	if rc.workflowID == "" {
		return Result{Command: cmd, Label: label, Error: "call step has no workflow link"}, nil
	}
	business := parse.PyStrip(truthyStr(args, "business"))
	goal := parse.PyStrip(firstNonEmpty(truthyStr(args, "goal"), rc.goal))
	if s.Phone != nil && business != "" && goal != "" {
		sid, err := s.Phone.PlaceErrandCall(ctx, ErrandCall{
			HouseholdID: rc.householdID, UserID: rc.userID, Business: business, Goal: goal,
			WorkflowID: rc.workflowID, Step: i, PriorContext: priorStepsContext(*rc.results),
		})
		if err != nil {
			s.log().Error("errands: call step failed to draft", "business", business, "err", err)
		} else if sid != "" {
			return Result{}, &Suspend{Cursor: i + 1, Kind: signalPhone, Key: sid}
		}
	}
	return Result{Command: cmd, Label: label, Error: "couldn't set up the call"}, nil
}

// priorStepsContext is _prior_steps_context: the earlier steps, for the next call's brief.
func priorStepsContext(results []Result) string {
	var lines []string
	for _, r := range results {
		label := parse.PyStrip(firstNonEmpty(r.Label, "Step"))
		detail := parse.PyStrip(firstNonEmpty(r.Summary, r.Message, r.Error))
		if detail != "" {
			lines = append(lines, "- "+label+": "+detail)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Earlier in this errand (background — use only if the business asks or it's " +
		"relevant):\n" + strings.Join(lines, "\n")
}

func outcomeData(o CallOutcome) *pyjson.Object {
	if o.OutcomeJSON == "" {
		return nil
	}
	v, err := pyjson.Loads(o.OutcomeJSON)
	if err != nil {
		return nil
	}
	obj, _ := v.(*pyjson.Object)
	return obj
}

// goalAchieved is _outcome_goal_achieved: true / false / unknown (nil).
func goalAchieved(o CallOutcome) *bool {
	d := outcomeData(o)
	if d == nil {
		return nil
	}
	v, _ := d.Get("goal_achieved")
	if b, ok := v.(bool); ok {
		return &b
	}
	return nil
}

// outcomeSummary is _outcome_summary: the wrap-up plus confirmed facts.
func outcomeSummary(o CallOutcome) string {
	d := outcomeData(o)
	if d == nil {
		return ""
	}
	summary := parse.PyStrip(truthyStr(d, "summary"))
	if fv, ok := d.Get("facts"); ok {
		if facts, ok := fv.([]any); ok && len(facts) > 0 {
			parts := make([]string, len(facts))
			for i, f := range facts {
				parts[i] = pyjson.Str(f)
			}
			summary = parse.PyStrip(summary + " (confirmed: " + strings.Join(parts, ", ") + ")")
		}
	}
	return summary
}

// interpretCall is PhoneCallHandler.interpret_signal: a call that connected continues the
// errand only on an explicit goal_achieved == true (D45 strict fail-fast).
func interpretCall(o CallOutcome, steps []Step, i int) Result {
	label := "Call"
	if i >= 0 && i < len(steps) && steps[i].Label != "" {
		label = steps[i].Label
	}
	who := firstNonEmpty(o.ContactName, "the business")
	if o.State == "done" {
		achieved := goalAchieved(o)
		summary := outcomeSummary(o)
		if achieved != nil && *achieved {
			return Result{Command: cmdPhone, Label: label, Success: true,
				Message: "Called " + who + " and achieved the goal.", Summary: summary}
		}
		reason := "reached " + who + " but couldn't confirm the goal was achieved"
		if achieved != nil {
			reason = "reached " + who + " but the goal wasn't achieved"
		}
		return Result{Command: cmdPhone, Label: label, Summary: summary, Error: reason}
	}
	reason, ok := map[string]string{
		"failed":   "the call didn't go through",
		"declined": "the call was declined",
		"expired":  "the call plan expired before it was confirmed",
	}[o.State]
	if !ok {
		reason = "the call ended as " + o.State
	}
	return Result{Command: cmdPhone, Label: label, Error: firstNonEmpty(o.ErrorMessage, reason)}
}

// --- request_replan (deferred, approval) ---

type replanHandler struct{}

func (replanHandler) Match(_ *Service, cmd string) bool { return cmd == cmdReplan }

func (replanHandler) Run(_ context.Context, _ *Service, rc *runCtx, cmd string, _ *pyjson.Object, label string, i int) (Result, *Suspend) {
	if rc.workflowID == "" {
		return Result{Command: cmd, Label: label, Error: "replan step has no workflow link"}, nil
	}
	return Result{}, &Suspend{Cursor: i + 1, Kind: signalApproval, Key: fmt.Sprintf("%s:%d", rc.workflowID, i)}
}

// interpretApproval: an approved (or in-envelope auto-continued) checkpoint is a successful
// control step, left out of the completion summary.
func interpretApproval(steps []Step, i int) Result {
	label := "Replan"
	if i >= 0 && i < len(steps) && steps[i].Label != "" {
		label = steps[i].Label
	}
	return Result{Command: cmdReplan, Label: label, Success: true, Control: true}
}

// --- server tools (sync) ---

type serverHandler struct{}

func (serverHandler) Match(s *Service, cmd string) bool {
	if s.Tools == nil {
		return false
	}
	_, ok := s.Tools.Get(cmd)
	return ok
}

func (serverHandler) Run(ctx context.Context, s *Service, rc *runCtx, cmd string, args *pyjson.Object, label string, _ int) (Result, *Suspend) {
	turn := servertools.Turn{
		ConversationID: rc.convID, HouseholdID: rc.householdID, NodeID: rc.nodeID, Timezone: rc.timezone,
		Utterance: rc.goal,
	}
	if rc.userID != nil {
		turn.Speaker.UserID = *rc.userID
	}
	raw := s.Tools.Execute(ctx, servertools.Call{ID: hexID("call_"), Name: cmd, Args: args}, turn)
	r := serverOutcome(cmd, raw)
	r.Label = label
	s.log().Info("errands: step (server)", "command", cmd, "success", r.Success)
	return r, nil
}

// toObject turns a tool result into a dict ({} when it isn't one), as json.loads(raw) did.
func toObject(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	if v == nil {
		return pyjson.NewObject()
	}
	d, err := pyjson.Loads(pyjson.Dumps(v, false))
	if err != nil {
		return pyjson.NewObject()
	}
	if o, ok := d.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// summarizeServerResult is _summarize_server_result.
func summarizeServerResult(o *pyjson.Object) string {
	for _, k := range []string{"sources", "memories", "results", "items"} {
		v, _ := o.Get(k)
		if l, ok := v.([]any); ok && len(l) > 0 {
			return fmt.Sprintf("%d %s", len(l), k)
		}
	}
	if v, _ := o.Get("status"); v != nil {
		if st, ok := v.(string); ok && st != "accepted" {
			return st
		}
	}
	return ""
}

// serverOutcome is _server_step_outcome: a dict carrying "error" is a failure.
func serverOutcome(cmd string, raw any) Result {
	o := toObject(raw)
	if ev, ok := o.Get("error"); ok && ev != nil {
		return Result{Command: cmd, Error: firstNonEmpty(truthyStr(o, "message"), pyjson.Str(ev))}
	}
	return Result{Command: cmd, Success: true, Message: firstNonEmpty(truthyStr(o, "message"), summarizeServerResult(o)), Data: o}
}

// --- node commands (sync, the catch-all) ---

type nodeHandler struct{}

func (nodeHandler) Match(*Service, string) bool { return true }

func (nodeHandler) Run(ctx context.Context, s *Service, rc *runCtx, cmd string, args *pyjson.Object, label string, _ int) (Result, *Suspend) {
	var out *pyjson.Object
	if s.Nodes == nil || rc.nodeID == "" {
		out = servertools.Obj("success", false, "error", "could not dispatch to node: no node")
	} else {
		out = s.Nodes.RunTool(ctx, rc.nodeID, cmd, s.resolveNodeArgs(rc, args), rc.userID, rc.goal, s.NodeTimeout)
	}
	r := nodeOutcome(cmd, out)
	r.Label = label
	s.log().Info("errands: step (node)", "command", cmd, "success", r.Success)
	return r, nil
}

// resolveNodeArgs is _resolve_node_args: date keys in resolved_datetimes become the ISO
// datetimes node commands expect, in the household timezone (D18).
func (s *Service) resolveNodeArgs(rc *runCtx, args *pyjson.Object) *pyjson.Object {
	dt, ok := args.Get("resolved_datetimes")
	if !ok || dt == nil {
		return args
	}
	values, isList := dt.([]any)
	if !isList {
		values = []any{dt}
	}
	if rc.dateCtx == nil {
		rc.dateCtx = dates.New(s.now(), rc.timezone)
	}
	var out []any
	for _, v := range values {
		str, ok := v.(string)
		if !ok {
			continue
		}
		if dates.IsISODatetime(str) {
			out = append(out, str)
			continue
		}
		if vals, ok := rc.dateCtx.Lookup(dates.NormalizeKey(str)); ok {
			for _, x := range vals {
				out = append(out, x)
			}
		}
	}
	if len(out) == 0 {
		out = values
	}
	c := copyObj(args)
	c.Set("resolved_datetimes", out)
	return c
}

// nodeOutcome is _node_step_outcome.
func nodeOutcome(cmd string, out *pyjson.Object) Result {
	if out == nil {
		return Result{Command: cmd, Success: true}
	}
	ev, _ := out.Get("error")
	success := true
	if sv, ok := out.Get("success"); ok {
		success = truthy(sv)
	}
	success = success && !truthy(ev)
	r := Result{Command: cmd, Success: success}
	if success {
		r.Message = truthyStr(out, "message")
		r.Data = out
	} else if truthy(ev) {
		r.Error = pyjson.Str(ev)
	} else {
		r.Error = "the command reported a failure"
	}
	return r
}

// --- the run loop ---

// execute is execute_errand from start: fail-fast, suspend on a deferred step.
func (s *Service) execute(ctx context.Context, w *workflowRow, start int, prior []Result) (*Aggregate, *Suspend) {
	results := append([]Result(nil), prior...)
	rc := &runCtx{workflowID: w.ID, householdID: w.HouseholdID, nodeID: w.NodeID, userID: w.UserID, goal: w.Goal,
		timezone: s.timezone(ctx, w.HouseholdID), convID: "errand-" + hexID(""), results: &results}
	for i := start; i < len(w.Steps); i++ {
		st := w.Steps[i]
		cmd := parse.PyStrip(st.Command)
		if cmd == "" {
			continue
		}
		args := st.Args
		if args == nil {
			args = pyjson.NewObject()
		}
		label := firstNonEmpty(st.Label, cmd)
		args, skip := ResolveStepArgs(args, results, s.now())
		if skip != "" {
			s.log().Info("errands: step skipped", "index", i, "command", cmd, "reason", skip)
			results = append(results, Result{Command: cmd, Label: label, Success: true,
				Data: servertools.Obj("skipped", skip)})
			continue
		}
		r, susp := s.runStep(ctx, rc, cmd, args, label, i)
		if susp != nil {
			susp.Results = results
			return nil, susp
		}
		r.Label = label
		results = append(results, r)
		if !r.Success {
			s.log().Info("errands: fail-fast", "index", i, "command", cmd)
			break
		}
	}
	agg := s.aggregateAndCompose(ctx, w.Goal, results)
	return &agg, nil
}

// Aggregate is the finished run's result.
type Aggregate struct {
	Status  string // success | partial | failed
	Passed  int
	Failed  int
	Message string
	Results []Result
}

// composeMessage is _compose_message: the plain per-step roll-up (the compose fallback).
func composeMessage(results []Result) string {
	if len(results) == 0 {
		return "The errand had no steps to run."
	}
	parts := make([]string, len(results))
	for i, r := range results {
		label := firstNonEmpty(r.Label, r.Command, "step")
		switch {
		case r.Success && r.Message != "":
			parts[i] = label + ": " + r.Message
		case r.Success:
			parts[i] = label + ": done"
		default:
			parts[i] = label + ": " + firstNonEmpty(r.Error, "failed")
		}
	}
	return strings.Join(parts, " • ")
}

func aggregate(results []Result) Aggregate {
	passed := 0
	for _, r := range results {
		if r.Success {
			passed++
		}
	}
	failed := len(results) - passed
	status := "partial"
	switch {
	case len(results) == 0, passed == 0:
		status = "failed"
	case failed == 0:
		status = "success"
	}
	return Aggregate{Status: status, Passed: passed, Failed: failed, Message: composeMessage(results), Results: results}
}

// compactData is _compact_data (cap 240): scalars and containers only.
func compactData(data *pyjson.Object, limit int) string {
	if data == nil {
		return ""
	}
	skip := map[string]bool{"success": true, "error": true, "actions": true, "message": true, "status": true}
	var parts []string
	total := 0
	for _, k := range data.Keys() {
		if skip[k] {
			continue
		}
		v, _ := data.Get(k)
		switch {
		case isScalar(v):
			parts = append(parts, k+"="+pyjson.Str(v))
		case pyjson.TypeName(v) == "list" || pyjson.TypeName(v) == "dict":
			parts = append(parts, k+"="+pyjson.Dumps(v, true))
		default:
			continue
		}
		total += runeLen(parts[len(parts)-1])
		if total > limit {
			break
		}
	}
	return truncRunes(strings.Join(parts, "; "), limit)
}

// ComposePrompt is the completion-summary prompt (_compose_errand_message), byte-exact.
func ComposePrompt(goal string, results []Result) string {
	lines := make([]string, len(results))
	for i, r := range results {
		label := firstNonEmpty(r.Label, r.Command, "step")
		detail := firstNonEmpty(r.Message, compactData(r.Data, 240), r.Error)
		if detail == "" {
			if r.Success {
				detail = "finished but returned no information"
			} else {
				detail = "failed with no detail"
			}
		}
		ok := "FAILED"
		if r.Success {
			ok = "ok"
		}
		lines[i] = "- " + label + " [" + ok + "]: " + detail
	}
	return "You are Jarvis giving the user the FINAL report on a background errand that " +
		"has now FINISHED. Their goal was: " + goal + "\n\nWhat ACTUALLY happened, step by " +
		"step:\n" + strings.Join(lines, "\n") +
		"\n\nWrite a short, friendly 1-2 sentence summary. Base EVERY statement STRICTLY " +
		"on the detail shown after each step — quote the useful information (the weather, " +
		"the answer, who you reached). NEVER invent an outcome: if a step's detail says it " +
		"'returned no information', report that it ran but do NOT claim what it produced — " +
		"do NOT say a joke was told, a message was sent, an item was found, or a task was " +
		"done unless that detail is actually shown above. The errand is OVER, so NEVER say " +
		"you 'will' do something and never mention any action not in the steps above. If a " +
		"step FAILED, say so honestly (a failed step stops the errand, so later steps did " +
		"NOT run).\n" +
		"CRITICAL — the goal may DESCRIBE actions (set a reminder, send a message, make a " +
		"call, add a to-do) that were NOT performed: ONLY the steps listed above are real. " +
		"If the goal mentions such an action but there is NO step for it above, you MUST " +
		"state it was NOT done (e.g. 'I checked the forecast — 100% rain — but I did not set " +
		"a reminder'). Do not imply a conditional follow-up happened just because its " +
		"condition was met. No preamble, no markdown."
}

// composeErrandMessage LLM-composes the summary on the background label (D40 09.Q11; legacy
// used the live slot), thinking off; any failure or empty output falls back.
func (s *Service) composeErrandMessage(ctx context.Context, goal string, results []Result, fallback string) string {
	if s.LLM == nil {
		return fallback
	}
	temp, maxTok, off := 0.3, 400, 0
	resp, err := s.LLM.Chat(ctx, llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTok, ReasoningBudget: &off,
		Messages: []llm.Message{{Role: "user", Content: llm.TextContent(ComposePrompt(goal, results))}},
	})
	if err != nil {
		s.log().Warn("errands: result compose failed; using the plain summary", "err", err)
		return fallback
	}
	content := resp.Content
	if i := strings.Index(content, "</think>"); i >= 0 {
		content = content[i+len("</think>"):]
	}
	if content = parse.PyStrip(content); content != "" {
		return content
	}
	return fallback
}

// aggregateAndCompose is aggregate_and_compose: control steps leave the tally and the summary
// (unless nothing else is left).
func (s *Service) aggregateAndCompose(ctx context.Context, goal string, results []Result) Aggregate {
	var visible []Result
	for _, r := range results {
		if !r.Control {
			visible = append(visible, r)
		}
	}
	if len(visible) == 0 {
		visible = results
	}
	agg := aggregate(visible)
	agg.Results = results
	agg.Message = s.composeErrandMessage(ctx, goal, visible, agg.Message)
	return agg
}

func terminalState(a Aggregate) string {
	if a.Status == "success" {
		return "done"
	}
	if a.Status == "" {
		return "failed"
	}
	return a.Status
}

// --- the durable drive/resume ---

type resumeJob struct {
	WorkflowID string       `json:"workflow_id"`
	Step       int          `json:"step"` // the step the signal answers; -1 starts the run
	Kind       string       `json:"kind"` // start | phone_call | approval
	Call       *CallOutcome `json:"call,omitempty"`
	// Fail, when set, is a synthesized failure outcome for the step (a deadline).
	Fail *failOutcome `json:"fail,omitempty"`
}

type failOutcome struct {
	Command string `json:"command"`
	Label   string `json:"label"`
	Error   string `json:"error"`
}

func (s *Service) enqueueResume(ctx context.Context, j resumeJob) error {
	b, _ := json.Marshal(j)
	_, err := s.Queue.Enqueue(ctx, JobResume, b, queue.Options{})
	return err
}

func (s *Service) runResumeJob(ctx context.Context, job queue.Job) ([]byte, error) {
	var j resumeJob
	if err := json.Unmarshal(job.Payload, &j); err != nil {
		return nil, queue.Permanent(err)
	}
	_, err := s.deliver(ctx, j)
	return nil, err
}

// deliver is deliver_signal (and run_workflow for the start signal): load, check the run waits
// on exactly this step, claim it, interpret the signal, then fail-fast or keep driving. It
// returns whether it drove the run.
func (s *Service) deliver(ctx context.Context, j resumeJob) (bool, error) {
	w, err := s.loadWorkflow(ctx, j.WorkflowID)
	if err != nil {
		return false, err
	}
	if w == nil || w.State != "waiting" || w.Cursor != j.Step+1 {
		return false, nil // already resumed, cancelled, or a stale signal
	}
	s.driving.Store(w.ID, struct{}{})
	defer s.driving.Delete(w.ID)
	ok, err := s.claimRunning(ctx, w.ID, w.Cursor)
	if err != nil || !ok {
		return false, err
	}
	w.State = "running"
	// From here the run must land somewhere honest even if the job's context ends.
	dctx := context.WithoutCancel(ctx)
	crashMsg, crashErr := "The workflow hit a snag after the last step.", "resume crashed"
	if j.Kind == signalStart {
		crashMsg, crashErr = "The workflow couldn't run.", "run crashed"
	}
	defer func() {
		if p := recover(); p != nil {
			s.log().Error("errands: run crashed", "workflow", w.ID, "panic", p)
			s.postCompletionCard(dctx, w.HouseholdID, w.DisplayTitle(), Aggregate{Status: "failed", Message: crashMsg}, w.UserID)
			_ = s.saveTerminal(dctx, w.ID, "failed", crashErr, w.Results)
		}
	}()

	results := append([]Result(nil), w.Results...)
	if j.Kind != signalStart {
		var outcome Result
		switch {
		case j.Fail != nil:
			outcome = Result{Command: j.Fail.Command, Label: j.Fail.Label, Error: j.Fail.Error}
		case j.Kind == signalPhone && j.Call != nil:
			outcome = interpretCall(*j.Call, w.Steps, j.Step)
		case j.Kind == signalApproval:
			outcome = interpretApproval(w.Steps, j.Step)
		default:
			s.log().Error("errands: no handler for signal", "kind", j.Kind)
			_ = s.saveTerminal(dctx, w.ID, "failed", "no consumer/handler for this signal", results)
			return false, nil
		}
		results = append(results, outcome)
		if !outcome.Success {
			agg := s.aggregateAndCompose(dctx, w.Goal, results)
			s.finish(dctx, w, agg)
			return true, nil
		}
	}
	s.drive(dctx, w, results)
	return true, nil
}

// drive runs the plan from the cursor: suspend on the next deferred step, else finish.
func (s *Service) drive(ctx context.Context, w *workflowRow, prior []Result) {
	agg, susp := s.execute(ctx, w, w.Cursor, prior)
	if susp != nil {
		s.suspend(ctx, w, susp)
		return
	}
	s.finish(ctx, w, *agg)
}

// finish posts the one completion card and lands the run terminal.
func (s *Service) finish(ctx context.Context, w *workflowRow, agg Aggregate) {
	s.postCompletionCard(ctx, w.HouseholdID, w.DisplayTitle(), agg, w.UserID)
	errText := ""
	if agg.Status != "success" {
		errText = truncRunes(agg.Message, 500)
	}
	if err := s.saveTerminal(ctx, w.ID, terminalState(agg), errText, agg.Results); err != nil {
		s.log().Error("errands: persist terminal state", "workflow", w.ID, "err", err)
	}
	s.log().Info("errands: run complete", "workflow", w.ID, "status", agg.Status)
}

// suspend persists the wait and schedules what resolves it: the checkpoint replan (approval)
// and the wait's deadline (D14, D40 09.Q6) — delayed queue jobs, not a poll.
func (s *Service) suspend(ctx context.Context, w *workflowRow, susp *Suspend) {
	if err := s.saveWaiting(ctx, w.ID, susp.Cursor, susp.Results, susp.Kind); err != nil {
		s.log().Error("errands: persist waiting state", "workflow", w.ID, "err", err)
		return
	}
	step := susp.Cursor - 1
	switch susp.Kind {
	case signalApproval:
		s.enqueueDeadline(ctx, deadlineJob{WorkflowID: w.ID, Step: step, Kind: signalApproval}, s.ApprovalDeadline)
		reason := ""
		if step >= 0 && step < len(w.Steps) {
			reason = truthyStr(w.Steps[step].Args, "reason")
		}
		b, _ := json.Marshal(replanJob{WorkflowID: w.ID, Step: step, Reason: reason})
		if _, err := s.Queue.Enqueue(ctx, JobReplan, b, queue.Options{}); err != nil {
			s.log().Error("errands: queue checkpoint replan", "workflow", w.ID, "err", err)
		}
	case signalPhone:
		s.enqueueDeadline(ctx, deadlineJob{WorkflowID: w.ID, Step: step, Kind: signalPhone, SessionID: susp.Key}, s.PhoneDeadline)
	}
	s.log().Info("errands: run waiting", "workflow", w.ID, "on", susp.Kind, "key", susp.Key)
}

// OnCallTerminal is the phone module's in-process hook (D16): a workflow-linked call reached a
// terminal state, so the errand resumes (or fails fast). Idempotent: the claim serializes it
// with the deadline's safety net.
func (s *Service) OnCallTerminal(ctx context.Context, o CallOutcome) error {
	if !s.Available() || o.WorkflowID == "" || !o.Terminal() {
		return nil
	}
	return s.enqueueResume(ctx, resumeJob{WorkflowID: o.WorkflowID, Step: o.Step, Kind: signalPhone, Call: &o})
}

// Recover is startup recovery: a run still "running" at boot was caught mid-step by a restart.
// It fails with an honest card (D40 09.Q3). Runs this process is driving are skipped.
func (s *Service) Recover(ctx context.Context) int {
	if s.DB == nil {
		return 0
	}
	rows, err := s.DB.Read.QueryContext(ctx, `SELECT id FROM cc_workflows WHERE state = 'running'`)
	if err != nil {
		s.log().Error("errands: startup recovery", "err", err)
		return 0
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if _, live := s.driving.Load(id); live {
			continue
		}
		w, err := s.loadWorkflow(ctx, id)
		if err != nil || w == nil || w.State != "running" {
			continue
		}
		passed := 0
		for _, r := range w.Results {
			if r.Success {
				passed++
			}
		}
		agg := Aggregate{Status: "failed", Passed: passed, Failed: max(len(w.Results)-passed, 0),
			Message: "This errand was interrupted by a restart and couldn't be finished.", Results: w.Results}
		if passed > 0 {
			agg.Status = "partial"
		}
		s.postCompletionCard(ctx, w.HouseholdID, w.DisplayTitle(), agg, w.UserID)
		if err := s.saveTerminal(ctx, w.ID, terminalState(agg), "interrupted by a restart", w.Results); err != nil {
			s.log().Error("errands: recovery persist", "workflow", w.ID, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		s.log().Warn("errands: startup recovery failed orphaned runs", "count", n)
	}
	return n
}
