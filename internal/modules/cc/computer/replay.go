package computer

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Replaying a taught workflow (plan P3 piece 31): each step is tried target-first, with no model
// call. The step's recorded element is looked up in a fresh snapshot by role and name (snapshots
// carry no selector or AX path yet: docs/decisions/2026-10-20-replay-target-first.md), and when
// it is there the step's action runs on it directly. When it isn't, or the action fails, one
// model round does the step, with the recorded element as a hint; if that doesn't do it, the run
// asks the user. Every action, direct or not, is decided against the household's rules as in
// Run: risky ones hold for a confirmation and a blocked one ends the run. After the last step the
// workflow's success check runs (piece 12); only then are the healed steps stored.

// Replay runs wf with params as t's computer task, or resumes the run t.RunID after a
// confirmation or a question. t.Goal and t.Target default to the workflow's name and first step.
func (l *Loop) Replay(ctx context.Context, t Task, wf *Workflow, params map[string]string) Outcome {
	steps, err := wf.Bind(params)
	if err != nil {
		return Outcome{RunID: t.RunID, State: StateFailed, Summary: err.Error() + "."}
	}
	if strings.TrimSpace(t.Goal) == "" {
		t.Goal = fmt.Sprintf("Run the workflow %q", wf.Name)
	}
	if t.Target.Kind == "" && len(steps) > 0 {
		t.Target = steps[0].target()
	}
	// The window opens where the workflow starts, as recorded (http too), not on https://<site>.
	if t.StartURL == "" && len(steps) > 0 && steps[0].Target == "browser" && steps[0].Action.Kind == "navigate" {
		t.StartURL = steps[0].Action.URL
	}
	run, paused, err := l.open(ctx, t)
	if err != nil {
		return Outcome{RunID: t.RunID, State: StateFailed, Summary: "I couldn't start the computer task: " + err.Error()}
	}
	sc := settings.Scope{HouseholdID: t.HouseholdID}
	// A run has its own model setting. Its auto is computer tasks' automatic pick (the largest
	// local vision model), not whatever computer.model is set to: auto never means cloud.
	pick, err := pickLabel(ctx, l.Models, l.str(ctx, SettingRunModel, sc, ModelAuto))
	if err != nil {
		return l.end(ctx, run, Outcome{State: StateFailed, Summary: err.Error()})
	}
	run.Model = pick.Model
	// A replay's direct steps have no model to read a checkout from the page: a purchase holds
	// for the user as recorded, without sending the step back for one.
	r := &round{run: run, task: t, pick: pick, seen: map[string]bool{}, resume: paused, checkoutAsked: true,
		wfGoal: firstNonEmpty(wf.Skill.Goal.EndState, wf.Skill.Goal.Reason), sites: strictSites(wf)}
	maxRounds := l.int(ctx, SettingMaxRounds, sc, defaultMaxRounds)
	deadline := l.now().Add(time.Duration(l.int(ctx, SettingMaxMinutes, sc, defaultMaxMinutes)) * time.Minute)

	next := stepsDone(run.History)
	if next == 0 && run.Pending == nil && t.Target.Kind == "browser" {
		// The executor acts only in a window an observe opened: open it first, unless the
		// household blocks the site, as a computer task does.
		if out, blocked := l.blockedSite(ctx, r); blocked {
			return l.end(ctx, run, out)
		}
		if _, err := l.Exec.Observe(ctx, Observe{RunID: run.ID, Target: t.Target, StartURL: t.StartURL, Snapshot: true}); err != nil {
			return l.end(ctx, run, Outcome{State: StateFailed, Summary: "I couldn't see the computer: " + err.Error()})
		}
	}
	if next == 0 && run.Pending == nil {
		// What was true when the task was recorded, made sure of before the first step.
		if out, done := l.prerequisites(ctx, r, wf, params); done {
			return l.end(ctx, run, out)
		}
	}
	if held := run.Pending; held != nil && next < len(steps) {
		run.Pending = nil
		if l.stopRequested(ctx, run) {
			return l.end(ctx, run, Outcome{State: StateStopped, Summary: "Stopped."})
		}
		r.wstep, r.task.Target, r.heal = next+1, steps[next].target(), heldHeal(run.History, next+1)
		n := len(run.History)
		if out, done := l.runApproved(ctx, r, held); done {
			return l.end(ctx, run, out)
		}
		if len(run.History) > n && r.feedback == "" {
			if out, done := l.checkStep(ctx, r, steps[next], next); done {
				return l.end(ctx, run, out)
			}
			markDone(run, steps[next], next+1, r.heal)
			next++
			r.task.Answer = ""
		}
	}
	for i := next; i < len(steps); i++ {
		if l.stopRequested(ctx, run) {
			return l.end(ctx, run, Outcome{State: StateStopped, Summary: "Stopped."})
		}
		if run.Rounds >= maxRounds {
			return l.end(ctx, run, Outcome{State: StateFailed,
				Summary: fmt.Sprintf("I stopped after %d rounds without finishing.", run.Rounds)})
		}
		if !l.now().Before(deadline) {
			return l.end(ctx, run, Outcome{State: StateFailed, Summary: "I ran out of time before finishing."})
		}
		st := steps[i]
		r.wstep, r.task.Target, r.heal, r.hint = i+1, st.target(), nil, ""
		if newApp(steps, i) {
			// The recording opened the app through a launcher, which isn't a step: the agent
			// opens it (from its catalogue) and gives it the focus, so keys go to it (#581).
			if _, err := l.Exec.Observe(ctx, Observe{RunID: run.ID, Target: r.task.Target, Snapshot: true}); err != nil {
				return l.end(ctx, run, Outcome{State: StateFailed,
					Summary: fmt.Sprintf("Step %d (%s): I couldn't open %s: %s.", i+1, oneLine(st.Intent), st.App, strings.TrimSuffix(err.Error(), "."))})
			}
		}
		if awaitingCheck(run.History, i+1) {
			// The step ran in an earlier call and its check failed. The user's word that it is
			// fine stands for the check; with none, look again. It is never run a second time.
			r.heal = heldHeal(run.History, i+1)
			if !vouches(r.task.Answer) {
				// No word that it is fine (no answer, or an instruction): look again, and let
				// the model act on what the user said.
				if out, done := l.checkStep(ctx, r, st, i); done {
					return l.end(ctx, run, out)
				}
			}
		} else {
			out, done := l.replayStep(ctx, r, st, i)
			if done {
				if out.State == StateFailed {
					out.Summary = fmt.Sprintf("Step %d (%s) failed: %s", i+1, oneLine(st.Intent), out.Summary)
				}
				return l.end(ctx, run, out)
			}
			if out, done := l.checkStep(ctx, r, st, i); done {
				return l.end(ctx, run, out)
			}
		}
		markDone(run, st, i+1, r.heal)
		r.task.Answer = "" // the answer was for the step that asked
		if err := l.Store.Save(ctx, run); err != nil {
			return l.end(ctx, run, Outcome{State: StateFailed, Summary: "I couldn't save the task's progress."})
		}
		if t.Progress != nil {
			t.Progress(run)
		}
	}
	if held := run.Pending; held != nil && next >= len(steps) && len(steps) > 0 {
		// A confirmation asked while working toward the result, after the last step.
		run.Pending = nil
		r.wstep, r.task.Target = len(steps), steps[len(steps)-1].target()
		if out, done := l.runApproved(ctx, r, held); done {
			return l.end(ctx, run, out)
		}
	}
	return l.end(ctx, run, l.replayDone(ctx, r, wf, steps, params))
}

// stepSettle is how long a replay waits before it looks a second time at a step whose check
// failed: a slow page, a window still opening.
const stepSettle = 2 * time.Second

// entryCheck is the history entry of a step that ran but whose check failed.
const entryCheck = "check"

// stepAttempts is how many times a replay checks one step, with the model acting in between,
// before it asks.
const stepAttempts = 3

// checkStep verifies what step i should have caused (its expectation), by a fresh look, never by
// the action's own success. A check that fails is made once more after stepSettle (a slow page).
// Still failing, the model looks with the step's goal and what the check found, and either does
// what is still needed (the check is then made again), says the result is there in another form
// (the run carries on), or asks. A risky step is never left to the model: redoing a send or a
// payment that did go through is worse than a question. After stepAttempts checks the run asks,
// saying the step ran, what it expected and what it found, and records that the step waits on
// its check so a resumed run doesn't do it again. A step with no expectation is done when its
// action succeeded.
func (l *Loop) checkStep(ctx context.Context, r *round, st WorkflowStep, i int) (Outcome, bool) {
	c := st.Expect
	if c != nil && !r.pick.Vision && c.Screen != "" {
		// A sentence about the screen needs a model that sees it. Without one the rest of the
		// expectation is still checked; the workflow's own result check stays strict.
		b := *c
		b.Screen = ""
		c = &b
	}
	if c == nil || c.empty() {
		return Outcome{}, false
	}
	if c.Screen != "" {
		b := *c
		b.Screen = "After the last action, is this true: " + oneLine(c.Screen) + "?"
		c = &b
	}
	waiting := func(why string) {
		r.run.History = append(r.run.History, Entry{Round: r.run.Rounds, Kind: entryCheck, Intent: st.Intent,
			Result: "unverified: " + why, WStep: i + 1, Healed: r.heal})
	}
	for attempt := 1; ; attempt++ {
		res := l.Verify(ctx, r.run.ID, st.target(), r.pick, c)
		if !res.OK {
			l.sleep(ctx, stepSettle)
			res = l.Verify(ctx, r.run.ID, st.target(), r.pick, c)
		}
		if res.OK {
			return Outcome{}, false
		}
		why := strings.TrimSuffix(res.Why, ".")
		if st.Risk != "" || attempt >= stepAttempts {
			waiting(why)
			return Outcome{State: StateAsk, Reason: ReasonUnclear, Ask: fmt.Sprintf(
				"I did step %d (%s), but I couldn't confirm it worked: %s. Look at the computer: if it's as it "+
					"should be, tap Continue. If not, put it right yourself and tap Continue, or tell me what to do.",
				i+1, oneLine(st.Intent), why)}, true
		}
		r.recovering, r.said = why, false
		out, done := l.replayRound(ctx, r, st, i)
		r.recovering = ""
		if done {
			waiting(why) // a question or a confirmation: the step still waits on its check
			return out, true
		}
		if r.said {
			waiting(why + "; carried on: the model found the step's result there in another form")
			return Outcome{}, false
		}
	}
}

// vouches reports whether the user's answer to a failed check says it is fine as it is, rather
// than telling the run what to do.
func vouches(answer string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(answer), ".!")) {
	case "continue", "yes", "ok", "okay", "done", "it's fine", "its fine", "fine", "looks good", "carry on", "go on":
		return true
	}
	return false
}

// strictSites are the sites a Strict workflow may open: its steps' and prerequisites'. Nil for a
// Creative workflow, or one that names no site (a desktop task).
func strictSites(wf *Workflow) []string {
	if wf.Skill.Guardrails == GuardCreative {
		return nil
	}
	var out []string
	add := func(host string) {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	hostOf := func(raw string) string {
		if u, err := url.Parse(raw); err == nil {
			return u.Host
		}
		return ""
	}
	for _, st := range wf.Steps {
		add(st.Site)
		add(hostOf(st.Action.URL))
	}
	for _, p := range wf.Skill.Prerequisites {
		add(p.Site)
		add(hostOf(p.URL))
	}
	return out
}

// offSite is the host a navigation would open outside sites ("" when it stays inside, or sites
// is nil). A subdomain of a listed site, or the site a listed subdomain belongs to, is inside:
// a bank's sign-in often lives on another host of the same name.
func offSite(sites []string, raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || len(sites) == 0 {
		return ""
	}
	host := strings.ToLower(u.Host)
	for _, s := range sites {
		if host == s || strings.HasSuffix(host, "."+s) || strings.HasSuffix(s, "."+host) {
			return ""
		}
	}
	return host
}

// awaitingCheck reports whether step wstep ran and is waiting on its failed check.
func awaitingCheck(h []Entry, wstep int) bool {
	n := len(h)
	return n > 0 && h[n-1].Kind == entryCheck && h[n-1].WStep == wstep && !h[n-1].StepDone
}

// replayStep does one step: directly when it needs no element or its element is found, else in
// one model round. done reports an outcome that ends the call (a confirmation, a question, a
// block, a stop); otherwise the step is done.
func (l *Loop) replayStep(ctx context.Context, r *round, st WorkflowStep, i int) (Outcome, bool) {
	a := st.Action
	a.Approval, a.CaptureID = "", ""
	if strings.Contains(a.Text, MaskedValue) {
		// The recorder never kept the value. The user types it themselves and says carry on.
		if r.task.Answer != "" {
			r.run.History = append(r.run.History, Entry{Round: r.run.Rounds, Kind: a.Kind, Intent: a.Intent,
				Result: "done by the user", WStep: i + 1})
			return Outcome{}, false
		}
		return Outcome{State: StateAsk, Reason: ReasonLoginNeeded, Ask: fmt.Sprintf(
			"Step %d (%s) needs something I don't keep, like a password or a card number. "+
				"Enter it on the computer yourself, then tap Continue.", i+1, oneLine(st.Intent))}, true
	}
	var obs Observation
	direct := true
	switch {
	case st.Element != nil && takesRef(a.Kind):
		o, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: r.task.Target, StartURL: r.task.StartURL, Snapshot: true})
		if err != nil {
			return Outcome{State: StateFailed, Summary: "I couldn't see the computer: " + err.Error()}, true
		}
		obs = o
		if el, ok := findElement(obs.Elements, st.Element); ok {
			a = onElement(a, el)
		} else {
			direct = false
		}
	case needsTarget(a.Kind):
		direct = false // a ref or coordinates from the recording mean nothing now
	}
	if direct {
		if out, done := l.decideAndAct(ctx, r, obs, []computeruse.Action{a}, nil); done {
			return out, true
		}
		if r.feedback == "" {
			return Outcome{}, false
		}
	}
	return l.replayRound(ctx, r, st, i)
}

// replayRound is a step's model round: the step's intent as the task, the recorded element as a
// hint, and the failure of the direct try as feedback. The model may do the step, say it is
// already done, or ask; anything else asks the user.
func (l *Loop) replayRound(ctx context.Context, r *round, st WorkflowStep, i int) (Outcome, bool) {
	r.run.Rounds++
	r.task.Goal = fmt.Sprintf("%s (step %d of a saved task: do only this step, then say done)", oneLine(st.Intent), i+1)
	if r.recovering != "" {
		// The step ran; its check failed. The model judges by what is on the screen now.
		r.task.Goal = fmt.Sprintf("%s (step %d of a saved task). I already did this step, but its check failed: %s. "+
			"Look at the screen. If what the step is for is already there, say done and do nothing. "+
			"Otherwise do only what is still needed for this step, then say done.", oneLine(st.Intent), i+1, r.recovering)
		if g := oneLine(st.Goal); g != "" {
			r.task.Goal += " The step is for: " + g + "."
		}
		if r.wfGoal != "" {
			r.task.Goal += " The whole task is done when: " + oneLine(r.wfGoal) + "."
		}
	}
	r.task.Success = nil
	if e := st.Element; e != nil && (e.Name != "" || e.Role != "") {
		r.hint = fmt.Sprintf("this step was recorded on %s %q; it may have moved or been renamed.", firstNonEmpty(e.Role, "element"), e.Name)
		if st.Action.Kind == "type" || st.Action.Kind == "set_value" || st.Action.Kind == "select" {
			r.hint += fmt.Sprintf(" The value is %q.", st.Action.Text)
		}
	}
	if w := st.Window; w != nil && w.Point != nil {
		r.hint = strings.TrimSpace(r.hint + fmt.Sprintf(" It was a click %d,%d from the top-left of the window, which was %d×%d then.",
			w.Point.X, w.Point.Y, w.Point.W, w.Point.H))
	}
	defer func() { r.hint, r.feedback = "", "" }()
	unsure := func(why string) (Outcome, bool) {
		return Outcome{State: StateAsk, Reason: ReasonUnclear, Ask: fmt.Sprintf(
			"I couldn't do step %d (%s): %s. Do it on the computer and tap Continue, or tell me what to do.",
			i+1, oneLine(st.Intent), why)}, true
	}
	obs, stop := l.look(ctx, r)
	if stop != nil {
		return *stop, true
	}
	reply, err := l.ask(ctx, r, obs)
	if err != nil {
		return Outcome{State: StateFailed, Summary: err.Error()}, true
	}
	r.feedback = ""
	switch {
	case reply.Done:
		r.said = true
		return Outcome{}, false
	case reply.Ask != "":
		if reply.Reason == ReasonPageInstructions {
			return Outcome{State: StateAsk, Ask: injectionAsk, Reason: ReasonPageInstructions}, true
		}
		return Outcome{State: StateAsk, Ask: reply.Ask, Reason: reply.Reason}, true
	}
	var actions []computeruse.Action
	for _, a := range reply.Actions {
		if a.Kind != "screenshot" {
			actions = append(actions, l.toImage(r, a))
		}
	}
	if len(actions) == 0 {
		return unsure("I couldn't find where to do it")
	}
	for _, a := range actions {
		if host := offSite(r.sites, a.URL); a.Kind == "navigate" && host != "" {
			// Strict: only the sites the workflow names. Leaving them is the user's call.
			return Outcome{State: StateAsk, Reason: ReasonUnclear, Ask: fmt.Sprintf(
				"To do step %d (%s) I would have to open %s, and this workflow only uses %s. Do the step on the "+
					"computer and tap Continue, or set the workflow to Creative to let me use other sites.",
				i+1, oneLine(st.Intent), host, joinAnd(r.sites))}, true
		}
	}
	if rec := st.Element; rec != nil {
		for _, a := range actions {
			if el := elementOf(obs, a.Ref); el != nil {
				if !sameElement(*rec, *el) {
					r.heal = &RecordedElement{Role: el.Role, Name: el.Name, Source: rec.Source}
				}
				break
			}
		}
	}
	if out, done := l.decideAndAct(ctx, r, obs, actions, reply.Checkout); done {
		return out, true
	}
	if r.feedback != "" {
		return unsure(strings.TrimPrefix(r.feedback, "these actions failed, so the rest of the batch didn't run: "))
	}
	return Outcome{}, false
}

// endAttempts is how many model rounds a replay spends working toward a result its steps didn't
// produce before it asks.
const endAttempts = 3

// replayDone checks the workflow's result: its success check and the goal's end state, for this
// run's values. When every step ran but the result isn't there, the model works toward it (the
// recorded steps are a route, the end state is the point), and the check is made again. Still
// missing, the run asks; only the user's word that it is fine ends it done then, and the summary
// says the run couldn't check it. Then the healed steps are stored.
func (l *Loop) replayDone(ctx context.Context, r *round, wf *Workflow, steps []WorkflowStep, params map[string]string) Outcome {
	bind := func(s string) string { return wf.BindText(s, params) }
	check := wf.Success.with(bind)
	end := oneLine(bind(wf.Skill.Goal.EndState))
	// The end state is asked of the screen only when nothing observable checks the result: a
	// download, a file, an address or a text is proof, and "the statement is in Downloads" is
	// not something a screenshot shows.
	if end != "" && !check.concrete() && check.Screen == "" && r.pick.Vision {
		check.Screen = "Is this true now: " + end + "?"
	}
	if check.empty() {
		// C4: never done unchecked. A workflow saved without a check is checked on screen.
		check = SuccessCheck{Screen: fmt.Sprintf("Is the task %q finished (the last step was: %s)?", wf.Name, oneLine(steps[len(steps)-1].Intent))}
	}
	last := steps[len(steps)-1]
	tgt := last.target()
	res := l.Verify(ctx, r.run.ID, tgt, r.pick, &check)
	vouched := false
	for attempt := 1; !res.OK; attempt++ {
		why := strings.TrimSuffix(res.Why, ".")
		if vouches(r.task.Answer) {
			vouched = true
			break
		}
		if attempt > endAttempts {
			return Outcome{State: StateAsk, Reason: ReasonUnclear, Ask: fmt.Sprintf(
				"I ran every step of %q, but couldn't confirm it worked: %s. Look at the computer: if the result is "+
					"there, tap Continue. If not, finish it yourself and tap Continue, or tell me what to do.", wf.Name, why)}
		}
		// The model works toward the result, as it puts a failed step right.
		finish := WorkflowStep{Intent: "Finish " + strconvQuote(wf.Name), Target: last.Target, Site: last.Site, App: last.App,
			Goal: firstNonEmpty(end, oneLine(bind(wf.Skill.Goal.Reason)))}
		r.wstep, r.task.Target, r.heal, r.hint = len(steps), tgt, nil, ""
		r.recovering, r.said = "every step ran, but the result isn't there: "+why, false
		out, done := l.replayRound(ctx, r, finish, len(steps)-1)
		r.recovering = ""
		r.task.Answer = "" // what the user said was for the first of these rounds
		if done {
			return out
		}
		res = l.Verify(ctx, r.run.ID, tgt, r.pick, &check)
		if r.said && !res.OK {
			attempt = endAttempts // the model has nothing more to do: ask
		}
	}
	did := fmt.Sprintf("Ran %q.", wf.Name)
	heals := healsOf(r.run.History, wf.Steps)
	if len(heals) > 0 {
		var moved []string
		for _, h := range heals {
			moved = append(moved, firstNonEmpty(h.Was.Name, h.Was.Role, wf.Steps[h.Step].Intent))
		}
		note := fmt.Sprintf(" %s moved; I updated the step.", joinAnd(moved))
		if len(heals) > 1 {
			note = fmt.Sprintf(" %s moved; I updated the steps.", joinAnd(moved))
		}
		if l.Workflows != nil {
			if err := l.Workflows.Heal(ctx, wf.ID, wf.Version, heals); err != nil {
				note = fmt.Sprintf(" %s moved, but I couldn't update the workflow.", joinAnd(moved))
				heals = nil
			}
		}
		did += note
	}
	if vouched {
		return Outcome{State: StateDone, Summary: did + " You confirmed the result; I couldn't check it myself: " +
			strings.TrimSuffix(res.Why, ".") + ".", Verify: &check, Healed: heals}
	}
	return Outcome{State: StateDone, Summary: did + " Checked: " + res.Checked + ".", Checked: res.Checked,
		Verify: &check, Healed: heals}
}

func strconvQuote(s string) string { return fmt.Sprintf("%q", s) }

// newApp reports a desktop step in another app than the step before it (or the first step).
func newApp(steps []WorkflowStep, i int) bool {
	st := steps[i]
	return st.Target == "desktop" && st.App != "" && (i == 0 || steps[i-1].Target != "desktop" || steps[i-1].App != st.App)
}

// stepsDone is how many of a replay's steps its history finished (they finish in order).
func stepsDone(h []Entry) int {
	n := 0
	for _, e := range h {
		if e.StepDone && e.WStep > n {
			n = e.WStep
		}
	}
	return n
}

// markDone records that step wstep finished, on its last action or, when it ran none (the model
// said it was done already), as an entry of its own.
func markDone(run *Run, st WorkflowStep, wstep int, heal *RecordedElement) {
	if n := len(run.History); n > 0 && run.History[n-1].WStep == wstep {
		run.History[n-1].StepDone, run.History[n-1].Healed = true, heal
		return
	}
	run.History = append(run.History, Entry{Round: run.Rounds, Kind: st.Action.Kind, Intent: st.Intent,
		Result: "ok", WStep: wstep, StepDone: true, Healed: heal})
}

// heldHeal is the element a held step was healed to, kept with its held actions.
func heldHeal(h []Entry, wstep int) *RecordedElement {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].WStep == wstep && h[i].Healed != nil {
			return h[i].Healed
		}
	}
	return nil
}

// healsOf are the finished steps that were healed, in step order. A step whose element names a
// param ("Statement {month}") is not: the element healed to is this run's value's.
func healsOf(h []Entry, steps []WorkflowStep) []Heal {
	var out []Heal
	for _, e := range h {
		i := e.WStep - 1
		if !e.StepDone || e.Healed == nil || i < 0 || i >= len(steps) || steps[i].Element == nil ||
			placeholderRe.MatchString(steps[i].Element.Name) {
			continue
		}
		out = append(out, Heal{Step: i, Was: *steps[i].Element, Element: *e.Healed})
	}
	return out
}

// takesRef reports an action that can run on a snapshot element (a type runs as set_value).
func takesRef(kind string) bool {
	switch kind {
	case "press", "set_value", "select", "click", "type":
		return true
	}
	return false
}

// needsTarget reports an action that can't run without a fresh element or screenshot.
func needsTarget(kind string) bool {
	switch kind {
	case "press", "set_value", "select", "click", "double_click", "triple_click", "move", "drag", "zoom":
		return true
	}
	return false
}

// onElement points a recorded action at a found element.
func onElement(a computeruse.Action, el Element) computeruse.Action {
	switch a.Kind {
	case "click":
		a.Kind = "press"
	case "type":
		a.Kind = "set_value"
	}
	a.Ref, a.X, a.Y = el.Ref, nil, nil
	return a
}

// findElement is the snapshot element a recorded element is: the one element with its role and
// name (case and spacing aside). None, or more than one, is not found.
func findElement(els []Element, rec *RecordedElement) (Element, bool) {
	if strings.TrimSpace(rec.Name) == "" {
		return Element{}, false
	}
	var hit []Element
	for _, e := range els {
		if sameElement(*rec, e) {
			hit = append(hit, e)
		}
	}
	if len(hit) != 1 {
		return Element{}, false
	}
	return hit[0], true
}

func sameElement(rec RecordedElement, e Element) bool {
	return normText(rec.Name) == normText(e.Name) && (rec.Role == "" || strings.EqualFold(rec.Role, e.Role))
}

func elementOf(o Observation, ref string) *Element {
	if ref == "" {
		return nil
	}
	for _, e := range o.Elements {
		if e.Ref == ref {
			return &e
		}
	}
	return nil
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
