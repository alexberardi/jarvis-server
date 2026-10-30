package errands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// espressoSources is what quick_search returns: the pages' text, which the answer is built from.
func espressoSources() *pyjson.Object {
	return servertools.Obj("query", "best espresso machines under $500", "sources", []any{
		servertools.Obj("title", "The 3 Best Espresso Machines Under $500", "url", "https://example.com/best",
			"content", "Our picks: Breville Bambino Plus ($499), Gaggia Classic Pro ($449) and De'Longhi Dedica Arte ($299)."),
		servertools.Obj("title", "Espresso machine reviews", "url", "https://example.org/reviews",
			"content", "The Breville Bambino Plus tops our ratings for milk texture."),
	}, "elapsed_seconds", 1.2)
}

const espressoGoal = "search the web for the three best-reviewed espresso machines under $500 and send me a summary"

const espressoAnswer = "Breville Bambino Plus ($499)\nGaggia Classic Pro ($449)\nDe'Longhi Dedica Arte ($299)\n" +
	"The Bambino Plus is the reviewers' favourite."

func espressoEnv(t *testing.T, autonomous bool) (*tenv, []MenuEntry) {
	e := newTenv(t, true)
	e.set[SettingAutonomous] = autonomous
	e.tools.Register(&fakeTool{name: "quick_search", def: toolDef("quick_search", "Search"), result: espressoSources()})
	e.llm.say("plan", planJSON("Espresso machines", step("quick_search", "Search for espresso machines",
		`{"query": "best espresso machines under $500"}`)))
	e.llm.say("compose", "  "+espressoAnswer+"  ")
	return e, []MenuEntry{{Command: "quick_search", Args: pyjson.NewObject()}}
}

// The composer sees what the search found (#232); the step's own line stays a short status.
func TestComposePromptCarriesStepContent(t *testing.T) {
	r := serverOutcome("quick_search", espressoSources())
	r.Label = "Search for espresso machines"
	if r.Message != "2 sources" {
		t.Fatalf("step message = %q", r.Message)
	}
	p := ComposePrompt(espressoGoal, []Result{r})
	for _, want := range []string{
		"- Search for espresso machines [ok]: 2 sources",
		"The 3 Best Espresso Machines Under $500",
		"Gaggia Classic Pro ($449)",
		"tops our ratings",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("compose prompt lacks %q:\n%s", want, p)
		}
	}
}

// Long pages are cut so the prompt stays bounded.
func TestStepContentIsCapped(t *testing.T) {
	long := strings.Repeat("espresso ", 4000)
	o := servertools.Obj("sources", []any{servertools.Obj("title", "A", "content", long),
		servertools.Obj("title", "B", "content", long), servertools.Obj("title", "C", "content", long)})
	got := stepContent(Result{Success: true, Data: o})
	if n := runeLen(got); n > stepContentLimit+200 || n < 1000 {
		t.Fatalf("content is %d runes", n)
	}
	if !strings.Contains(got, "C") {
		t.Fatal("a later source was crowded out entirely")
	}
	if stepContent(Result{Success: true, Data: servertools.Obj("message", "done")}) != "" {
		t.Fatal("a plain result has content")
	}
}

func TestRanCardCarriesAnswer(t *testing.T) {
	e, menu := espressoEnv(t, true)
	e.policy.set("quick_search", allowTool)
	p := e.draft(espressoGoal, ptr(7), menu)
	e.idle()
	if st := e.wfState(p.WorkflowID); st != "done" {
		t.Fatalf("state = %s", st)
	}
	up, ok := e.cards.update(p.InboxItemID)
	if !ok {
		t.Fatal("ran card not updated")
	}
	if up.Metadata["answer"] != espressoAnswer {
		t.Fatalf("answer = %#v", up.Metadata["answer"])
	}
	steps := up.Metadata["steps"].([]any)
	if res := steps[0].(map[string]any)["result"]; res != "2 sources" {
		t.Fatalf("step result = %#v", res)
	}
	if !strings.HasPrefix(up.Body, espressoAnswer+"\n\n") || !strings.Contains(up.Body, "1. ✅ Search for espresso machines — 2 sources") {
		t.Fatalf("body = %q", up.Body)
	}
	if up.Summary != "Breville Bambino Plus ($499)" {
		t.Fatalf("summary = %q", up.Summary)
	}
	cp := *e.llm.requests("compose")[0].Messages[0].Content.Text
	if !strings.Contains(cp, "De'Longhi Dedica Arte ($299)") {
		t.Fatalf("compose prompt:\n%s", cp)
	}
}

// A run started by a Run tap gets the same answer on its completion card.
func TestCompletionCardCarriesAnswer(t *testing.T) {
	e, menu := espressoEnv(t, false)
	p := e.draft(espressoGoal, ptr(7), menu)
	launch(t, e, p)
	e.idle()
	cards := e.cards.all()
	last := cards[len(cards)-1]
	if last.Title != "✅ Errand done: Espresso machines" || last.Body != espressoAnswer ||
		last.Summary != "Breville Bambino Plus ($499)" || last.Metadata["answer"] != espressoAnswer ||
		last.Metadata["status"] != "success" {
		t.Fatalf("completion = %+v", last)
	}
}

// A compose failure falls back to the plain roll-up, which is no answer.
func TestNoAnswerWhenComposeFails(t *testing.T) {
	e, menu := espressoEnv(t, false)
	e.llm.fail("compose", errors.New("model down"))
	p := e.draft(espressoGoal, ptr(7), menu)
	launch(t, e, p)
	e.idle()
	cards := e.cards.all()
	last := cards[len(cards)-1]
	if _, ok := last.Metadata["answer"]; ok || last.Summary != "Search for espresso machines: 2 sources" {
		t.Fatalf("completion = %+v", last)
	}
}

// deep_research only starts a report that arrives on its own later, so the errand menu says so
// and points at quick_search for what the errand should report back (#232).
func TestMenuNotesDeepResearchIsDetached(t *testing.T) {
	e := newTenv(t, false)
	e.tools.Register(&fakeTool{name: "deep_research", def: toolDef("deep_research", "Research a topic in depth.")})
	e.tools.Register(&fakeTool{name: "quick_search", def: toolDef("quick_search", "Search the web.")})
	e.set[settingWebSearch] = true
	m := e.s.FullMenu(context.Background(), nil, hh, nil)
	if len(m) != 2 || m[0].Command != "deep_research" || m[1].Description != "Search the web." {
		t.Fatalf("menu = %+v", m)
	}
	if d := m[0].Description; !strings.HasPrefix(d, "Research a topic in depth. ") || !strings.Contains(d, "use quick_search") {
		t.Fatalf("deep_research = %q", d)
	}
}

func TestCardSummarySkipsLeadIn(t *testing.T) {
	for answer, want := range map[string]string{
		"Here are the three:\n\n1. Gaggia Classic Pro ($449)\n2. Bambino": "1. Gaggia Classic Pro ($449)",
		"It will rain tomorrow.": "It will rain tomorrow.",
		"Found these:":           "Found these:",
		strings.Repeat("a", 300): strings.Repeat("a", summaryLimit-1) + "…",
	} {
		if got := cardSummary(Aggregate{Message: answer, Answer: answer}); got != want {
			t.Errorf("summary of %q = %q", answer, got)
		}
	}
	if got := cardSummary(Aggregate{Message: "Search: 2 sources"}); got != "Search: 2 sources" {
		t.Errorf("no answer: %q", got)
	}
}

// Asking for the result to be sent to the user is fulfilled by the report (seen on the sandbox:
// "I did not send this summary" after a "send me a summary" goal).
func TestComposePromptSendMeIsTheReport(t *testing.T) {
	p := ComposePrompt(espressoGoal, []Result{serverOutcome("quick_search", espressoSources())})
	if !strings.Contains(p, "'send me a summary', 'let me know') is NOT an undone action") ||
		!strings.Contains(p, "no lead-in line") {
		t.Fatalf("compose prompt:\n%s", p)
	}
}

// #452: the 9B planned quick_search + remember ("save a summary") for a question. The remember
// step held autorun (it asks) and the report said "I saved a summary … but I did not send it to
// you". A question's errand is its lookup; the report delivers it.
func TestUnaskedRememberIsDropped(t *testing.T) {
	e := newTenv(t, true)
	e.set[SettingAutonomous] = true
	e.tools.Register(&fakeTool{name: "quick_search", def: toolDef("quick_search", "Search"), result: espressoSources()})
	e.llm.say("plan", planJSON("Espresso machines",
		step("quick_search", "Search for espresso machines", `{"query": "best espresso machines under $500"}`),
		step("remember", "Save a summary", `{"fact": "the best espresso machines under $500"}`)))
	e.llm.say("compose", espressoAnswer)
	menu := []MenuEntry{{Command: "quick_search", Args: pyjson.NewObject()}, {Command: "remember", Args: pyjson.NewObject()}}
	e.policy.set("quick_search", allowTool)
	const goal = "find the best espresso machines under $500 with prices"
	p := e.draft(goal, ptr(7), menu)
	e.idle()
	if st := e.wfState(p.WorkflowID); st != "done" {
		t.Fatalf("state = %s (the plan should autorun)", st)
	}
	up, ok := e.cards.update(p.InboxItemID)
	if !ok || up.Metadata["answer"] != espressoAnswer {
		t.Fatalf("ran card = %+v", up)
	}
	if steps := up.Metadata["steps"].([]any); len(steps) != 1 {
		t.Fatalf("steps = %#v", steps)
	}
}

func TestDropUnaskedRemember(t *testing.T) {
	search := Step{Command: "quick_search", Label: "Search"}
	remember := Step{Command: "remember", Label: "Save it"}
	for _, c := range []struct {
		goal string
		in   []Step
		want int
	}{
		{"find the best espresso machines under $500 with prices", []Step{search, remember}, 1},
		{"what are the top rated air fryers", []Step{remember, search}, 1},
		{"look up the best espresso machine and remember it", []Step{search, remember}, 2},
		{"search for flights to Rome and save the cheapest one", []Step{search, remember}, 2},
		{"make a note that my locker code is 1234", []Step{remember}, 1},
		{"don't forget that the plumber comes on Tuesday", []Step{remember}, 1},
		// Nothing else to run: keep it rather than leave no plan.
		{"my locker code is 1234", []Step{remember}, 1},
	} {
		if got := dropUnaskedRemember(c.goal, c.in); len(got) != c.want {
			t.Errorf("%q: %d steps, want %d (%+v)", c.goal, len(got), c.want, got)
		}
	}
}

// The prompt says the report is the answer, and remember only when asked; menus without
// quick_search or remember keep their bytes.
func TestAnswerRuleInPrompts(t *testing.T) {
	menu := []MenuEntry{{Command: "quick_search", Description: "Search the web."}, {Command: "remember", Description: "Save a fact."}}
	for name, p := range map[string]string{
		"plan":   BuildPrompt("find espresso machines", menu),
		"refine": BuildRefinePrompt("s", nil, "i", menu),
		"replan": BuildReplanPrompt("g", nil, "", "r", menu),
	} {
		if !strings.Contains(p, answerRule) {
			t.Errorf("%s prompt lacks the answer rule:\n%s", name, p)
		}
	}
	if strings.Contains(BuildPrompt("check the weather", commandMenu()[:1]), "ANSWERS:") {
		t.Error("answer rule on a menu without quick_search or remember")
	}
	for _, want := range []string{"report IS the answer", "prices", "remember"} {
		if !strings.Contains(answerRule, want) {
			t.Errorf("answer rule lacks %q", want)
		}
	}
}

// A save step the user did ask for is not a reason to say the result was not sent.
func TestComposePromptSaveIsNotUnsent(t *testing.T) {
	p := ComposePrompt(espressoGoal, []Result{serverOutcome("quick_search", espressoSources())})
	if !strings.Contains(p, "saving or noting the result is not sending it either") {
		t.Fatalf("compose prompt:\n%s", p)
	}
}

// A deep_research step beside a write_document with no source becomes its source (C27): the
// report would arrive after the document was written.
func TestFoldResearchIntoDocument(t *testing.T) {
	research := Step{Command: "deep_research", Args: servertools.Obj("query", "Apollo 11 conspiracy theories")}
	doc := func(args ...any) Step { return Step{Command: cmdWriteDoc, Args: servertools.Obj(args...)} }
	got := foldResearchIntoDocument([]Step{research, doc("path", "a.odt", "instructions", "x")})
	if len(got) != 1 || got[0].Command != cmdWriteDoc || truthyStr(got[0].Args, "source") != "Apollo 11 conspiracy theories" {
		t.Fatalf("folded: %+v", got)
	}
	kept := []Step{research, doc("path", "a.odt", "source", "https://example.com")}
	if got := foldResearchIntoDocument(kept); len(got) != 2 {
		t.Fatalf("a document with its own source: %+v", got)
	}
	if got := foldResearchIntoDocument([]Step{research}); len(got) != 1 {
		t.Fatalf("no document: %+v", got)
	}
}

// A server step sees the earlier successful steps and what they returned.
func TestPriorResults(t *testing.T) {
	got := PriorResults([]Result{
		{Command: "quick_search", Label: "Search", Success: true, Message: "3 results", Data: servertools.Obj("answer", "Kodak")},
		{Command: "remember", Label: "Note", Error: "off"},
		{Command: cmdReplan, Success: true, Control: true},
	})
	if got != "- Search: 3 results\n  {\"answer\": \"Kodak\"}" {
		t.Fatalf("prior %q", got)
	}
}

// A spreadsheet planned after a search is written from the search's results when it runs, not
// from rows the planner made up (C27); after a file read the checkpoint handles it.
func TestSheetFromResults(t *testing.T) {
	sheet := Step{Command: cmdWriteSheet, Label: "Save", Args: servertools.Obj("name", "planets", "sheets", []any{
		servertools.Obj("title", "Planets", "columns", []any{"Planet", "Moons"}, "rows", []any{[]any{"Saturn", 146}})})}
	search := Step{Command: cmdSearch, Args: servertools.Obj("query", "moons per planet")}
	got := sheetFromResults("Put the planets' moons in planets.xlsx.", []Step{search, sheet})
	if len(got) != 2 || got[1].Command != cmdWriteDoc || truthyStr(got[1].Args, "path") != "~/Documents/planets.xlsx" {
		t.Fatalf("converted: %+v", got)
	}
	if instr := truthyStr(got[1].Args, "instructions"); !strings.Contains(instr, `["Planet", "Moons"]`) || strings.Contains(instr, "146") {
		t.Fatalf("instructions %q", instr)
	}
	if got := sheetFromResults("x", []Step{sheet}); got[0].Command != cmdWriteSheet {
		t.Fatal("rows the user gave were converted")
	}
	read := Step{Command: cmdReadFile, Args: servertools.Obj("path", "a.csv")}
	if got := sheetFromResults("x", []Step{read, search, sheet}); got[2].Command != cmdWriteSheet {
		t.Fatal("the budget flow was converted")
	}
}
