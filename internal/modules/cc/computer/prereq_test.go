package computer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

func withPrereqs(ps ...Prerequisite) *Workflow {
	wf := statementWorkflow()
	wf.Skill.Prerequisites = ps
	return wf
}

func (e *tenv) resume(wf *Workflow, runID, answer string) Outcome {
	tk := task()
	tk.Goal, tk.Target, tk.RunID, tk.Answer = "", computeruse.Target{}, runID, answer
	return e.loop.Replay(context.Background(), tk, wf, map[string]string{"month": "October 2026"})
}

// Signed in is checked by a look: a page with a way to sign out is, and the run goes on.
func TestPrerequisiteSignedIn(t *testing.T) {
	e := newEnv(t, bankPage(download))
	out := e.replay(withPrereqs(Prerequisite{Kind: PrereqSignedIn, Site: "bank.example.com"}), map[string]string{"month": "October 2026"})
	if out.State != StateDone || len(e.llm.reqs) != 0 {
		t.Fatalf("outcome %+v", out)
	}
}

// Signed out, the run stops before step 1 and asks; it types nothing. On the user's word it
// carries on, and doesn't ask about that prerequisite again.
func TestPrerequisiteSignedOutAsksAndNeverTypes(t *testing.T) {
	login := Observation{URL: "https://bank.example.com/login", Title: "Sign in", Gen: 1, Elements: []Element{
		{Ref: "e1", Role: "textbox", Name: "Username"},
		{Ref: "e2", Role: "textbox", Name: "Password", Type: "password", Secure: true},
		{Ref: "e3", Role: "button", Name: "Sign in"},
	}}
	e := newEnv(t, login)
	wf := withPrereqs(Prerequisite{Kind: PrereqSignedIn, Site: "bank.example.com"})
	out := e.replay(wf, map[string]string{"month": "October 2026"})
	if out.State != StateAsk || out.Reason != ReasonLoginNeeded || !strings.Contains(out.Ask, "You're signed out of bank.example.com") {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.exec.batches) != 0 {
		t.Fatalf("acted before the prerequisite was met: %+v", e.exec.batches)
	}
	e.exec.pages = []Observation{bankPage(download)}
	if out = e.resume(wf, out.RunID, "continue"); out.State != StateDone {
		t.Fatalf("after signing in: %+v", out)
	}
}

// A page that shows neither a sign-in nor a sign-out says nothing either way: the run goes on.
// So does one on another site: its own steps get there.
func TestPrerequisiteSignedInWithoutASign(t *testing.T) {
	plain := Observation{URL: "https://bank.example.com/statements", Title: "Statements", Gen: 1, Elements: []Element{download}}
	if signedOut(plain) || signedOut(bankPage(download)) {
		t.Fatal("a page with no sign-in control read as signed out")
	}
	other := Observation{URL: "https://news.example.org/", Gen: 1, Elements: []Element{{Role: "button", Name: "Log in"}, download}}
	e := newEnv(t, other)
	out := e.replay(withPrereqs(Prerequisite{Kind: PrereqSignedIn, Site: "bank.example.com"}), map[string]string{"month": "October 2026"})
	if out.State == StateAsk && out.Reason == ReasonLoginNeeded {
		t.Fatalf("asked to sign in to a site the window isn't on: %+v", out)
	}
}

// An app is opened by looking at it; one that can't be opened stops the run before step 1.
func TestPrerequisiteAppOpen(t *testing.T) {
	e := newEnv(t, bankPage(download))
	out := e.replay(withPrereqs(Prerequisite{Kind: PrereqAppOpen, App: "LibreOffice Calc"}), map[string]string{"month": "October 2026"})
	if out.State != StateDone {
		t.Fatalf("outcome %+v", out)
	}
	opened := false
	for _, o := range e.exec.observes {
		opened = opened || (o.Target.Kind == "desktop" && o.Target.App == "LibreOffice Calc")
	}
	if !opened {
		t.Fatalf("the app was never looked at: %+v", e.exec.observes)
	}

	e = newEnv(t, bankPage(download))
	e.exec.observeErr = func(o Observe) error {
		if o.Target.App == "LibreOffice Calc" {
			return errors.New("no app called LibreOffice Calc")
		}
		return nil
	}
	out = e.replay(withPrereqs(Prerequisite{Kind: PrereqAppOpen, App: "LibreOffice Calc"}), map[string]string{"month": "October 2026"})
	if out.State != StateAsk || !strings.Contains(out.Ask, "LibreOffice Calc needs to be open, and I couldn't open it: no app called LibreOffice Calc") || len(e.exec.batches) != 0 {
		t.Fatalf("outcome %+v, %d batches", out, len(e.exec.batches))
	}
}

// A file is open when its app's window is titled with it, for the run's values; otherwise the
// run asks for it by name.
func TestPrerequisiteFileOpen(t *testing.T) {
	p := Prerequisite{Kind: PrereqFileOpen, File: "budget-{month}.xlsx", App: "LibreOffice Calc"}
	sheet := Observation{Title: "budget-October 2026.xlsx — LibreOffice Calc", App: "LibreOffice Calc", Gen: 1}
	e := newEnv(t, bankPage(download), sheet, bankPage(download))
	if out := e.replay(withPrereqs(p), map[string]string{"month": "October 2026"}); out.State != StateDone {
		t.Fatalf("with the file open: %+v", out)
	}
	e = newEnv(t, bankPage(download))
	out := e.replay(withPrereqs(p), map[string]string{"month": "October 2026"})
	if out.State != StateAsk || !strings.Contains(out.Ask, "budget-October 2026.xlsx needs to be open in LibreOffice Calc") {
		t.Fatalf("without it: %+v", out)
	}
}

// A page that isn't open is opened, as a step's navigation is.
func TestPrerequisitePageOpen(t *testing.T) {
	e := newEnv(t, bankPage(download))
	out := e.replay(withPrereqs(Prerequisite{Kind: PrereqPageOpen, URL: "bank.example.com/accounts"}), map[string]string{"month": "October 2026"})
	if out.State != StateDone {
		t.Fatalf("outcome %+v", out)
	}
	if a := e.exec.batches[0].Actions[0]; a.Kind != "navigate" || a.URL != "https://bank.example.com/accounts" {
		t.Fatalf("first action %+v, want the prerequisite's page opened", a)
	}
}

// A file that isn't open is found by name and opened by the computer, and then looked for: one
// match is opened, several are asked about, a program is left to the user. The run never sends a path.
func TestPrerequisiteFileIsOpenedByName(t *testing.T) {
	p := Prerequisite{Kind: PrereqFileOpen, File: "budget-{month}.xlsx", App: "LibreOffice Calc"}
	sheet := Observation{Title: "budget-October 2026.xlsx — LibreOffice Calc", App: "LibreOffice Calc", Gen: 1}

	// Opening the window, the look that doesn't find it, then (after it is opened) the sheet.
	e := newEnv(t, bankPage(download), bankPage(download), sheet, bankPage(download))
	e.sleeps()
	e.exec.openFile = func(name string) (OpenResult, error) {
		return OpenResult{Opened: name, Folder: "Documents"}, nil
	}
	out := e.replay(withPrereqs(p), map[string]string{"month": "October 2026"})
	if out.State != StateDone || len(e.exec.opened) != 1 || e.exec.opened[0] != "budget-October 2026.xlsx" {
		t.Fatalf("one match: %+v, asked to open %v", out, e.exec.opened)
	}

	for name, tc := range map[string]struct {
		res  OpenResult
		want string
	}{
		"several": {OpenResult{Candidates: []string{"budget.xlsx in Documents", "budget.xlsx in Downloads"}},
			"more than one file has that name: budget.xlsx in Documents; budget.xlsx in Downloads"},
		"a program": {OpenResult{NeedsConfirm: true}, "it is a program or script: I only run one when you say so"},
		"none":      {OpenResult{}, "budget-October 2026.xlsx needs to be open in LibreOffice Calc. Open it and tap Continue."},
		"not shown": {OpenResult{Opened: "budget-October 2026.xlsx", Folder: "Documents"}, "I opened it from Documents, but I can't see it there"},
	} {
		e := newEnv(t, bankPage(download))
		e.sleeps()
		e.exec.openFile = func(string) (OpenResult, error) { return tc.res, nil }
		out := e.replay(withPrereqs(p), map[string]string{"month": "October 2026"})
		if out.State != StateAsk || !strings.Contains(out.Ask, tc.want) || len(e.exec.batches) != 0 {
			t.Errorf("%s: %+v", name, out)
		}
	}
}
