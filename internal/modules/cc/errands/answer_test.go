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
