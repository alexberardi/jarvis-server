// Package computer is the computer-task loop (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 10):
// observe the page or app, ask the model for a short batch of actions, decide each one against the
// household's rules, send the batch to the agent, and repeat until the model says done. Budgets
// (rounds, minutes), the last two screenshots, a durable stop flag and one audit row per action
// are part of it. The errand step that drives it is piece 11.
package computer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	// verifyRounds is the extra budget after a done whose check failed: this many more rounds to
	// fix it and say done again.
	verifyRounds = 5
)

// LLM is the llm module in process.
type LLM interface {
	Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error)
}

// Executor runs a run's observations and actions on the computer (the agent's computer_observe,
// computer_act, computer_files and computer_stop, through the node bus).
type Executor interface {
	Observe(ctx context.Context, o Observe) (Observation, error)
	Act(ctx context.Context, b Batch) (ActResult, error)
	Files(ctx context.Context, f Files) (FileInfo, error)
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
	Workflows WorkflowStore // nil: a replay's healed steps aren't stored
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
	// StartURL is where the task's window opens on its first look ("" = https://<site>): the web
	// address the user gave, with its scheme, port and path.
	StartURL string
	Success  *SuccessCheck
	Rules    string // the household's rules as one line for the model ("" = the defaults)
	UseCloud bool
	// Approved are the digests of held actions the user approved: the loop mints their tokens
	// and runs the held batch before anything else.
	Approved []string
	// ApprovedReason is what the audit log says approved them ("" = "approved by the user").
	ApprovedReason string
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
	Summary  string // done: the model's summary and what was checked; failed/stopped: the reason, for the user
	Checked  string // done: what Jarvis checked before reporting it
	Ask      string // ask: the question
	Reason   string // ask: page_instructions | unclear | login_needed
	Pending  []PendingAction
	Checkout *Checkout
	Verify   *SuccessCheck // done: the check that passed
	// Downloads are the files a browser task downloaded (done), by name in ~/Downloads.
	Downloads []string
	Rounds    int
	Healed    []Heal // a replay's done: the steps whose element moved (stored when Loop.Workflows is set)
	// Err and the prompt size are a failed model call's diagnostics, for the log, not the user:
	// the call's own error, and the engine's n_prompt_tokens when it reported one (PromptTokens)
	// or else an estimate of the text alone (PromptTokensEst, plus PromptImages screenshots).
	Err             string
	PromptTokens    int
	PromptTokensEst int
	PromptImages    int
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
	StartURL   string // the task's (Task.StartURL), for the executor to open its window on
	Snapshot   bool
	Screenshot bool
}

// Observation is what the executor saw.
type Observation struct {
	URL, Title, App string
	Gen             int
	Elements        []Element
	Text            string // the page's visible text (capped by the agent), "" when unread
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

// Batch is one computer_act call. Resume continues a desktop task the user paused by using the
// computer (its Paused card's Continue).
type Batch struct {
	RunID   string
	Target  computeruse.Target
	Actions []computeruse.Action
	Resume  bool
}

// ActResult is computer_act's answer: a result per action that ran, in order (the agent stops at
// the first failure).
type ActResult struct {
	Results []StepResult
	Paused  bool // the user took over the computer
}

// StepResult is one action's result. NewApp is the app a desktop action was the task's first in
// (shown and audited, Q4); Text is what a clipboard_read read (content, like the page).
type StepResult struct {
	I      int
	OK     bool
	Code   string // denied, needs_approval, stale_ref, stopped, paused, not_attended, …
	Error  string
	NewApp string `json:"new_app"`
	Text   string
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
	hint     string // a replay's fallback round: where the step was recorded
	looked   bool   // this Run call has observed once
	wstep    int    // a replay's step (1-based) the round's actions belong to
	heal     *RecordedElement
	// resume: the run was paused (the user used the computer); its next batch resumes it.
	resume bool
	// clipboard is what the last batch read from the clipboard, for the next prompt only.
	clipboard string
	// downloadsBefore are the files in Downloads when this Run call began (nil: not a browser task), so
	// done can name the ones the task downloaded.
	downloadsBefore map[string]bool
	// fresh: the run began in this Run call, so downloadsBefore are all from before the task.
	fresh bool
	// checkoutAsked: a batch that buys something came without a checkout, and was sent back.
	checkoutAsked bool
	// sendAsked counts the dones over a filled-in form that wasn't sent, sent back (at most 2).
	sendAsked int
}

// downloads are the names of the finished files in Downloads, nil when it can't be listed.
func (l *Loop) downloads(ctx context.Context, runID string) map[string]bool {
	info, err := l.Exec.Files(ctx, Files{RunID: runID, Op: "list", Path: DownloadsDir})
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, e := range info.Entries {
		if !e.Dir && !hasSuffix(strings.ToLower(e.Name), partialSuffixes) {
			out[e.Name] = true
		}
	}
	return out
}

// newDownloads are the files in Downloads that weren't there when the Run call began, sorted.
func (l *Loop) newDownloads(ctx context.Context, r *round) []string {
	if r.downloadsBefore == nil {
		return nil
	}
	var out []string
	for name := range l.downloads(ctx, r.run.ID) {
		if !r.downloadsBefore[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// downloadButton is obs's first element named for downloading, else nil.
func downloadButton(obs Observation) *Element {
	for i, e := range obs.Elements {
		if (e.Role == "button" || e.Role == "link") && strings.Contains(strings.ToLower(e.Name), "download") {
			return &obs.Elements[i]
		}
	}
	return nil
}

// unsentForm is the submit button of the form on obs when the run's last actions filled fields
// in (set_value, select, type) and pressed nothing since, else nil.
func unsentForm(history []Entry, obs Observation) *Element {
	filled := false
	for i := len(history) - 1; i >= 0 && !filled; i-- {
		switch history[i].Kind {
		case "set_value", "select", "type":
			filled = true
		case "press", "click", "keypress", "navigate":
			return nil
		}
	}
	if !filled {
		return nil
	}
	for i, e := range obs.Elements {
		el := &computeruse.Element{Role: e.Role, Name: e.Name, Type: e.Type}
		if e.Role == "button" && computeruse.Classify(computeruse.Action{Kind: "press", Ref: e.Ref}, el, computeruse.Target{Kind: "browser"}).Category == computeruse.CatSubmitThirdParty {
			return &obs.Elements[i]
		}
	}
	return nil
}

// Run drives the task until it is done, needs the user, fails, is stopped or runs out of budget.
// History is saved after every round, so a stop, a crash or a confirmation loses nothing.
func (l *Loop) Run(ctx context.Context, t Task) Outcome {
	run, paused, err := l.open(ctx, t)
	if err != nil {
		return Outcome{RunID: t.RunID, State: StateFailed, Summary: "I couldn't start the computer task: " + err.Error()}
	}
	sc := settings.Scope{HouseholdID: t.HouseholdID}
	pick, err := pickLabel(ctx, l.Models, l.str(ctx, SettingModel, sc, ModelAuto), t.UseCloud)
	if err != nil {
		return l.end(ctx, run, Outcome{State: StateFailed, Summary: err.Error()})
	}
	run.Model = pick.Model
	r := &round{run: run, task: t, pick: pick, seen: map[string]bool{}, resume: paused, fresh: run.Rounds == 0}
	maxRounds := l.int(ctx, SettingMaxRounds, sc, defaultMaxRounds)
	deadline := l.now().Add(time.Duration(l.int(ctx, SettingMaxMinutes, sc, defaultMaxMinutes)) * time.Minute)

	if out, blocked := l.blockedSite(ctx, r); blocked {
		return l.end(ctx, run, out)
	}
	if t.Target.Kind == "browser" {
		// No Downloads folder yet (a fresh profile): nothing was there.
		if r.downloadsBefore = l.downloads(ctx, run.ID); r.downloadsBefore == nil {
			r.downloadsBefore = map[string]bool{}
		}
	}
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
		if v := run.Verify; v != nil && v.FailedAt > 0 && (run.Rounds-v.FailedAt >= verifyRounds || run.Rounds >= maxRounds) {
			return l.end(ctx, run, Outcome{State: StateFailed, Summary: v.couldntConfirm()})
		}
		if run.Rounds >= maxRounds {
			return l.end(ctx, run, Outcome{State: StateFailed,
				Summary: fmt.Sprintf("I stopped after %d rounds without finishing.", run.Rounds)})
		}
		if !l.now().Before(deadline) {
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

// blockedSite ends a browser task whose site the household blocks before anything opens it: the
// actions on the page would be refused anyway, after the page was loaded and read.
func (l *Loop) blockedSite(ctx context.Context, r *round) (Outcome, bool) {
	t := r.task
	if t.Target.Kind != "browser" {
		return Outcome{}, false
	}
	start := t.StartURL
	if start == "" && t.Target.Site != "" {
		start = "https://" + t.Target.Site
	}
	if start == "" {
		return Outcome{}, false
	}
	open := computeruse.Action{Kind: "navigate", URL: start, Intent: "Open " + start}
	d := l.decide(ctx, t, t.Target, Observation{}, open)
	if d.verdict.Effect != policy.Block {
		return Outcome{}, false
	}
	l.record(ctx, r, d, policy.OutcomeBlocked, "blocked: "+d.verdict.Reason)
	return Outcome{State: StateFailed, Summary: fmt.Sprintf("I can't use %s: %s.", d.site, d.verdict.Reason)}, true
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
	obs, stop := l.look(ctx, r)
	if stop != nil {
		return *stop, true
	}
	reply, err := l.ask(ctx, r, obs)
	if err != nil {
		out := Outcome{State: StateFailed, Summary: err.Error()}
		var me *modelError
		if errors.As(err, &me) {
			out.Err, out.PromptImages = me.err.Error(), me.images
			if out.PromptTokens = enginePromptTokens(me.err); out.PromptTokens == 0 {
				out.PromptTokensEst = me.textTokens
			}
		}
		return out, true
	}
	r.feedback, r.clipboard = "", ""
	switch {
	case reply.Done:
		if btn := unsentForm(r.run.History, obs); btn != nil && r.sendAsked < 2 {
			// The house model fills a form in and says done without sending it, and may say
			// it again after the first nudge: the second one writes the action out.
			r.sendAsked++
			r.feedback = fmt.Sprintf(`you said done, but the form isn't sent: press %q (%s) to send it, unless the task says not to`,
				btn.Name, btn.Ref)
			if r.sendAsked == 2 {
				r.feedback = fmt.Sprintf(`you said done again, but the form still isn't sent. Unless the task says not to send it, answer exactly {"actions": [{"kind": "press", "ref": %q, "intent": "Send the form"}]}`,
					btn.Ref)
			}
			return Outcome{}, false
		}
		return l.done(ctx, r, reply, obs)
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

// look observes for the model: new page text that reads like instructions to it ends the call
// (stop), and a screenshot joins the ones the model sees.
func (l *Loop) look(ctx context.Context, r *round) (Observation, *Outcome) {
	obs, err := l.observe(ctx, r)
	if err != nil {
		return obs, &Outcome{State: StateFailed, Summary: "I couldn't see the computer: " + err.Error()}
	}
	// After the user answered a question, what was on the page then is not news.
	answered := r.task.Answer != "" && !r.looked
	r.looked = true
	for _, s := range pageTexts(obs) {
		if !r.seen[s] && !answered && injectionLike(s) {
			return obs, &Outcome{State: StateAsk, Ask: injectionAsk, Reason: ReasonPageInstructions}
		}
		r.seen[s] = true
	}
	if obs.Image != nil && r.pick.Vision {
		r.shots = append(r.shots, obs.Image)
		if len(r.shots) > maxScreenshots {
			r.shots = r.shots[len(r.shots)-maxScreenshots:]
		}
	}
	return obs, nil
}

// done checks the result before the task reports done (C4): the task's own success check, else
// the one the model's done carries. A task's check said only in words (Screen: the planner's
// "an order confirmation page") is checked together with the model's checks of the address, the
// text, a download or a file: both must hold, the model's never replace the task's (the house
// model checked "checkout" on the checkout page, before ordering). A done without a check is sent
// back once for one. A check that fails goes back to the model as feedback, with verifyRounds more
// rounds to fix it.
func (l *Loop) done(ctx context.Context, r *round, reply Reply, obs Observation) (Outcome, bool) {
	check := r.task.Success
	switch {
	case check.empty():
		check = reply.Verify
	case check.screenOnly() && reply.Verify.concrete():
		both := *reply.Verify
		both.Screen = check.Screen
		check = &both
	}
	if check != nil && check.Download != nil && r.fresh && len(r.downloadsBefore) > 0 {
		// A file that was there before the task (an earlier task's statement) isn't its download.
		c, d := *check, *check.Download
		d.before = r.downloadsBefore
		c.Download = &d
		check = &c
	}
	if r.run.Verify == nil {
		r.run.Verify = &VerifyState{}
	}
	v := r.run.Verify
	if check.empty() {
		if !v.Asked {
			v.Asked = true
			r.feedback = `you said done but gave no "verify" check, so I can't confirm it. If the task is finished, say done again with a "verify" that shows it (url_contains, text_present, download, file or screen); if not, carry on.`
			return Outcome{}, false
		}
		return Outcome{State: StateFailed, Summary: "I couldn't confirm the task was finished: there was no way to check it."}, true
	}
	res := l.Verify(ctx, r.run.ID, r.task.Target, r.pick, check)
	if res.OK {
		sum := strings.TrimSpace(reply.Summary)
		if sum != "" && !strings.HasSuffix(sum, ".") {
			sum += "."
		}
		got := l.newDownloads(ctx, r)
		if len(got) > 0 {
			// The errand's next steps read them by name (read_computer_file).
			sum += " Downloaded: " + strings.Join(got, ", ") + " (in " + DownloadsDir + ")."
		}
		return Outcome{State: StateDone, Summary: strings.TrimSpace(sum + " Checked: " + res.Checked + "."), Downloads: got,
			Checked: res.Checked, Verify: check}, true
	}
	if v.FailedAt == 0 {
		v.FailedAt = r.run.Rounds
	}
	v.What, v.Why = check.describe(), res.Why
	why := res.Why
	if check.Download != nil && strings.Contains(why, "Downloads") {
		if btn := downloadButton(obs); btn != nil {
			// The house model opens a statement's page and takes that for its download.
			why += fmt.Sprintf(". Nothing was downloaded yet: press %q (%s) to download it", btn.Name, btn.Ref)
		}
	}
	r.feedback = "you said done, but my check failed: " + why +
		". So the task isn't finished: don't say done now; answer with the action it still needs on the " +
		"current page (e.g. press the button that sends, submits or completes it), and say done once it holds."
	return Outcome{}, false
}

// observe takes a snapshot, and a screenshot for the desktop, when the model asked for one, or
// when the snapshot has too few elements to work from.
func (l *Loop) observe(ctx context.Context, r *round) (Observation, error) {
	t := r.task
	desktop := t.Target.Kind == "desktop"
	o := Observe{RunID: r.run.ID, Target: t.Target, StartURL: t.StartURL, Snapshot: true, Screenshot: desktop || r.wantShot}
	r.wantShot = false
	obs, err := l.Exec.Observe(ctx, o)
	if err != nil {
		return obs, err
	}
	if obs.Image == nil && len(obs.Elements) < fewElements {
		shot, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: t.Target, StartURL: t.StartURL, Screenshot: true})
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
		Feedback: r.feedback, Hint: r.hint, Clipboard: r.clipboard, Desktop: t.Target.Kind == "desktop",
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
			return Reply{}, &modelError{err: err, textTokens: (len(systemPrompt) + len(text)) / 4, images: len(r.shots)}
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

// modelError is a model call that failed: the user's message, and what the log says about it.
type modelError struct {
	err        error
	textTokens int // estimated, at about four bytes a token
	images     int
}

func (e *modelError) Error() string { return fmt.Sprintf("The model didn't answer: %v", e.err) }

func (e *modelError) Unwrap() error { return e.err }

var nPromptTokens = regexp.MustCompile(`"n_prompt_tokens"\s*:\s*(\d+)`)

// enginePromptTokens is the prompt size an engine's error reports (a context overflow's
// n_prompt_tokens), 0 when it has none.
func enginePromptTokens(err error) int {
	var ae *llm.APIError
	if !errors.As(err, &ae) {
		return 0
	}
	m := nPromptTokens.FindStringSubmatch(ae.Message)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// decision is one action's verdict.
type decision struct {
	action  computeruse.Action
	risk    computeruse.Risk
	target  string // the policy target that decided it
	site    string
	app     string
	verdict policy.Verdict
	newApp  string // the app the executor said the action was the task's first in
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
	if len(pending) > 0 && co == nil && !r.checkoutAsked {
		for _, p := range pending {
			if p.Category == computeruse.CatPay {
				// The confirmation shows what is bought and for how much: ask the model once.
				r.checkoutAsked = true
				r.feedback = `these actions buy something, so the user confirms them first: answer again with the same actions and a "checkout" read from the page (product, price, currency, seller, ship_to, card)`
				return Outcome{}, false
			}
		}
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
	reason := r.task.ApprovedReason
	if reason == "" {
		reason = "approved by the user"
	}
	ds := make([]decision, len(held.Actions))
	for i, h := range held.Actions {
		ds[i] = decision{action: h.Action, site: siteHost(tgt.Site), app: tgt.App,
			verdict: policy.Verdict{Effect: policy.Allow, Reason: reason}}
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
	b := Batch{RunID: r.run.ID, Target: tgt, Resume: r.resume}
	for _, d := range ds {
		b.Actions = append(b.Actions, d.action)
	}
	res, err := l.Exec.Act(ctx, b)
	if err == nil {
		r.resume = false
	}
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
			result := "ok"
			if sr.NewApp != "" {
				// The first time the task used this app: allowed (Q4), and said.
				result = "ok (first use of " + oneLine(sr.NewApp) + ")"
				d.newApp = sr.NewApp
			}
			if d.action.Kind == "clipboard_read" {
				r.clipboard = sr.Text
			}
			l.record(ctx, r, d, policy.OutcomeRan, result)
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
		Intent: d.action.Intent, Ref: d.action.Ref, Result: result, WStep: r.wstep, Healed: r.heal})
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
	detail := map[string]any{"intent": d.action.Intent, "kind": d.action.Kind, "target": d.target,
		"site": d.site, "app": d.app, "result": result}
	if d.newApp != "" {
		detail["new_app"] = d.newApp
	}
	_ = l.Audit.Record(ctx, policy.Event{
		HouseholdID: r.task.HouseholdID, UserID: r.task.UserID, Source: SourceComputer, SourceID: r.run.ID,
		Tool: policy.ToolComputerTask, Effect: effect, Outcome: outcome, RuleID: ruleID, Reason: d.verdict.Reason,
		Detail: detail,
	})
}

// open creates the task's run, or loads the one it resumes; paused: that one was paused.
func (l *Loop) open(ctx context.Context, t Task) (run *Run, paused bool, err error) {
	if t.RunID == "" {
		kind := t.Target.Kind
		if kind == "" {
			kind = "browser"
		}
		run, err := l.Store.Create(ctx, Run{HouseholdID: t.HouseholdID, UserID: t.UserID, NodeID: t.NodeID,
			WorkflowID: t.WorkflowID, Step: t.Step, Goal: t.Goal, Target: kind})
		return run, false, err
	}
	run, err = l.Store.Get(ctx, t.RunID)
	if err != nil {
		return nil, false, err
	}
	switch run.State {
	case RunDone, RunFailed, RunStopped:
		return nil, false, fmt.Errorf("the task already ended (%s)", run.State)
	}
	paused = run.State == RunPaused
	run.State = RunRunning
	return run, paused, nil
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
	if run.State == RunDone || run.State == RunFailed {
		// The computer's side of the task ends with it: its "controlling" indicator, and a
		// desktop task's guard. (A browser task's left the agent busy: no recording could start.)
		l.Exec.Stop(ctx, run.ID)
	}
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

// pageTexts are the observation's texts the injection check and the text checks read: the
// title, each element's name and value, and each line of the page's text.
func pageTexts(o Observation) []string {
	out := []string{o.Title}
	for _, e := range o.Elements {
		out = append(out, e.Name, e.Value)
	}
	for _, l := range strings.Split(o.Text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
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
