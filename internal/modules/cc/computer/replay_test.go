package computer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// fakeWorkflows records what a replay healed.
type fakeWorkflows struct {
	id      string
	version int
	heals   []Heal
}

func (f *fakeWorkflows) Heal(_ context.Context, id string, version int, heals []Heal) error {
	f.id, f.version, f.heals = id, version, heals
	return nil
}

// statementWorkflow downloads a month's statement: open the statements page for {month}, press
// Download CSV.
func statementWorkflow() *Workflow {
	return &Workflow{ID: "tw_1", Name: "Bank statement", Slug: "bank_statement", Version: 3,
		Triggers: []string{"download my bank statement"},
		Params:   []WorkflowParam{{Name: "month", Kind: "month", Example: "September 2026"}},
		Steps: []WorkflowStep{
			{Intent: "Open the statements for {month}", Target: "browser", Site: "bank.example.com",
				Action: computeruse.Action{Kind: "navigate", Intent: "Open the statements for {month}",
					URL: "https://bank.example.com/statements?month={month}"}},
			{Intent: "Download the CSV", Target: "browser", Site: "bank.example.com",
				Action:  computeruse.Action{Kind: "press", Intent: "Download the CSV", Ref: RecordedRef},
				Element: &RecordedElement{Role: "button", Name: "Download CSV", Selector: "#download-csv"}},
		},
		Success: SuccessCheck{URLContains: "/statements"},
	}
}

func bankPage(els ...Element) Observation {
	return Observation{URL: "https://bank.example.com/statements?month=October+2026", Title: "Statements", Gen: 1,
		Elements: append([]Element{
			{Ref: "e1", Role: "link", Name: "Home"},
			{Ref: "e2", Role: "link", Name: "Accounts"},
			{Ref: "e3", Role: "combobox", Name: "Month"},
			{Ref: "e5", Role: "link", Name: "Help"},
			{Ref: "e6", Role: "link", Name: "Sign out"},
		}, els...)}
}

func (e *tenv) replay(wf *Workflow, params map[string]string) Outcome {
	t := task()
	t.Goal, t.Target = "", computeruse.Target{}
	return e.loop.Replay(context.Background(), t, wf, params)
}

func TestReplayTargetFirstNoModel(t *testing.T) {
	e := newEnv(t, bankPage(Element{Ref: "e4", Role: "button", Name: "Download  csv"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	out := e.replay(statementWorkflow(), map[string]string{"month": "October 2026"})
	if out.State != StateDone || out.Summary != `Ran "Bank statement". Checked: the address contains "/statements".` || out.Rounds != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.llm.reqs) != 0 {
		t.Fatalf("the model was called %d times", len(e.llm.reqs))
	}
	if len(e.exec.batches) != 2 {
		t.Fatalf("batches %+v", e.exec.batches)
	}
	if a := e.exec.batches[0].Actions[0]; a.Kind != "navigate" || a.URL != "https://bank.example.com/statements?month=October+2026" {
		t.Fatalf("navigate %+v", a)
	}
	if a := e.exec.batches[1].Actions[0]; a.Kind != "press" || a.Ref != "e4" || a.Intent != "Download the CSV" {
		t.Fatalf("press %+v", a)
	}
	if b := e.exec.batches[1]; b.Target.Kind != "browser" || b.Target.Site != "bank.example.com" {
		t.Fatalf("target %+v", b.Target)
	}
	if wfs.heals != nil || out.Healed != nil {
		t.Fatalf("healed %+v", wfs.heals)
	}
	r := e.storedRun(out.RunID)
	if r.State != RunDone || len(r.History) != 2 || !r.History[1].StepDone || r.History[1].WStep != 2 || r.Goal != `Run the workflow "Bank statement"` {
		t.Fatalf("stored run %+v", r)
	}
	// Every action is still decided and audited.
	if ev := e.events(); len(ev) != 2 || ev[1].Detail["intent"] != "Download the CSV" {
		t.Fatalf("audit %+v", ev)
	}
}

func TestReplayHealsMovedButton(t *testing.T) {
	// The button is gone; a link with the same name sits elsewhere on the page.
	e := newEnv(t, bankPage(Element{Ref: "e9", Role: "link", Name: "Download CSV"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	e.script(actions(press("e9", "Download the CSV")))
	out := e.replay(statementWorkflow(), map[string]string{"month": "October 2026"})
	if out.State != StateDone || out.Rounds != 1 ||
		out.Summary != `Ran "Bank statement". Download CSV moved; I updated the step. Checked: the address contains "/statements".` {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.llm.reqs) != 1 {
		t.Fatalf("model calls %d", len(e.llm.reqs))
	}
	txt := userText(e.llm.reqs[0])
	for _, want := range []string{"Task: Download the CSV (step 2 of a saved task", `Hint: this step was recorded on button "Download CSV"`, "e9 link"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, txt)
		}
	}
	if a := e.exec.batches[len(e.exec.batches)-1].Actions[0]; a.Ref != "e9" {
		t.Fatalf("pressed %+v", a)
	}
	want := Heal{Step: 1, Was: *statementWorkflow().Steps[1].Element, Element: RecordedElement{Role: "link", Name: "Download CSV"}}
	if wfs.id != "tw_1" || wfs.version != 3 || len(wfs.heals) != 1 || wfs.heals[0] != want {
		t.Fatalf("stored %s v%d %+v", wfs.id, wfs.version, wfs.heals)
	}

	// The next replay finds the healed element directly.
	e2 := newEnv(t, bankPage(Element{Ref: "e9", Role: "link", Name: "Download CSV"}))
	wf := statementWorkflow()
	wf.Steps[1].Element = &wfs.heals[0].Element
	if out := e2.replay(wf, map[string]string{"month": "November 2026"}); out.State != StateDone || len(e2.llm.reqs) != 0 {
		t.Fatalf("second replay %+v, %d model calls", out, len(e2.llm.reqs))
	}
}

func TestReplayHealsMovedField(t *testing.T) {
	// The Memo field was renamed; a type step that recorded it goes to the model, not the focus.
	wf := statementWorkflow()
	wf.Steps = append(wf.Steps, WorkflowStep{Intent: "Write the month", Target: "browser",
		Action:  computeruse.Action{Kind: "type", Intent: "Write the month", Text: "{month}"},
		Element: &RecordedElement{Role: "textbox", Name: "Memo"}})
	e := newEnv(t, bankPage(Element{Ref: "e4", Role: "button", Name: "Download CSV"},
		Element{Ref: "e7", Role: "textbox", Name: "Search"}, Element{Ref: "e8", Role: "textbox", Name: "Memo (optional)"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	typ, _ := json.Marshal(map[string]any{"kind": "type", "ref": "e8", "text": "October 2026", "intent": "Write the month"})
	e.script(actions(string(typ)))
	out := e.replay(wf, map[string]string{"month": "October 2026"})
	if out.State != StateDone || out.Rounds != 1 {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.llm.reqs) != 1 {
		t.Fatalf("model calls %d", len(e.llm.reqs))
	}
	txt := userText(e.llm.reqs[0])
	for _, want := range []string{`Hint: this step was recorded on textbox "Memo"`, `The value is "October 2026"`} {
		if !strings.Contains(txt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, txt)
		}
	}
	for _, b := range e.exec.batches {
		for _, a := range b.Actions {
			if a.Kind == "type" && a.Ref == "" {
				t.Fatalf("typed at the focus: %+v", a)
			}
		}
	}
	if a := e.exec.batches[len(e.exec.batches)-1].Actions[0]; a.Ref != "e8" || a.Text != "October 2026" {
		t.Fatalf("typed %+v", a)
	}
	want := Heal{Step: 2, Was: RecordedElement{Role: "textbox", Name: "Memo"}, Element: RecordedElement{Role: "textbox", Name: "Memo (optional)"}}
	if wfs.id != "tw_1" || wfs.version != 3 || len(wfs.heals) != 1 || wfs.heals[0] != want {
		t.Fatalf("stored %s v%d %+v", wfs.id, wfs.version, wfs.heals)
	}
}

func TestReplayNoHealWhenModelPicksRecordedElement(t *testing.T) {
	// Two buttons named Download CSV: the direct try can't tell which, so the model picks one.
	// It is the recorded element, so nothing moved and nothing is stored.
	e := newEnv(t, bankPage(Element{Ref: "e9", Role: "button", Name: "Download CSV"},
		Element{Ref: "e10", Role: "button", Name: "Download CSV"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	e.script(actions(press("e10", "Download the CSV")))
	out := e.replay(statementWorkflow(), map[string]string{"month": "October 2026"})
	if out.State != StateDone || len(e.llm.reqs) != 1 || out.Healed != nil ||
		out.Summary != `Ran "Bank statement". Checked: the address contains "/statements".` {
		t.Fatalf("outcome %+v, %d model calls", out, len(e.llm.reqs))
	}
	if wfs.heals != nil {
		t.Fatalf("stored a heal of an element that didn't move: %+v", wfs.heals)
	}
}

func TestReplayNotHealedWhenCheckFails(t *testing.T) {
	e := newEnv(t, bankPage(Element{Ref: "e9", Role: "link", Name: "Download CSV"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	e.script(actions(press("e9", "Download the CSV")))
	wf := statementWorkflow()
	wf.Success = SuccessCheck{URLContains: "/downloaded"}
	out := e.replay(wf, map[string]string{"month": "October 2026"})
	if out.State != StateFailed || !strings.Contains(out.Summary, `I ran every step of "Bank statement", but couldn't confirm it worked`) {
		t.Fatalf("outcome %+v", out)
	}
	if wfs.heals != nil {
		t.Fatalf("stored a heal of a run that didn't check out: %+v", wfs.heals)
	}
}

func TestReplayParamSubstitution(t *testing.T) {
	wf := statementWorkflow()
	wf.Params = append(wf.Params, WorkflowParam{Name: "note", Kind: "text"})
	wf.Steps = append(wf.Steps, WorkflowStep{Intent: "Write {note} for {month}", Target: "browser",
		Action:  computeruse.Action{Kind: "type", Text: "{note} ({month}) {unknown}"},
		Element: &RecordedElement{Role: "textbox", Name: "Memo"}})
	wf.Steps[0].Action.URL = "https://bank.example.com/{month}/statements?month={month}"

	steps, err := wf.Bind(map[string]string{"month": "March 2026", "note": "rent & bills"})
	if err != nil {
		t.Fatal(err)
	}
	if u := steps[0].Action.URL; u != "https://bank.example.com/March%202026/statements?month=March+2026" {
		t.Fatalf("url %q", u)
	}
	if s := steps[0]; s.Intent != "Open the statements for March 2026" || s.Action.Intent != "Open the statements for March 2026" {
		t.Fatalf("intent %+v", s)
	}
	if a := steps[2].Action; a.Text != "rent & bills (March 2026) {unknown}" || a.Intent != "Write rent & bills for March 2026" {
		t.Fatalf("type %+v", a)
	}
	// The workflow itself is unchanged.
	if wf.Steps[0].Intent != "Open the statements for {month}" {
		t.Fatalf("bound in place: %+v", wf.Steps[0])
	}
	if _, err := wf.Bind(map[string]string{"month": "March 2026"}); err == nil || err.Error() != `I need the note to run "Bank statement"` {
		t.Fatalf("missing param: %v", err)
	}

	// A replay with the field on the page sets it directly; without a value it doesn't start.
	e := newEnv(t, bankPage(Element{Ref: "e4", Role: "button", Name: "Download CSV"}, Element{Ref: "e7", Role: "textbox", Name: "Memo"}))
	out := e.replay(wf, map[string]string{"month": "March 2026", "note": "rent"})
	if out.State != StateDone || len(e.llm.reqs) != 0 {
		t.Fatalf("outcome %+v", out)
	}
	if a := e.exec.batches[2].Actions[0]; a.Kind != "set_value" || a.Ref != "e7" || a.Text != "rent (March 2026) {unknown}" {
		t.Fatalf("typed %+v", a)
	}
	if out := e.replay(wf, nil); out.State != StateFailed || out.RunID != "" || !strings.Contains(out.Summary, "I need the month and note") {
		t.Fatalf("without params %+v", out)
	}
}

func TestReplayRiskyStepHoldsThenResumes(t *testing.T) {
	e := newEnv(t)
	wf := &Workflow{ID: "tw_2", Name: "Reorder soap", Version: 1,
		Steps: []WorkflowStep{
			{Intent: "Add the soap", Target: "browser", Site: "shop.example.com",
				Action: computeruse.Action{Kind: "press", Intent: "Add the soap", Ref: RecordedRef}, Element: &RecordedElement{Role: "button", Name: "Add to cart"}},
			{Intent: "Place the order", Target: "browser", Site: "shop.example.com",
				Action: computeruse.Action{Kind: "press", Intent: "Place the order", Ref: RecordedRef}, Element: &RecordedElement{Role: "button", Name: "Place order"}},
			{Intent: "Wait", Target: "browser", Action: computeruse.Action{Kind: "wait", Intent: "Wait", MS: 500}},
		},
		Success: SuccessCheck{TextPresent: []string{"Lavender soap"}},
	}
	out := e.replay(wf, nil)
	if out.State != StateNeedsApproval || len(out.Pending) != 1 || out.Pending[0].Action.Ref != "e5" || out.Pending[0].Category != computeruse.CatPay {
		t.Fatalf("outcome %+v", out)
	}
	if len(e.exec.batches) != 1 {
		t.Fatalf("the held step ran: %+v", e.exec.batches)
	}
	tk := task()
	tk.RunID, tk.Approved = out.RunID, []string{out.Pending[0].Digest}
	out = e.loop.Replay(context.Background(), tk, wf, nil)
	if out.State != StateDone || len(e.llm.reqs) != 0 {
		t.Fatalf("resumed %+v", out)
	}
	if len(e.exec.batches) != 3 || e.exec.batches[1].Actions[0].Approval == "" || e.exec.batches[2].Actions[0].Kind != "wait" {
		t.Fatalf("batches %+v", e.exec.batches)
	}
}

func TestReplayAsksWhenUnsure(t *testing.T) {
	t.Run("model asks", func(t *testing.T) {
		e := newEnv(t, bankPage())
		e.script(`{"ask": "There is no Download button. Which one?", "reason": "unclear"}`)
		out := e.replay(statementWorkflow(), map[string]string{"month": "October 2026"})
		if out.State != StateAsk || out.Ask != "There is no Download button. Which one?" {
			t.Fatalf("outcome %+v", out)
		}
		// The answer resumes the step that asked, not the whole workflow.
		e.script(actions(press("e5", "Download the CSV")))
		tk := task()
		tk.RunID, tk.Answer = out.RunID, "It's under Help now"
		out = e.loop.Replay(context.Background(), tk, statementWorkflow(), map[string]string{"month": "October 2026"})
		if out.State != StateDone || len(e.exec.batches) != 2 || !strings.Contains(userText(e.llm.reqs[1]), "It's under Help now") {
			t.Fatalf("resumed %+v %+v", out, e.exec.batches)
		}
	})
	t.Run("masked value", func(t *testing.T) {
		e := newEnv(t, bankPage())
		wf := statementWorkflow()
		wf.Steps[1] = WorkflowStep{Intent: "Type the password", Target: "browser", Action: computeruse.Action{Kind: "type", Intent: "Type the password", Text: MaskedValue}}
		out := e.replay(wf, map[string]string{"month": "October 2026"})
		if out.State != StateAsk || out.Reason != ReasonLoginNeeded || len(e.exec.batches) != 1 {
			t.Fatalf("outcome %+v", out)
		}
		tk := task()
		tk.RunID, tk.Answer = out.RunID, "Done"
		if out = e.loop.Replay(context.Background(), tk, wf, map[string]string{"month": "October 2026"}); out.State != StateDone || len(e.exec.batches) != 1 {
			t.Fatalf("resumed %+v", out)
		}
	})
}

func TestMatchWorkflows(t *testing.T) {
	ws := []Workflow{
		{ID: "a", Name: "Bank statement", Slug: "bank_statement", Triggers: []string{"download my bank statement"}},
		{ID: "b", Name: "Bank transfer", Slug: "bank_transfer"},
		{ID: "c", Name: "Order soap", Slug: "order_soap", Triggers: []string{"reorder the soap"}},
	}
	ids := func(q string) string {
		var out []string
		for _, w := range MatchWorkflows(ws, q) {
			out = append(out, w.ID)
		}
		return strings.Join(out, ",")
	}
	for q, want := range map[string]string{
		"the Bank Statement workflow":            "a",
		"bank_statement":                         "a",
		"Download my bank statement.":            "a",
		"reorder the soap":                       "c",
		"run order soap":                         "c",
		"bank":                                   "a,b",
		"download my bank statement for October": "a",
		"pay the rent":                           "",
		"":                                       "",
	} {
		if got := ids(q); got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
}

func TestReplayDesktopAXElement(t *testing.T) {
	ptr := func(i int) *int { return &i }
	notes := func(els ...Element) Observation {
		return Observation{App: "com.apple.Notes", Title: "Notes", Gen: 1, Elements: els}
	}
	wf := &Workflow{ID: "tw_1", Version: 3, Name: "New note", Steps: []WorkflowStep{
		{Intent: "New note", Target: "desktop", App: "com.apple.Notes",
			Action:  computeruse.Action{Kind: "click", Intent: "New note", X: ptr(310), Y: ptr(62)},
			Element: &RecordedElement{Role: "AXButton", Name: "New Note", AXPath: "AXWindow/AXToolbar/AXButton#new", Source: "ax"}},
	}, Success: SuccessCheck{TextPresent: []string{"Search"}}}

	// The recorded element is on screen: pressed at once, no model.
	e := newEnv(t, notes(Element{Ref: "a1", Role: "AXTextField", Name: "Search"}, Element{Ref: "a2", Role: "AXButton", Name: "New Note"}))
	out := e.replay(wf, nil)
	if out.State != StateDone || out.Rounds != 0 || len(e.llm.reqs) != 0 {
		t.Fatalf("outcome %+v, model calls %d", out, len(e.llm.reqs))
	}
	if a := e.exec.batches[0].Actions[0]; a.Kind != "press" || a.Ref != "a2" || a.X != nil {
		t.Fatalf("pressed %+v", a)
	}
	if b := e.exec.batches[0]; b.Target.Kind != "desktop" || b.Target.App != "com.apple.Notes" {
		t.Fatalf("target %+v", b.Target)
	}

	// It was renamed: the model picks it, and the heal stays an accessibility element.
	e = newEnv(t, notes(Element{Ref: "a1", Role: "AXTextField", Name: "Search"}, Element{Ref: "a2", Role: "AXButton", Name: "Compose"}))
	wfs := &fakeWorkflows{}
	e.loop.Workflows = wfs
	e.script(actions(press("a2", "New note")))
	out = e.replay(wf, nil)
	if out.State != StateDone || out.Rounds != 1 {
		t.Fatalf("outcome %+v", out)
	}
	want := Heal{Step: 0, Was: *wf.Steps[0].Element, Element: RecordedElement{Role: "AXButton", Name: "Compose", Source: "ax"}}
	if len(wfs.heals) != 1 || wfs.heals[0] != want {
		t.Fatalf("stored %+v", wfs.heals)
	}
}

// TestParamValueMonth: a month param whose example is YYYY-MM takes the month as people say it.
func TestParamValueMonth(t *testing.T) {
	iso := WorkflowParam{Name: "month", Kind: "month", Example: "2026-09"}
	now := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		p    WorkflowParam
		v    string
		want string
	}{
		{iso, "August", "2026-08"},
		{iso, " august 2026 ", "2026-08"},
		{iso, "Aug 2025", "2025-08"},
		{iso, "September of 2026", "2026-09"},
		{iso, "October", "2026-10"},
		{iso, "December", "2025-12"}, // the last December up to now
		{iso, "2026-8", "2026-08"},
		{iso, "08/2026", "2026-08"},
		{iso, "last month", "last month"},
		{iso, "2026-13", "2026-13"},
		{WorkflowParam{Name: "month", Kind: "month", Example: "September 2026"}, "August", "August"},
		{WorkflowParam{Name: "note", Kind: "text", Example: "2026-09"}, " August ", "August"},
	} {
		if got := ParamValue(c.p, c.v, now); got != c.want {
			t.Errorf("ParamValue(%+v, %q) = %q, want %q", c.p, c.v, got, c.want)
		}
	}
}

// linkMonthWorkflow picks its month by pressing the month's link (a learnt "Statement {month}").
func linkMonthWorkflow() *Workflow {
	return &Workflow{ID: "tw_2", Name: "Download bank statement", Version: 1,
		Params: []WorkflowParam{{Name: "month", Kind: "month", Example: "2026-09"}},
		Steps: []WorkflowStep{
			{Intent: "select statement period", Target: "browser", Site: "bank.example.com",
				Action:  computeruse.Action{Kind: "press", Intent: "select statement period", Ref: RecordedRef},
				Element: &RecordedElement{Role: "link", Name: "Statement {month}", Selector: `link "Statement {month}"`}},
		},
		Success: SuccessCheck{URLContains: "/statements"},
	}
}

// TestReplayPressesParamElement: the recorded element's name takes the param's value, so the
// replay presses the new month's link directly; one healed is not stored (it is that month's).
func TestReplayPressesParamElement(t *testing.T) {
	steps, err := linkMonthWorkflow().bind(map[string]string{"month": "August"}, time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if el := steps[0].Element; el.Name != "Statement 2026-08" || el.Selector != `link "Statement 2026-08"` {
		t.Fatalf("element %+v", el)
	}
	if wf := linkMonthWorkflow(); wf.Steps[0].Element.Name != "Statement {month}" {
		t.Fatalf("bound in place: %+v", wf.Steps[0].Element)
	}

	e := newEnv(t, bankPage(Element{Ref: "e8", Role: "link", Name: "Statement 2026-09"}, Element{Ref: "e9", Role: "link", Name: "Statement 2026-08"}))
	if out := e.replay(linkMonthWorkflow(), map[string]string{"month": "2026-08"}); out.State != StateDone || len(e.llm.reqs) != 0 {
		t.Fatalf("outcome %+v, %d model calls", out, len(e.llm.reqs))
	}
	if a := e.exec.batches[0].Actions[0]; a.Ref != "e9" {
		t.Fatalf("pressed %+v", a)
	}

	// The month's link is now a button: the model's round presses it, and the step keeps its param.
	e2 := newEnv(t, bankPage(Element{Ref: "e9", Role: "button", Name: "Statement 2026-08"}))
	wfs := &fakeWorkflows{}
	e2.loop.Workflows = wfs
	e2.script(actions(press("e9", "select statement period")))
	if out := e2.replay(linkMonthWorkflow(), map[string]string{"month": "2026-08"}); out.State != StateDone || len(out.Healed) != 0 || len(wfs.heals) != 0 {
		t.Fatalf("outcome %+v, stored %+v", out, wfs.heals)
	}
}

// TestReplayOpensWindowAtFirstStep: the task's window is opened (the executor acts only in a
// window an observe opened) on the first step's address, before the first act, as a computer
// task's is on its start URL: on http too, not https://<site>.
func TestReplayOpensWindowAtFirstStep(t *testing.T) {
	e := newEnv(t, bankPage(Element{Ref: "e4", Role: "button", Name: "Download CSV"}))
	wf := statementWorkflow()
	wf.Steps[0].Action.URL = "http://127.0.0.1:4000/bank/statements?month={month}"
	wf.Steps[0].Site = "127.0.0.1:4000"
	var before int
	e.exec.onAct = func() {
		if len(e.exec.batches) == 1 {
			before = len(e.exec.observes)
		}
	}
	if out := e.replay(wf, map[string]string{"month": "2026-08"}); out.State != StateDone {
		t.Fatalf("outcome %+v", out)
	}
	if before == 0 {
		t.Fatal("acted before any observe opened the window")
	}
	if o := e.exec.observes[0]; o.StartURL != "http://127.0.0.1:4000/bank/statements?month=2026-08" {
		t.Fatalf("first observe %+v", o)
	}
}
