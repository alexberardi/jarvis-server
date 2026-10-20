// Package computer is the computer-task loop (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 10):
// observe the page or app, ask the model for a short batch of actions, decide each one against the
// household's rules, send the batch to the agent, and repeat until the model says done. Budgets
// (rounds, minutes), the last two screenshots, a durable stop flag and one audit row per action
// are part of it. The errand step that drives it is piece 11.
package computer

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Household settings the loop reads (Interfaces §3; registered with the errand wiring, piece 11).
const (
	SettingMaxRounds  = "computer.max_rounds"
	SettingMaxMinutes = "computer.max_minutes"
	SettingModel      = "computer.model"
	// SettingAutoApproveCents is the spend limit: a checkout at most this many cents is approved
	// without asking (0 = off). The errand step reads it (piece 11).
	SettingAutoApproveCents = "computer.auto_approve_cents"

	defaultMaxRounds  = 40
	defaultMaxMinutes = 20
)

// Audit source of the loop's events.
const SourceComputer = "computer"

// Outcome states.
const (
	StateDone          = "done"
	StateAsk           = "ask"
	StateNeedsApproval = "needs_approval"
	StateFailed        = "failed"
	StateStopped       = "stopped"
	StatePaused        = "paused"
)

const (
	maxScreenshots = 2 // screenshots sent per model call
	fewElements    = 5 // a snapshot with fewer elements gets a screenshot too
	maxTokens      = 1024
)

// LLM is the llm module in process.
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Executor runs a run's observations and actions on the computer (the agent's computer_observe,
// computer_act and computer_stop, through the node bus).
type Executor interface {
	Observe(ctx context.Context, o Observe) (Observation, error)
	Act(ctx context.Context, b Batch) (ActResult, error)
	Stop(ctx context.Context, runID string)
}

// Policy decides actions against the household's rules (policy.Policy).
type Policy interface {
	Decide(ctx context.Context, hh string, a policy.Action) policy.Verdict
}

// Audit is the autonomy activity log (policy.Store).
type Audit interface {
	Record(ctx context.Context, e policy.Event) error
}

// Approvals mints single-use approval tokens for risky actions (cc's mintApproval, piece 5).
type Approvals interface {
	Mint(ctx context.Context, hh, nodeID, runID, digest string) (string, error)
}

// Settings reads household settings (nil: the defaults).
type Settings interface {
	Int(ctx context.Context, key string, sc settings.Scope) int64
	String(ctx context.Context, key string, sc settings.Scope) string
}

// Loop runs computer tasks. LLM, Exec, Policy and Store are required.
type Loop struct {
	LLM       LLM
	Exec      Executor
	Policy    Policy
	Audit     Audit // nil: no audit rows
	Store     *Store
	Approvals Approvals // nil: a risky action can't be allowed, it fails
	Settings  Settings
	Models    Models
	Now       func() time.Time
}

// Task is one computer task: a new run, or the resumption of RunID (after a confirmation or a
// question).
type Task struct {
	RunID       string // "" starts a new run
	HouseholdID string
	UserID      *int64
	NodeID      string
	WorkflowID  string
	Step        int
	Goal        string
	Target      computeruse.Target
	Success     *SuccessCheck
	Rules       string // the household's rules as one line for the model ("" = the defaults)
	UseCloud    bool
	// Approved are the digests of held actions the user approved: the loop mints their tokens
	// and runs the held batch before anything else.
	Approved []string
	// Answer is the user's reply to the run's last question.
	Answer string
	// Progress, when set, is called after every round with the run as saved (its history), so
	// the errand's card can show what the task is doing.
	Progress func(run *Run)
}

// Outcome is where a Run call ended.
type Outcome struct {
	RunID    string
	State    string // done | ask | needs_approval | failed | stopped | paused
	Summary  string // done: the model's summary; failed/stopped: the reason, for the user
	Ask      string // ask: the question
	Reason   string // ask: page_instructions | unclear | login_needed
	Pending  []PendingAction
	Checkout *Checkout
	Verify   *SuccessCheck
	Rounds   int
}

// PendingAction is one risky action held for the user's confirmation. All the risky actions of a
// batch are held together, in one confirmation.
type PendingAction struct {
	Index    int // in the held batch
	Action   computeruse.Action
	Category string
	Reason   string
	Digest   string // what the approval token will be bound to
}

// Observe is one computer_observe call.
type Observe struct {
	RunID      string
	Target     computeruse.Target
	Snapshot   bool
	Screenshot bool
}

// Observation is what the executor saw.
type Observation struct {
	URL, Title, App string
	Gen             int
	Elements        []Element
	Image           *Image // nil without a screenshot
}

// Element is one snapshot element (piece 6's snapshot, the AX/AT-SPI trees on the desktop).
type Element struct {
	Ref, Role, Name, Value string
	Type, Autocomplete     string
	State                  []string
	Secure                 bool
}

// Image is one screenshot, for one model call only.
type Image struct {
	MIME          string
	Data          []byte
	CaptureID     string
	Width, Height int
}

// Batch is one computer_act call.
type Batch struct {
	RunID   string
	Target  computeruse.Target
	Actions []computeruse.Action
}

// ActResult is computer_act's answer: a result per action that ran, in order (the agent stops at
// the first failure).
type ActResult struct {
	Results []StepResult
	Paused  bool // the user took over the computer
}

// StepResult is one action's result.
type StepResult struct {
	I     int
	OK    bool
	Code  string // denied, needs_approval, stale_ref, stopped, …
	Error string
}

// round is one Run call's state.
type round struct {
	run      *Run
	task     Task
	pick     Pick
	shots    []*Image
	seen     map[string]bool
	wantShot bool
	feedback string
	looked   bool // this Run call has observed once
}

// Run drives the task until it is done, needs the user, fails, is stopped or runs out of budget.
// History is saved after every round, so a stop, a crash or a confirmation loses nothing.
func (l *Loop) Run(ctx context.Context, t Task) Outcome {
	run, err := l.open(ctx, t)
	if err != nil {
		return Outcome{RunID: t.RunID, State: StateFailed, Summary: "I couldn't start the computer task: " + err.Error()}
	}
	sc := settings.Scope{HouseholdID: t.HouseholdID}
	pick, err := pickLabel(ctx, l.Models, l.str(ctx, SettingModel, sc, ModelAuto), t.UseCloud)
	if err != nil {
		return l.end(ctx, run, Outcome{State: StateFailed, Summary: err.Error()})
	}
	run.Model = pick.Model
	r := &round{run: run, task: t, pick: pick, seen: map[string]bool{}}
	maxRounds := l.int(ctx, SettingMaxRounds, sc, defaultMaxRounds)
	deadline := l.now().Add(time.Duration(l.int(ctx, SettingMaxMinutes, sc, defaultMaxMinutes)) * time.Minute)

	if held := run.Pending; held != nil {
		run.Pending = nil
		// A stop pressed while the run waited wins over a later approval: the held batch is
		// dropped unsent.
		if l.stopRequested(ctx, run) {
			return l.end(ctx, run, Outcome{State: StateStopped, Summary: "Stopped."})
		}
		if out, done := l.runApproved(ctx, r, held); done {
			return l.end(ctx, run, out)
		}
	}
	for {
		if l.stopRequested(ctx, run) {
			return l.end(ctx, run, Outcome{State: StateStopped, Summary: "Stopped."})
		}
		if run.Rounds >= maxRounds {
			l.Exec.Stop(ctx, run.ID)
			return l.end(ctx, run, Outcome{State: StateFailed,
				Summary: fmt.Sprintf("I stopped after %d rounds without finishing.", run.Rounds)})
		}
		if !l.now().Before(deadline) {
			l.Exec.Stop(ctx, run.ID)
			return l.end(ctx, run, Outcome{State: StateFailed,
				Summary: "I ran out of time before finishing."})
		}
		run.Rounds++
		out, done := l.round(ctx, r)
		if done {
			return l.end(ctx, run, out)
		}
		if err := l.Store.Save(ctx, run); err != nil {
			return l.end(ctx, run, Outcome{State: StateFailed, Summary: "I couldn't save the task's progress."})
		}
		if t.Progress != nil {
			t.Progress(run)
		}
	}
}

// stopRequested reports a durable stop (or a store that can't say), and passes it on to the
// executor.
func (l *Loop) stopRequested(ctx context.Context, run *Run) bool {
	if stop, err := l.Store.StopRequested(ctx, run.ID); err != nil || stop {
		l.Exec.Stop(ctx, run.ID)
		return true
	}
	return false
}

// round is one observe → model → policy → act cycle; done reports an outcome that ends the call.
func (l *Loop) round(ctx context.Context, r *round) (Outcome, bool) {
	t := r.task
	obs, err := l.observe(ctx, r)
	if err != nil {
		return Outcome{State: StateFailed, Summary: "I couldn't see the computer: " + err.Error()}, true
	}
	// New page text that reads like instructions to the model stops the run. After the user
	// answered a question, what was on the page then is not news.
	answered := t.Answer != "" && !r.looked
	r.looked = true
	for _, s := range pageTexts(obs) {
		if !r.seen[s] && !answered && injectionLike(s) {
			return Outcome{State: StateAsk, Ask: injectionAsk, Reason: ReasonPageInstructions}, true
		}
		r.seen[s] = true
	}
	if obs.Image != nil && r.pick.Vision {
		r.shots = append(r.shots, obs.Image)
		if len(r.shots) > maxScreenshots {
			r.shots = r.shots[len(r.shots)-maxScreenshots:]
		}
	}

	reply, err := l.ask(ctx, r, obs)
	if err != nil {
		return Outcome{State: StateFailed, Summary: err.Error()}, true
	}
	r.feedback = ""
	switch {
	case reply.Done:
		return Outcome{State: StateDone, Summary: reply.Summary, Verify: reply.Verify}, true
	case reply.Ask != "":
		if reply.Reason == ReasonPageInstructions {
			return Outcome{State: StateAsk, Ask: injectionAsk, Reason: ReasonPageInstructions}, true
		}
		return Outcome{State: StateAsk, Ask: reply.Ask, Reason: reply.Reason}, true
	}

	// A screenshot "action" is the model asking to see the screen next round.
	var actions []computeruse.Action
	for _, a := range reply.Actions {
		if a.Kind == "screenshot" {
			r.wantShot = true
			continue
		}
		actions = append(actions, l.toImage(r, a))
	}
	if len(actions) == 0 {
		return Outcome{}, false
	}
	return l.decideAndAct(ctx, r, obs, actions, reply.Checkout)
}

// observe takes a snapshot, and a screenshot for the desktop, when the model asked for one, or
// when the snapshot has too few elements to work from.
func (l *Loop) observe(ctx context.Context, r *round) (Observation, error) {
	t := r.task
	desktop := t.Target.Kind == "desktop"
	o := Observe{RunID: r.run.ID, Target: t.Target, Snapshot: true, Screenshot: desktop || r.wantShot}
	r.wantShot = false
	obs, err := l.Exec.Observe(ctx, o)
	if err != nil {
		return obs, err
	}
	if obs.Image == nil && len(obs.Elements) < fewElements {
		shot, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: t.Target, Screenshot: true})
		if err == nil {
			obs.Image = shot.Image
		}
	}
	return obs, nil
}

// ask sends the round's prompt and parses the answer, with one retry on an unreadable one.
func (l *Loop) ask(ctx context.Context, r *round, obs Observation) (Reply, error) {
	t := r.task
	text := renderPrompt(promptInput{
		Goal: t.Goal, Success: t.Success, Rules: t.Rules, Answer: t.Answer, History: r.run.History,
		Obs: obs, Images: len(r.shots), Coordinates: coordinateHint(r.pick.Adapter, r.shots),
		Feedback: r.feedback,
	})
	parts := []llm.Part{{Type: "text", Text: text}}
	for _, im := range r.shots {
		url := "data:" + im.MIME + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
		parts = append(parts, llm.Part{Type: "image_url", ImageURL: &llm.ImageURL{URL: url}})
	}
	msgs := []llm.Message{
		{Role: "system", Content: llm.TextContent(systemPrompt)},
		{Role: "user", Content: &llm.Content{Parts: parts}},
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		temp, mt, think := 0.0, maxTokens, 0
		resp, err := l.LLM.Chat(ctx, llm.ChatRequest{
			Label: r.pick.Label, Priority: llm.PriorityBackground, Temperature: &temp, MaxTokens: &mt,
			ReasoningBudget: &think, Messages: msgs,
		})
		if err != nil {
			return Reply{}, fmt.Errorf("The model didn't answer: %v", err)
		}
		reply, err := parseReply(resp.Content)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		msgs = append(msgs,
			llm.Message{Role: "assistant", Content: llm.TextContent(resp.Content)},
			llm.Message{Role: "user", Content: llm.TextContent("I couldn't read that: " + err.Error() +
				". Answer with exactly one JSON object in one of the four shapes.")})
	}
	return Reply{}, fmt.Errorf("The model's answers couldn't be read (%v).", lastErr)
}

// decision is one action's verdict.
type decision struct {
	action  computeruse.Action
	risk    computeruse.Risk
	target  string // the policy target that decided it
	site    string
	app     string
	verdict policy.Verdict
}

// decideAndAct decides every action of the batch, then blocks, holds or runs it.
func (l *Loop) decideAndAct(ctx context.Context, r *round, obs Observation, actions []computeruse.Action, co *Checkout) (Outcome, bool) {
	t := r.task
	tgt := l.target(t, obs)
	ds := make([]decision, len(actions))
	for i, a := range actions {
		ds[i] = l.decide(ctx, t, tgt, obs, a)
	}
	for i, d := range ds {
		if d.verdict.Effect == policy.Block {
			l.record(ctx, r, d, policy.OutcomeBlocked, "blocked: "+d.verdict.Reason)
			for _, rest := range ds[i+1:] {
				l.record(ctx, r, rest, policy.OutcomeFailed, "not run")
			}
			return Outcome{State: StateFailed, Summary: fmt.Sprintf("I can't do %q: %s.", d.action.Intent, d.verdict.Reason)}, true
		}
	}

	// When anything in the batch waits for the user, every risky action of it is in the same
	// confirmation, the ones the household allows too: on resume each approved one gets its token.
	asks := false
	for _, d := range ds {
		asks = asks || d.verdict.Effect == policy.Ask
	}
	var pending []PendingAction
	held := &Held{Target: tgt}
	for i, d := range ds {
		h := HeldAction{Action: d.action}
		switch {
		case !asks:
		case d.risk.Level == computeruse.LevelConfirm:
			h.Category, h.Reason, h.Digest = d.risk.Category, d.risk.Reason, d.action.Digest(r.run.ID, tgt)
			pending = append(pending, PendingAction{Index: i, Action: d.action, Category: h.Category, Reason: h.Reason, Digest: h.Digest})
		case d.verdict.Effect == policy.Ask:
			// A site or app the household asks about: the whole batch waits, as one confirmation.
			h.Reason, h.Digest = d.verdict.Reason, d.action.Digest(r.run.ID, tgt)
			pending = append(pending, PendingAction{Index: i, Action: d.action, Reason: h.Reason, Digest: h.Digest})
		}
		held.Actions = append(held.Actions, h)
	}
	if len(pending) > 0 {
		for _, p := range pending {
			l.record(ctx, r, ds[p.Index], policy.OutcomeAsked, "waiting for the user")
		}
		r.run.Pending = held
		return Outcome{State: StateNeedsApproval, Pending: pending, Checkout: co}, true
	}

	for i, d := range ds {
		if d.risk.Level != computeruse.LevelConfirm {
			continue
		}
		tok, err := l.mint(ctx, r, d.action.Digest(r.run.ID, tgt))
		if err != nil {
			for _, rest := range ds {
				l.record(ctx, r, rest, policy.OutcomeFailed, "not run")
			}
			return Outcome{State: StateFailed, Summary: "I couldn't approve a risky step: " + err.Error()}, true
		}
		ds[i].action.Approval = tok
	}
	return l.act(ctx, r, tgt, ds)
}

// runApproved runs a held batch once the user approved every one of its risky actions. A batch
// with an action left unapproved is dropped; the model plans again from a fresh look.
func (l *Loop) runApproved(ctx context.Context, r *round, held *Held) (Outcome, bool) {
	ok := map[string]bool{}
	for _, d := range r.task.Approved {
		ok[d] = true
	}
	for _, h := range held.Actions {
		if h.Digest != "" && !ok[h.Digest] {
			return Outcome{}, false
		}
	}
	tgt := held.Target
	ds := make([]decision, len(held.Actions))
	for i, h := range held.Actions {
		ds[i] = decision{action: h.Action, site: siteHost(tgt.Site), app: tgt.App,
			verdict: policy.Verdict{Effect: policy.Allow, Reason: "approved by the user"}}
		if h.Category != "" {
			ds[i].risk = computeruse.Risk{Level: computeruse.LevelConfirm, Category: h.Category, Reason: h.Reason}
			ds[i].target = policy.TargetAction + h.Category
		}
		if h.Digest != "" {
			tok, err := l.mint(ctx, r, h.Digest)
			if err != nil {
				return Outcome{State: StateFailed, Summary: "I couldn't approve a risky step: " + err.Error()}, true
			}
			ds[i].action.Approval = tok
		}
	}
	return l.act(ctx, r, tgt, ds)
}

// act sends the batch and records a history entry and an audit row per action.
func (l *Loop) act(ctx context.Context, r *round, tgt computeruse.Target, ds []decision) (Outcome, bool) {
	b := Batch{RunID: r.run.ID, Target: tgt}
	for _, d := range ds {
		b.Actions = append(b.Actions, d.action)
	}
	res, err := l.Exec.Act(ctx, b)
	if err != nil {
		for _, d := range ds {
			l.record(ctx, r, d, policy.OutcomeFailed, "not run")
		}
		return Outcome{State: StateFailed, Summary: "I couldn't act on the computer: " + err.Error()}, true
	}
	byIndex := map[int]StepResult{}
	for _, sr := range res.Results {
		byIndex[sr.I] = sr
	}
	stopped := false
	var failed []string
	for i, d := range ds {
		sr, ran := byIndex[i]
		switch {
		case !ran:
			l.record(ctx, r, d, policy.OutcomeFailed, "not run")
		case sr.OK:
			l.record(ctx, r, d, policy.OutcomeRan, "ok")
		default:
			result := strings.TrimSpace("failed: " + sr.Code + " " + sr.Error)
			l.record(ctx, r, d, policy.OutcomeFailed, result)
			failed = append(failed, fmt.Sprintf("%q %s", d.action.Intent, result))
			stopped = stopped || sr.Code == "stopped"
		}
	}
	switch {
	case stopped:
		return Outcome{State: StateStopped, Summary: "Stopped."}, true
	case res.Paused:
		return Outcome{State: StatePaused, Summary: "Paused: you're using the computer."}, true
	}
	if len(failed) > 0 {
		r.feedback = "these actions failed, so the rest of the batch didn't run: " + strings.Join(failed, "; ")
	}
	return Outcome{}, false
}

// decide resolves one action: the built-in deny list, then the household's rules for the site or
// app it acts in, then for its risk category when it is risky. The strictest verdict wins.
func (l *Loop) decide(ctx context.Context, t Task, tgt computeruse.Target, obs Observation, a computeruse.Action) decision {
	el := elementFor(obs, a.Ref)
	d := decision{action: a, app: tgt.App, site: siteHost(tgt.Site)}
	if a.Kind == "navigate" {
		if u, err := url.Parse(a.URL); err == nil {
			d.site = u.Hostname()
		}
	}
	d.risk = computeruse.Classify(a, el, computeruse.Target{Kind: tgt.Kind, Site: d.site, App: d.app})
	if d.risk.Level == computeruse.LevelDeny {
		d.verdict = policy.Verdict{Effect: policy.Block, Reason: d.risk.Reason}
		return d
	}
	var targets []string
	switch {
	case d.site != "":
		targets = append(targets, policy.TargetSite+d.site)
	case d.app != "":
		targets = append(targets, policy.TargetApp+d.app)
	}
	if d.risk.Level == computeruse.LevelConfirm {
		targets = append(targets, policy.TargetAction+d.risk.Category)
	}
	d.verdict = policy.Verdict{Effect: policy.Allow, Reason: "no rule for this action"}
	for _, target := range targets {
		v := l.Policy.Decide(ctx, t.HouseholdID, policy.Action{Tool: policy.ToolComputerTask, Target: target,
			IsRisky: d.risk.Level == computeruse.LevelConfirm, Source: SourceComputer})
		if rank(v.Effect) > rank(d.verdict.Effect) || d.target == "" {
			d.verdict, d.target = v, target
		}
	}
	return d
}

func rank(e policy.Effect) int {
	switch e {
	case policy.Block:
		return 2
	case policy.Ask:
		return 1
	}
	return 0
}

// target is the run's target as the agent sees it now: the current tab's host, or the app.
func (l *Loop) target(t Task, obs Observation) computeruse.Target {
	tgt := t.Target
	if obs.URL != "" {
		if u, err := url.Parse(obs.URL); err == nil && u.Host != "" {
			tgt.Site = u.Host
		}
	}
	if obs.App != "" {
		tgt.App = obs.App
	}
	return tgt
}

// toImage maps a model's coordinates to the newest screenshot's pixels and names that capture.
func (l *Loop) toImage(r *round, a computeruse.Action) computeruse.Action {
	if len(r.shots) == 0 || (a.X == nil && len(a.Path) == 0) {
		return a
	}
	im := r.shots[len(r.shots)-1]
	ad := r.pick.Adapter
	if a.X != nil {
		x, y := ad.ToImage(*a.X, *a.Y, im.Width, im.Height)
		a.X, a.Y = &x, &y
	}
	if len(a.Path) > 0 {
		p := make([][2]int, len(a.Path))
		for i, pt := range a.Path {
			x, y := ad.ToImage(pt[0], pt[1], im.Width, im.Height)
			p[i] = [2]int{x, y}
		}
		a.Path = p
	}
	a.CaptureID = im.CaptureID
	return a
}

func (l *Loop) mint(ctx context.Context, r *round, digest string) (string, error) {
	if l.Approvals == nil {
		return "", fmt.Errorf("approvals are not available")
	}
	return l.Approvals.Mint(ctx, r.task.HouseholdID, r.task.NodeID, r.run.ID, digest)
}

// record appends the action to the run's history and writes its audit row.
func (l *Loop) record(ctx context.Context, r *round, d decision, outcome, result string) {
	r.run.History = append(r.run.History, Entry{Round: r.run.Rounds, Kind: d.action.Kind,
		Intent: d.action.Intent, Ref: d.action.Ref, Result: result})
	if l.Audit == nil {
		return
	}
	var ruleID *int64
	if d.verdict.Rule != nil && d.verdict.Rule.ID != 0 {
		ruleID = &d.verdict.Rule.ID
	}
	effect := d.verdict.Effect
	if effect == "" {
		effect = policy.Allow
	}
	_ = l.Audit.Record(ctx, policy.Event{
		HouseholdID: r.task.HouseholdID, UserID: r.task.UserID, Source: SourceComputer, SourceID: r.run.ID,
		Tool: policy.ToolComputerTask, Effect: effect, Outcome: outcome, RuleID: ruleID, Reason: d.verdict.Reason,
		Detail: map[string]any{"intent": d.action.Intent, "kind": d.action.Kind, "target": d.target,
			"site": d.site, "app": d.app, "result": result},
	})
}

// open creates the task's run, or loads the one it resumes.
func (l *Loop) open(ctx context.Context, t Task) (*Run, error) {
	if t.RunID == "" {
		kind := t.Target.Kind
		if kind == "" {
			kind = "browser"
		}
		return l.Store.Create(ctx, Run{HouseholdID: t.HouseholdID, UserID: t.UserID, NodeID: t.NodeID,
			WorkflowID: t.WorkflowID, Step: t.Step, Goal: t.Goal, Target: kind})
	}
	run, err := l.Store.Get(ctx, t.RunID)
	if err != nil {
		return nil, err
	}
	switch run.State {
	case RunDone, RunFailed, RunStopped:
		return nil, fmt.Errorf("the task already ended (%s)", run.State)
	}
	run.State = RunRunning
	return run, nil
}

// end saves the run in the state the outcome leaves it in.
func (l *Loop) end(ctx context.Context, run *Run, out Outcome) Outcome {
	out.RunID, out.Rounds = run.ID, run.Rounds
	switch out.State {
	case StateDone:
		run.State = RunDone
	case StateAsk, StateNeedsApproval:
		run.State = RunWaiting
	case StatePaused:
		run.State = RunPaused
	case StateStopped:
		run.State = RunStopped
	default:
		run.State, run.Error = RunFailed, out.Summary
	}
	_ = l.Store.Save(ctx, run)
	return out
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) int(ctx context.Context, key string, sc settings.Scope, def int) int {
	if l.Settings != nil {
		if v := l.Settings.Int(ctx, key, sc); v > 0 {
			return int(v)
		}
	}
	return def
}

func (l *Loop) str(ctx context.Context, key string, sc settings.Scope, def string) string {
	if l.Settings != nil {
		if v := l.Settings.String(ctx, key, sc); v != "" {
			return v
		}
	}
	return def
}

// pageTexts are the observation's texts the injection check reads.
func pageTexts(o Observation) []string {
	out := []string{o.Title}
	for _, e := range o.Elements {
		out = append(out, e.Name, e.Value)
	}
	return out
}

func elementFor(o Observation, ref string) *computeruse.Element {
	if ref == "" {
		return nil
	}
	for _, e := range o.Elements {
		if e.Ref == ref {
			return &computeruse.Element{Role: e.Role, Name: e.Name, Type: e.Type, Autocomplete: e.Autocomplete, Secure: e.Secure}
		}
	}
	return nil
}

// siteHost drops a port: rules name hosts.
func siteHost(site string) string {
	if site == "" {
		return ""
	}
	if u, err := url.Parse("//" + site); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return site
}

// coordinateHint tells the model the coordinate convention of its screenshots.
func coordinateHint(a computeruse.Adapter, shots []*Image) string {
	if len(shots) == 0 {
		return ""
	}
	im := shots[len(shots)-1]
	switch a.Name {
	case "norm1000":
		return "Coordinates are 0 to 999 across and down the screenshot."
	}
	return fmt.Sprintf("Coordinates are pixels of the newest screenshot (%dx%d).", im.Width, im.Height)
}
