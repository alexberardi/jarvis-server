package errands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/dates"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
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
	// signalConfirm resumes a run parked on a step its household's rules ask about (autonomy
	// PLAN §1, checkpoint 2). A resume kind only: the run waits on approval.
	signalConfirm = "confirm_step"
)

// Suspend is a deferred step pausing the run on a signal.
type Suspend struct {
	Cursor  int    // the next step to run on resume
	Kind    string // phone_call | approval
	Key     string // the correlation key (a phone session id)
	Results []Result
	Confirm bool // an approval wait on the step at Cursor itself (a rule said ask), not a replan
	// Computer: a computer task's confirmation or question (Confirm is set too); its card replaces
	// the step card.
	Computer *computerWait
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
	wf          *workflowRow
	// signal is the confirmation or answer the run resumed from, for the step at the cursor only.
	signal *resumeJob
}

// StepHandler is one capability plane. Handlers are tried in order and the first match runs
// the step: phone → replan → computer task → server tool → node (the catch-all).
type StepHandler interface {
	Match(s *Service, command string) bool
	// Run returns the step's outcome, or a Suspend when a deferred step paused the run.
	Run(ctx context.Context, s *Service, rc *runCtx, command string, args *pyjson.Object, label string, i int) (Result, *Suspend)
}

func (s *Service) handlers() []StepHandler {
	return []StepHandler{phoneHandler{}, replanHandler{}, computerHandler{}, serverHandler{}, nodeHandler{}}
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
		r.Data = stripImages(out)
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
		timezone: s.timezone(ctx, w.HouseholdID), convID: "errand-" + hexID(""), results: &results, wf: w}
	auto, cards := w.Kind == KindAutorun, w.tracksProgress()
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
		if auto {
			if by, ok := s.stopRequested.LoadAndDelete(w.ID); ok {
				agg := aggregate(results)
				agg.Status, agg.Message, agg.Results = "stopped", "Stopped before “"+label+"”.", results
				if uid, ok := by.(int64); ok {
					agg.stoppedBy = &uid
				}
				return &agg, nil
			}
		}
		args, skip := ResolveStepArgs(args, results, s.now())
		if skip != "" {
			s.log().Info("errands: step skipped", "index", i, "command", cmd, "reason", skip)
			results = append(results, Result{Command: cmd, Label: label, Success: true,
				Data: servertools.Obj("skipped", skip)})
			if cards {
				s.updateRanCard(ctx, w, results, "running", "")
			}
			continue
		}
		var v *policy.Verdict
		if !controlCommands[cmd] && s.autonomous(ctx, w.HouseholdID) {
			d := s.Policy.Decide(ctx, w.HouseholdID, policy.Action{Tool: cmd, Args: args, IsRisky: st.IsRisky,
				RunWithoutConfirmation: st.RunWithoutConfirmation, Source: policy.SourceErrandStep})
			v = &d
			switch {
			case d.Effect == policy.Block:
				msg := "blocked by a household rule"
				if d.Rule != nil && d.Rule.Note != "" {
					msg += ": " + d.Rule.Note
				}
				s.recordStep(ctx, w, i, cmd, d, policy.OutcomeBlocked)
				s.log().Info("errands: step blocked by a household rule", "index", i, "command", cmd)
				results = append(results, Result{Command: cmd, Label: label, Error: msg})
				if cards {
					s.updateRanCard(ctx, w, results, "running", "")
				}
				agg := s.aggregateAndCompose(ctx, w.Goal, results)
				return &agg, nil
			case d.Effect == policy.Ask && auto && !st.Approved:
				s.recordStep(ctx, w, i, cmd, d, policy.OutcomeAsked)
				return nil, &Suspend{Cursor: i, Kind: signalApproval, Confirm: true, Results: results}
			}
		}
		rc.signal = nil
		if i == start && w.signal != nil && w.signal.Kind == signalConfirm {
			rc.signal = w.signal
		}
		r, susp := s.runStep(ctx, rc, cmd, args, label, i)
		if susp != nil {
			susp.Results = results
			return nil, susp
		}
		r.Label = label
		results = append(results, r)
		if auto && v != nil {
			outcome := policy.OutcomeRan
			if !r.Success {
				outcome = policy.OutcomeFailed
			}
			s.recordStep(ctx, w, i, cmd, *v, outcome)
		}
		if r.stopped {
			// The user stopped the computer task: the errand ends here, stopped.
			agg := aggregate(results)
			agg.Status, agg.Message, agg.Results = "stopped", "Stopped during “"+label+"”.", results
			if by, ok := s.stopRequested.LoadAndDelete(w.ID); ok {
				if uid, ok := by.(int64); ok {
					agg.stoppedBy = &uid
				}
			}
			return &agg, nil
		}
		if cards {
			s.updateRanCard(ctx, w, results, "running", "")
		}
		if !r.Success {
			s.log().Info("errands: fail-fast", "index", i, "command", cmd)
			break
		}
	}
	agg := s.aggregateAndCompose(ctx, w.Goal, results)
	return &agg, nil
}

// recordStep logs what the household's rules did to one step of a run.
func (s *Service) recordStep(ctx context.Context, w *workflowRow, i int, cmd string, v policy.Verdict, outcome string) {
	s.record(ctx, policy.Event{HouseholdID: w.HouseholdID, UserID: w.UserID, Source: policy.SourceErrandStep, SourceID: w.ID,
		Tool: cmd, Effect: v.Effect, Outcome: outcome, RuleID: ruleID(v), Reason: v.Reason, Detail: map[string]any{"step": i}})
}

// Aggregate is the finished run's result.
type Aggregate struct {
	Status  string // success | partial | failed | stopped (an autorun run's Stop)
	Passed  int
	Failed  int
	Message string
	// Answer is the composed report (what the errand found, or what it did), "" when the
	// compose fell back to the plain roll-up (#232). The cards show it above the steps.
	Answer  string
	Results []Result

	stoppedBy *int64 // Status "stopped": who tapped Stop
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

// stepContentLimit caps what one step's found content adds to the compose prompt: two
// quick_search sources in full (it keeps 4000 characters of each page).
const stepContentLimit = 8000

// stepContent is what a step found, for the composer (#232): the items of a result's list
// (search sources, memories, results), title and text each, cut to share stepContentLimit. The
// step's own line keeps its short status ("2 sources"); "" when the result has no list.
func stepContent(r Result) string {
	if !r.Success || r.Data == nil {
		return ""
	}
	var list []any
	for _, k := range []string{"sources", "memories", "results", "items"} {
		v, _ := r.Data.Get(k)
		if l, ok := v.([]any); ok && len(l) > 0 {
			list = l
			break
		}
	}
	if len(list) == 0 {
		return ""
	}
	each := max(stepContentLimit/len(list), 200)
	var lines []string
	total := 0
	for _, it := range list {
		var line string
		switch v := it.(type) {
		case *pyjson.Object:
			title := firstNonEmpty(truthyStr(v, "title"), truthyStr(v, "name"))
			text := firstNonEmpty(truthyStr(v, "content"), truthyStr(v, "snippet"), truthyStr(v, "text"),
				truthyStr(v, "summary"), truthyStr(v, "value"))
			line = strings.Join(slices.DeleteFunc([]string{title, text}, func(s string) bool { return s == "" }), ": ")
		case string:
			line = v
		default:
			line = pyjson.Str(v)
		}
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		if runeLen(line) > each {
			line = truncRunes(line, each) + "…"
		}
		if total += runeLen(line); total > stepContentLimit+200 {
			break
		}
		lines = append(lines, "    • "+line)
	}
	return strings.Join(lines, "\n")
}

// ComposePrompt is the completion report prompt (_compose_errand_message, extended by #232:
// it sees what each step found and answers an information goal with that answer).
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
		if found := stepContent(r); found != "" {
			lines[i] += "\n  What it found:\n" + found
		}
	}
	return "You are Jarvis giving the user the FINAL report on a background errand that " +
		"has now FINISHED. Their goal was: " + goal + "\n\nWhat ACTUALLY happened, step by " +
		"step:\n" + strings.Join(lines, "\n") +
		"\n\nIf the goal asks for information (to find, search, look up, compare, list or " +
		"summarize something), this report IS the answer the user is waiting for: give it. " +
		"Name the specific things found with their key facts (names, prices, ratings, times) " +
		"taken from what the steps found, one per line, starting with the first one (no " +
		"lead-in line), then one short closing sentence. If fewer were found than asked for, " +
		"give the ones found and say so. Otherwise write a " +
		"short, friendly 1-2 sentence summary. Base EVERY statement STRICTLY " +
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
		"condition was met. EXCEPTION: asking for the result to be sent, told or summarized " +
		"to the user themselves ('send me a summary', 'let me know') is NOT an undone " +
		"action: this report is how they get it, so never say it was not sent. No " +
		"preamble, no markdown (plain lines only)."
}

// composeErrandMessage LLM-composes the report on the background label (D40 09.Q11; legacy
// used the live slot), thinking off; any failure or empty output falls back (ok false).
func (s *Service) composeErrandMessage(ctx context.Context, goal string, results []Result, fallback string) (string, bool) {
	if s.LLM == nil {
		return fallback, false
	}
	temp, maxTok, off := 0.3, 800, 0
	resp, err := s.LLM.Chat(ctx, llm.ChatRequest{
		Label: "background", Temperature: &temp, MaxTokens: &maxTok, ReasoningBudget: &off,
		Messages: []llm.Message{{Role: "user", Content: llm.TextContent(ComposePrompt(goal, results))}},
	})
	if err != nil {
		s.log().Warn("errands: result compose failed; using the plain summary", "err", err)
		return fallback, false
	}
	content := resp.Content
	if i := strings.Index(content, "</think>"); i >= 0 {
		content = content[i+len("</think>"):]
	}
	if content = parse.PyStrip(content); content != "" {
		return content, true
	}
	return fallback, false
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
	msg, composed := s.composeErrandMessage(ctx, goal, visible, agg.Message)
	agg.Message = msg
	if composed {
		agg.Answer = msg
	}
	return agg
}

// cardSummary is a finished run's card summary (the inbox line and the push): the answer's
// first line that is not a lead-in ending in ":", else the message.
func cardSummary(a Aggregate) string {
	if a.Answer == "" {
		return a.Message
	}
	var first string
	for _, line := range strings.Split(a.Answer, "\n") {
		if line = parse.PyStrip(line); line != "" {
			if first == "" {
				first = line
			}
			if !strings.HasSuffix(line, ":") { // past a lead-in ("Here are the three:")
				first = line
				break
			}
		}
	}
	if runeLen(first) > summaryLimit {
		first = truncRunes(first, summaryLimit-1) + "…"
	}
	return first
}

const summaryLimit = 200

func terminalState(a Aggregate) string {
	switch a.Status {
	case "success":
		return "done"
	case "stopped":
		return "cancelled"
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
	// Approved are the digests of a computer task's held actions the user approved; Answer is the
	// user's answer to its question (a confirm_step resume).
	Approved []string `json:"approved,omitempty"`
	Answer   string   `json:"answer,omitempty"`
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
	w.signal = &j
	if j.Fail != nil {
		s.stopComputer(dctx, w.ID, true) // the wait ran out: its computer task ends unrun
	}
	if j.Kind != signalStart && (j.Kind != signalConfirm || j.Fail != nil) { // an approved step runs from the cursor
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

// finish posts the one completion card (an autorun run's "Ran on its own" card is updated
// instead) and lands the run terminal.
func (s *Service) finish(ctx context.Context, w *workflowRow, agg Aggregate) {
	if w.Kind == KindAutorun {
		s.stopRequested.Delete(w.ID) // a Stop during the last step
		if w.InboxItemID != "" {
			c := s.ranCard(ctx, w, agg.Results, ranState(agg))
			c.Answer = agg.Answer
			s.putRanCard(ctx, w, c, cardSummary(agg))
		}
		if agg.Status == "stopped" {
			s.recordStopped(ctx, w, agg.stoppedBy)
		}
	} else {
		s.stopRequested.Delete(w.ID)
		if w.InboxItemID != "" && w.hasComputerStep() {
			// The progress card shows where it ended; the completion card reports it.
			s.putRanCard(ctx, w, s.ranCard(ctx, w, agg.Results, ranState(agg)), "")
		}
		s.postCompletionCard(ctx, w.HouseholdID, w.DisplayTitle(), agg, w.UserID)
	}
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
		if susp.Confirm {
			// A new wait: the revision bump makes earlier cards (and this wait's predecessors'
			// deadlines) stale.
			if err := s.saveSteps(ctx, w.ID, w.Steps, true); err != nil {
				s.log().Error("errands: bump revision for a step card", "workflow", w.ID, "err", err)
			}
			w.Revision++
			s.enqueueDeadline(ctx, deadlineJob{WorkflowID: w.ID, Step: step, Kind: signalApproval, Confirm: true,
				Revision: w.Revision}, s.ApprovalDeadline)
			if susp.Computer != nil {
				s.postComputerCard(ctx, w, susp.Cursor, susp.Computer)
			} else {
				s.postStepCard(ctx, w, susp.Cursor)
			}
			s.updateRanCard(ctx, w, susp.Results, "waiting", "")
			break
		}
		s.enqueueDeadline(ctx, deadlineJob{WorkflowID: w.ID, Step: step, Kind: signalApproval, Revision: w.Revision},
			s.ApprovalDeadline)
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
