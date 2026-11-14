package computer

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Prerequisites (docs/agent/WORKFLOW-SKILLS.md, verified-replay Q1): before step 1 a run makes
// sure of what was true when the task was recorded. Each one is checked by a look, never
// assumed, and an unmet one achieved where the run may: an app is opened, a page is opened. It
// never signs in: signed out, it asks the user to and carries on when they say so. One it can't
// achieve stops the run before step 1 with a question.

// Opener is an Executor that can open one of the user's files by its name on the run's computer.
// It never takes a path: the computer finds the file itself.
type Opener interface {
	OpenFile(ctx context.Context, runID, name string) (OpenResult, error)
}

// OpenResult is what came of opening a file by name. At most one of these is set; none means
// no file has that name.
type OpenResult struct {
	Opened       string   // the file opened, and
	Folder       string   // where it was
	Candidates   []string // several files have the name: none was opened
	NeedsConfirm bool     // it is a program or script: the computer asks its user first
}

// entryPrereq is a prerequisite's history entry: Ref names it ("p1"), Result is "ok" or what
// the run asked.
const entryPrereq = "prereq"

var (
	signOutRe = regexp.MustCompile(`(?i)\b(sign|log)[ -]?out\b|\blogoff\b`)
	signInRe  = regexp.MustCompile(`(?i)\b(sign|log)[ -]?in\b|\blogin\b`)
)

// prerequisites checks each of wf's prerequisites in order, skipping those an earlier call of
// this run settled. done reports an outcome that ends the call (a question, a block).
func (l *Loop) prerequisites(ctx context.Context, r *round, wf *Workflow, params map[string]string) (Outcome, bool) {
	for i, p := range wf.Skill.Prerequisites {
		ref := "p" + strconv.Itoa(i+1)
		what := wf.BindText(prereqSentence(p), params)
		settle := func(result string) {
			r.run.History = append(r.run.History, Entry{Round: r.run.Rounds, Kind: entryPrereq, Intent: what, Ref: ref, Result: result})
		}
		switch prereqState(r.run.History, ref) {
		case "ok":
			continue
		case "asked":
			if vouches(r.task.Answer) {
				settle("ok") // the user saw to it
				r.task.Answer = ""
				continue
			}
		}
		out, met, done := l.prerequisite(ctx, r, wf, p, params)
		if done {
			return out, true
		}
		if met {
			settle("ok")
			continue
		}
		settle("asked: " + out.Ask)
		return out, true
	}
	return Outcome{}, false
}

// prereqState is "ok" or "asked" by the last entry of prerequisite ref, "" when it has none.
func prereqState(h []Entry, ref string) string {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Kind == entryPrereq && h[i].Ref == ref {
			state, _, _ := strings.Cut(h[i].Result, ":")
			return state
		}
	}
	return ""
}

// prerequisite checks one prerequisite and achieves it when the run may. met: it holds. Not
// met: out is the question for the user. done: out ends the call as it is (an action was held
// or blocked).
func (l *Loop) prerequisite(ctx context.Context, r *round, wf *Workflow, p Prerequisite, params map[string]string) (out Outcome, met, done bool) {
	ask := func(reason, format string, a ...any) (Outcome, bool, bool) {
		return Outcome{State: StateAsk, Reason: reason, Ask: fmt.Sprintf(format, a...)}, false, false
	}
	bind := func(s string) string { return wf.BindText(s, params) }
	browser := r.task.Target.Kind == "browser"
	switch p.Kind {
	case PrereqAppOpen:
		// Looking at an app opens it from the agent's catalogue and gives it the focus.
		if _, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: computeruse.Target{Kind: "desktop", App: p.App}, Snapshot: true}); err != nil {
			return ask(ReasonUnclear, "Before I start, %s needs to be open, and I couldn't open it: %s. Open it and tap Continue.",
				p.App, strings.TrimSuffix(err.Error(), "."))
		}
		return Outcome{}, true, false

	case PrereqFileOpen:
		file := bind(p.File)
		shown := func() bool {
			if p.App == "" {
				return false
			}
			obs, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: computeruse.Target{Kind: "desktop", App: p.App}, Snapshot: true})
			return err == nil && strings.Contains(strings.ToLower(obs.Title), strings.ToLower(file))
		}
		if shown() {
			return Outcome{}, true, false
		}
		in := ""
		if p.App != "" {
			in = " in " + p.App
		}
		// Not open: the computer finds it by name in the user's folders and opens it, when
		// exactly one file has that name and it is a document (Q1; the agent's desktop_open).
		if op, ok := l.Exec.(Opener); ok {
			switch res, err := op.OpenFile(ctx, r.run.ID, file); {
			case err != nil:
			case res.Opened != "":
				if p.App == "" {
					return Outcome{}, true, false // no app to look at: opening it is what can be done
				}
				l.sleep(ctx, stepSettle)
				if shown() {
					return Outcome{}, true, false
				}
				return ask(ReasonUnclear, "Before I start, %s needs to be open%s. I opened it from %s, but I can't see it there. "+
					"Check it's open and tap Continue.", file, in, firstNonEmpty(res.Folder, "your files"))
			case len(res.Candidates) > 0:
				return ask(ReasonUnclear, "Before I start, %s needs to be open%s, and more than one file has that name: %s. "+
					"Open the right one and tap Continue.", file, in, strings.Join(res.Candidates, "; "))
			case res.NeedsConfirm:
				return ask(ReasonUnclear, "Before I start, %s needs to be open, and it is a program or script: I only run one when you say so. "+
					"Open it yourself and tap Continue.", file)
			}
		}
		return ask(ReasonUnclear, "Before I start, %s needs to be open%s. Open it and tap Continue.", file, in)

	case PrereqPageOpen:
		if !browser {
			return Outcome{}, true, false
		}
		want := bind(p.URL)
		obs, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: r.task.Target, StartURL: r.task.StartURL, Snapshot: true})
		if err == nil && strings.Contains(strings.ToLower(obs.URL), strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(want, "https://"), "http://"))) {
			return Outcome{}, true, false
		}
		to := want
		if !strings.Contains(to, "://") {
			to = "https://" + to
		}
		if o, stop := l.decideAndAct(ctx, r, obs, []computeruse.Action{{Kind: "navigate", URL: to, Intent: "Open " + want}}, nil); stop {
			if r.run.Pending != nil {
				// A held action before step 1 would resume as step 1's: the user opens the page.
				r.run.Pending = nil
				return ask(ReasonUnclear, "Before I start, %s needs to be open. Open it in the window I opened and tap Continue.", want)
			}
			return o, false, true
		}
		if r.feedback != "" {
			why := r.feedback
			r.feedback = ""
			return ask(ReasonUnclear, "Before I start, %s needs to be open, and I couldn't open it: %s. Open it and tap Continue.", want, oneLine(why))
		}
		return Outcome{}, true, false

	case PrereqSignedIn:
		if !browser {
			return Outcome{}, true, false
		}
		obs, err := l.Exec.Observe(ctx, Observe{RunID: r.run.ID, Target: r.task.Target, StartURL: r.task.StartURL, Snapshot: true})
		if err != nil {
			return Outcome{}, true, false // the first step will say what is wrong with the window
		}
		if u, perr := url.Parse(obs.URL); perr != nil || offSite([]string{strings.ToLower(p.Site)}, "https://"+u.Host) != "" {
			return Outcome{}, true, false // not on that site yet: its own steps get there
		}
		if signedOut(obs) {
			return ask(ReasonLoginNeeded, "You're signed out of %s. Sign in in the window I opened, then tap Continue. I never type passwords.", p.Site)
		}
		return Outcome{}, true, false

	default: // PrereqOther: the model judges it on the screen, when it can see
		text := oneLine(bind(p.Text))
		if text == "" || !r.pick.Vision {
			return Outcome{}, true, false
		}
		if res := l.Verify(ctx, r.run.ID, r.task.Target, r.pick, &SuccessCheck{Screen: "Is this true now: " + text + "?"}); !res.OK {
			return ask(ReasonUnclear, "Before I start: %s. I couldn't see that it is (%s). Make it so and tap Continue.",
				text, strings.TrimSuffix(res.Why, "."))
		}
		return Outcome{}, true, false
	}
}

// signedOut reports a page that shows it wants a sign-in: a password field or a sign-in control,
// and no way to sign out. A page showing neither says nothing either way (an account menu can
// hide both), and the run goes on.
func signedOut(obs Observation) bool {
	wantsIn := false
	for _, e := range obs.Elements {
		if signOutRe.MatchString(e.Name) {
			return false
		}
		if e.Secure || strings.EqualFold(e.Type, "password") || signInRe.MatchString(e.Name) {
			wantsIn = true
		}
	}
	return wantsIn
}

// prereqSentence is a prerequisite as the history and the run's record say it.
func prereqSentence(p Prerequisite) string {
	switch p.Kind {
	case PrereqAppOpen:
		return p.App + " is open"
	case PrereqFileOpen:
		return p.File + " is open"
	case PrereqPageOpen:
		return p.URL + " is open"
	case PrereqSignedIn:
		return "signed in to " + p.Site
	}
	return p.Text
}
