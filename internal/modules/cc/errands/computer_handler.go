package errands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Computer tasks in errands (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 11): the planner may
// emit a computer_task step when the household has a computer with computer control on; its
// handler drives computer.Loop on that computer. A risky moment (a checkout) parks the errand on
// one confirmation card, a question parks it on a question card, and the run's card shows the
// last few things the task did. Stop sets the task's durable stop flag, whatever kind of errand
// it is in. Each action is decided against the household's rules inside the loop, however the
// errand was started (a live run_errand too), so the step itself is never skipped past them.

const (
	cmdComputer = policy.ToolComputerTask

	// SettingAutoApproveCents is the computer tasks' spend limit (0 = off).
	SettingAutoApproveCents = computer.SettingAutoApproveCents

	// CallbackAnswerStep is a computer task's question card's Continue, with the typed answer.
	CallbackAnswerStep  = "answer_errand_step"
	questionTitlePrefix = "❓ Errand question: "
	progressTitlePrefix = "🖥️ Errand: "

	// progressIntents is how many of the task's last actions its card lists.
	progressIntents = 5
	// heldResult is a history entry's result for an action held for the user (computer.Loop).
	heldResult = "waiting for the user"
)

// ComputerTasks runs computer_task steps.
type ComputerTasks struct {
	Loop *computer.Loop
	// Targets are the household's computers a computer task may run on (computer control on,
	// the agent's computer tools), in preference order; off says why there are none, for the user.
	Targets func(ctx context.Context, hh string) (nodeIDs []string, off string)
}

// computerEntry is computer_task in the planner's menu.
func computerEntry() MenuEntry {
	return MenuEntry{Command: cmdComputer, IsRisky: false,
		Description: "Do a multi-step task on the user's computer: in their browser (shopping, bank sites, forms) " +
			"or in a desktop app. Args: goal, target browser|desktop, site?, app?, success?",
		Args: servertools.Obj(
			"goal", "what to get done, in the user's words with every detail they gave",
			"target", "browser or desktop",
			"site", "the website to start on, e.g. amazon.com (browser)",
			"app", "the app to use (desktop)",
			"success", "how to tell it worked, e.g. an order confirmation page",
		)}
}

// computerOffered reports whether the household has a computer a computer task can run on.
func (s *Service) computerOffered(ctx context.Context, hh string) bool {
	if s.Computer == nil || s.Computer.Loop == nil || s.Computer.Targets == nil {
		return false
	}
	ids, _ := s.Computer.Targets(ctx, hh)
	return len(ids) > 0
}

type computerHandler struct{}

func (computerHandler) Match(_ *Service, cmd string) bool { return cmd == cmdComputer }

func (computerHandler) Run(ctx context.Context, s *Service, rc *runCtx, cmd string, args *pyjson.Object, label string, i int) (Result, *Suspend) {
	failed := func(msg string) (Result, *Suspend) { return Result{Command: cmd, Label: label, Error: msg}, nil }
	if s.Computer == nil || s.Computer.Loop == nil {
		return failed("computer tasks are unavailable")
	}
	if rc.workflowID == "" {
		return failed("computer step has no workflow link")
	}
	t := computer.Task{HouseholdID: rc.householdID, UserID: rc.userID, WorkflowID: rc.workflowID, Step: i,
		Goal:   parse.PyStrip(firstNonEmpty(truthyStr(args, "goal"), rc.goal)),
		Target: computeruse.Target{Kind: "browser", Site: siteOf(truthyStr(args, "site")), App: parse.PyStrip(truthyStr(args, "app"))},
	}
	if truthyStr(args, "target") == "desktop" {
		t.Target.Kind = "desktop"
	}
	if ok := parse.PyStrip(truthyStr(args, "success")); ok != "" {
		t.Success = &computer.SuccessCheck{Screen: ok}
	}
	// A run of this step that hasn't ended is resumed (after its confirmation or question).
	run, err := s.Computer.Loop.Store.ForStep(ctx, rc.workflowID, i)
	switch {
	case err == nil && (run.State == computer.RunWaiting || run.State == computer.RunPaused):
		t.RunID, t.NodeID = run.ID, run.NodeID
		if sig := rc.signal; sig != nil {
			t.Approved, t.Answer = sig.Approved, sig.Answer
		}
	case err != nil && !errors.Is(err, computer.ErrNoRun):
		return failed("I couldn't load the computer task: " + err.Error())
	default:
		ids, off := s.Computer.Targets(ctx, rc.householdID)
		if len(ids) == 0 {
			return failed(firstNonEmpty(off, "There's no computer with computer control on."))
		}
		t.NodeID = ids[0]
	}
	w := rc.wf
	if w != nil {
		s.ensureProgressCard(ctx, w, *rc.results)
		t.Progress = func(*computer.Run) { s.updateRanCard(ctx, w, *rc.results, "running", "") }
	}

	out := s.Computer.Loop.Run(ctx, t)
	var note string // a checkout approved within the spend limit, said on the card
	for out.State == computer.StateNeedsApproval {
		limit, ok := s.withinSpendLimit(ctx, rc.householdID, out)
		if !ok {
			break
		}
		note = strings.TrimSpace(note + " " + fmt.Sprintf("Approved on its own: %s is within your %s spend limit.",
			out.Checkout.Price, dollars(limit)))
		if w != nil {
			s.updateRanCard(ctx, w, *rc.results, "running", note)
		}
		t.RunID, t.Answer, t.Approved = out.RunID, "", digests(out.Pending)
		t.ApprovedReason = "within your " + dollars(limit) + " spend limit"
		out = s.Computer.Loop.Run(ctx, t)
	}
	s.log().Info("errands: step (computer)", "run", out.RunID, "state", out.State, "rounds", out.Rounds)
	switch out.State {
	case computer.StateDone:
		msg := strings.TrimSpace(firstNonEmpty(out.Summary, "Done.") + " " + note)
		return Result{Command: cmd, Label: label, Success: true, Message: msg, Summary: out.Summary}, nil
	case computer.StateNeedsApproval:
		return Result{}, &Suspend{Cursor: i, Kind: signalApproval, Confirm: true,
			Computer: &computerWait{Checkout: out.Checkout, Pending: out.Pending}}
	case computer.StateAsk:
		return Result{}, &Suspend{Cursor: i, Kind: signalApproval, Confirm: true, Computer: &computerWait{Ask: out.Ask}}
	case computer.StatePaused:
		return Result{}, &Suspend{Cursor: i, Kind: signalApproval, Confirm: true,
			Computer: &computerWait{Ask: "I paused because you're using the computer. Tap Continue when it's free again."}}
	case computer.StateStopped:
		return Result{Command: cmd, Label: label, Error: firstNonEmpty(out.Summary, "Stopped."), stopped: true}, nil
	}
	return failed(firstNonEmpty(out.Summary, "the computer task failed"))
}

// computerWait is what a computer step parked on: a confirmation of its held actions (with the
// checkout they complete, when they buy something) or a question.
type computerWait struct {
	Checkout *computer.Checkout
	Pending  []computer.PendingAction
	Ask      string
}

func digests(pending []computer.PendingAction) []string {
	var out []string
	for _, p := range pending {
		if p.Digest != "" {
			out = append(out, p.Digest)
		}
	}
	return out
}

// withinSpendLimit reports whether a held checkout is approved without asking: the household set
// a spend limit, its price is at most the limit, and everything held is a payment.
func (s *Service) withinSpendLimit(ctx context.Context, hh string, out computer.Outcome) (int64, bool) {
	if s.Settings == nil || out.Checkout == nil || len(out.Pending) == 0 {
		return 0, false
	}
	limit := s.Settings.Int(ctx, SettingAutoApproveCents, settings.Scope{HouseholdID: hh})
	if limit <= 0 {
		return 0, false
	}
	price, ok := priceCents(out.Checkout.Price)
	if !ok || price > limit {
		return 0, false
	}
	for _, p := range out.Pending {
		if p.Category != computeruse.CatPay {
			return 0, false
		}
	}
	return limit, true
}

// priceCents reads a price as the model reports it ("$12.99", "12,99 €", "1,299.00") in cents.
// It is on the auto-pay path, so anything it could under-read fails: more than one number
// ("$12.99 + $5.00 shipping"), or letters other than one currency code ("$1.2k", "Free").
func priceCents(s string) (int64, bool) {
	m := priceRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n := m[1]
	if i := strings.LastIndexAny(n, ".,"); i >= 0 && len(n)-i-1 == 2 {
		n = strings.NewReplacer(",", "", ".", "").Replace(n[:i]) + "." + n[i+1:]
	} else {
		n = strings.NewReplacer(",", "", ".", "").Replace(n)
	}
	f, err := strconv.ParseFloat(n, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(math.Round(f * 100)), true
}

// priceRe is one number, with a currency sign or a three-letter code (USD) on either side.
var priceRe = regexp.MustCompile(`^(?:[A-Z]{3}\s*)?[$€£¥₹]?\s*([0-9][0-9.,]*)\s*[$€£¥₹]?(?:\s*[A-Z]{3})?$`)

func dollars(cents int64) string { return fmt.Sprintf("$%d.%02d", cents/100, cents%100) }

// siteOf is a site argument as a host ("https://www.shop.example/x" → "www.shop.example").
func siteOf(site string) string {
	site = strings.TrimSpace(site)
	site = strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
	if i := strings.IndexAny(site, "/?#"); i >= 0 {
		site = site[:i]
	}
	return strings.ToLower(site)
}

// --- cards ---

// ComputerConfirmCard is a computer task's confirmation: its body (what will be bought, or the
// held actions) and metadata.checkout.
func ComputerConfirmCard(co *computer.Checkout, pending []computer.PendingAction) (body string, checkout map[string]any) {
	var lines []string
	if co != nil {
		price := co.Price
		if co.Currency != "" && !strings.Contains(price, co.Currency) && strings.IndexFunc(price, isCurrencySign) < 0 {
			price += " " + co.Currency
		}
		for _, l := range [][2]string{{"Product", co.Product}, {"Price", price}, {"Seller", co.Seller},
			{"Ship to", co.ShipTo}, {"Card", cardLast4(co.Card)}} {
			if l[1] != "" {
				lines = append(lines, l[0]+": "+l[1])
			}
		}
		checkout = map[string]any{"product": co.Product, "price": co.Price, "currency": co.Currency,
			"seller": co.Seller, "ship_to": co.ShipTo, "card": co.Card}
	}
	if len(lines) == 0 || len(pending) > 1 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "I'll:")
		for _, p := range pending {
			lines = append(lines, "- "+firstNonEmpty(p.Action.Intent, p.Action.Kind))
		}
	}
	return strings.Join(lines, "\n"), checkout
}

func isCurrencySign(r rune) bool { return strings.ContainsRune("$€£¥₹", r) }

func cardLast4(c string) string {
	if c = strings.TrimSpace(c); c == "" {
		return ""
	}
	return "•••• " + c
}

// postComputerCard posts the card a computer step parked on: the confirmation (Approve /
// Cancel) or the question (Continue with an answer / Stop).
func (s *Service) postComputerCard(ctx context.Context, w *workflowRow, i int, cw *computerWait) {
	if s.Cards == nil || i < 0 || i >= len(w.Steps) {
		return
	}
	st := w.Steps[i]
	label := firstNonEmpty(st.Label, parse.PyStrip(st.Command))
	ident := func() map[string]any { return map[string]any{"workflow_id": w.ID, "step": i, "revision": w.Revision} }
	md := map[string]any{"household_id": w.HouseholdID, "workflow_id": w.ID, "revision": w.Revision, "step": i,
		"steps": []Step{st}}
	var title, summary, body string
	if cw.Ask != "" {
		answer := ident()
		answer["instruction"] = ""
		title, summary = questionTitlePrefix+label, cw.Ask
		body = cw.Ask + "\n\nType your answer above and tap **Continue**, or tap **Stop** to end the errand here."
		md["editable_fields"] = []any{map[string]any{"label": "Your answer", "initial": "", "data_key": "instruction",
			"input_type": "text", "required": false}}
		md["interactive_elements"] = []any{
			element("continue-step", "Continue", CallbackAnswerStep, answer),
			element("stop-errand", "Stop", CallbackStop, map[string]any{"workflow_id": w.ID, "revision": w.Revision}),
		}
	} else {
		lines, co := ComputerConfirmCard(cw.Checkout, cw.Pending)
		title = stepTitlePrefix + label
		summary = "“" + w.DisplayTitle() + "” is ready to go ahead; tap Approve to let it."
		if cw.Checkout != nil && cw.Checkout.Product != "" {
			summary = "Buy " + cw.Checkout.Product + "?"
			if cw.Checkout.Price != "" {
				summary = "Buy " + cw.Checkout.Product + " for " + cw.Checkout.Price + "?"
			}
		}
		body = lines + "\n\nTap **Approve** to go ahead, or **Cancel**: nothing is bought or sent."
		if co != nil {
			md["checkout"] = co
		}
		md["interactive_elements"] = []any{
			element("approve-step", "Approve", CallbackApproveStep, ident()),
			element("stop-errand", "Cancel", CallbackStop, map[string]any{"workflow_id": w.ID, "revision": w.Revision}),
		}
	}
	s.Cards.Post(ctx, Card{HouseholdID: w.HouseholdID, UserID: w.UserID, Title: title, Summary: summary, Body: body,
		Category: planCategory, Metadata: md, Push: true, TargetType: targetType(w.UserID)})
}

// --- progress ---

// hasComputerStep reports whether the run's plan has a computer task.
func (w *workflowRow) hasComputerStep() bool {
	for _, st := range w.Steps {
		if parse.PyStrip(st.Command) == cmdComputer {
			return true
		}
	}
	return false
}

// tracksProgress reports whether the run keeps a card that is updated in place as it goes: an
// autorun run's "Ran on its own" card, or the progress card of an errand with a computer task.
func (w *workflowRow) tracksProgress() bool { return w.Kind == KindAutorun || w.hasComputerStep() }

// ensureProgressCard posts the progress card of an errand the user started, when its computer
// step begins (an autorun run has its "Ran on its own" card already).
func (s *Service) ensureProgressCard(ctx context.Context, w *workflowRow, results []Result) {
	if s.Cards == nil || w.InboxItemID != "" || w.Kind == KindAutorun {
		return
	}
	c := s.ranCardOf(w, s.ranCard(ctx, w, results, "running"), "")
	c.Push = false // the user just tapped Run; the confirmation and the completion push
	id := s.Cards.Post(ctx, c)
	if id == "" {
		return
	}
	w.InboxItemID = id
	if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows SET inbox_item_id = ?, updated_at = ? WHERE id = ?`,
		id, dbTime(s.now()), w.ID); err != nil {
		s.log().Warn("errands: store progress card id", "workflow", w.ID, "err", err)
	}
}

// computerProgress is a computer step's line on the card: its last few intents, and while it
// hasn't ended, "Working in <site|app>: <last intent>".
func (s *Service) computerProgress(ctx context.Context, w *workflowRow, i int, st Step) (line string, intents []string, ok bool) {
	if s.Computer == nil || s.Computer.Loop == nil || s.Computer.Loop.Store == nil {
		return "", nil, false
	}
	run, err := s.Computer.Loop.Store.ForStep(ctx, w.ID, i)
	if err != nil {
		return "", nil, false
	}
	for _, h := range run.History {
		// What the task did: not the actions it held for the user, nor the ones that never ran.
		if h.Intent != "" && h.Result != heldResult && h.Result != "not run" {
			intents = append(intents, h.Intent)
		}
	}
	if len(intents) > progressIntents {
		intents = intents[len(intents)-progressIntents:]
	}
	where := firstNonEmpty(siteOf(truthyStr(st.Args, "site")), parse.PyStrip(truthyStr(st.Args, "app")),
		map[string]string{"desktop": "the app"}[run.Target], "the browser")
	switch run.State {
	case computer.RunRunning:
		line = "Working in " + where
		if len(intents) > 0 {
			line += ": " + intents[len(intents)-1]
		}
	case computer.RunWaiting, computer.RunPaused:
		line = "Waiting for you in " + where
	}
	return line, intents, true
}

// --- taps ---

// approveComputer is the confirmation's Approve: the run resumes with exactly the held actions'
// digests approved (the loop mints their tokens). ok=false: the step isn't a computer task
// waiting on a confirmation.
func (s *Service) approveComputer(ctx context.Context, w *workflowRow, step int) (resumeJob, bool) {
	if s.Computer == nil || s.Computer.Loop == nil || parse.PyStrip(w.Steps[step].Command) != cmdComputer {
		return resumeJob{}, false
	}
	run, err := s.Computer.Loop.Store.ForStep(ctx, w.ID, step)
	if err != nil || run.State != computer.RunWaiting || run.Pending == nil {
		return resumeJob{}, false
	}
	j := resumeJob{WorkflowID: w.ID, Step: step - 1, Kind: signalConfirm}
	for _, h := range run.Pending.Actions {
		if h.Digest != "" {
			j.Approved = append(j.Approved, h.Digest)
		}
	}
	return j, true
}

// handleAnswerStep is a computer task's question card's Continue: the run resumes with the typed
// answer (or just carries on without one).
func (s *Service) handleAnswerStep(ctx context.Context, c CallbackContext) CallbackResult {
	if !s.Available() {
		return fail("Errands are unavailable right now.")
	}
	id := dataStr(c.Data, "workflow_id")
	if id == "" {
		return fail("Missing workflow_id")
	}
	step, ok := dataInt(c.Data, "step")
	if !ok {
		return fail("Missing step")
	}
	w, err := s.workflowIn(ctx, id, c.HouseholdID)
	if err != nil {
		return fail("Couldn't load the errand.")
	}
	if w == nil {
		return fail("Errand not found")
	}
	if w.State != "waiting" || w.WaitingOn != signalApproval || w.Cursor != step || step < 0 || step >= len(w.Steps) ||
		parse.PyStrip(w.Steps[step].Command) != cmdComputer {
		return handled(c.HouseholdID, "That question was already answered.")
	}
	if !revisionsMatch(c.Data["revision"], w.Revision) {
		return fail("This errand was updated — open the latest card.")
	}
	answer := strings.TrimSpace(dataStr(c.Data, "instruction"))
	if err := s.enqueueResume(ctx, resumeJob{WorkflowID: w.ID, Step: step - 1, Kind: signalConfirm,
		Answer: firstNonEmpty(answer, "Carry on.")}); err != nil {
		return fail("Couldn't continue the errand.")
	}
	s.log().Info("errands: computer question answered", "workflow", w.ID, "step", step)
	return CallbackResult{Success: true, ContextData: inbox(c.HouseholdID, "👍 Carrying on", "I'll continue “"+w.DisplayTitle()+"”.")}
}

// stopComputer sets the durable stop of the errand's computer task that hasn't ended and tells
// its computer; ended reports that it also lands it stopped (the errand was waiting on it, so
// nothing else will). ok=false: no such task.
func (s *Service) stopComputer(ctx context.Context, workflowID string, ended bool) bool {
	if s.Computer == nil || s.Computer.Loop == nil || s.Computer.Loop.Store == nil {
		return false
	}
	store := s.Computer.Loop.Store
	run, err := store.Active(ctx, workflowID)
	if err != nil {
		return false
	}
	if err := store.RequestStop(ctx, run.ID); err != nil {
		s.log().Warn("errands: computer stop", "run", run.ID, "err", err)
	}
	if ended {
		run.State, run.Pending, run.StopRequested = computer.RunStopped, nil, true
		if err := store.Save(ctx, run); err != nil {
			s.log().Warn("errands: computer stop", "run", run.ID, "err", err)
		}
	}
	if s.Computer.Loop.Exec != nil {
		s.Computer.Loop.Exec.Stop(ctx, run.ID)
	}
	return true
}
