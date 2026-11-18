package computer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

func TestPastCheckout(t *testing.T) {
	obs := Observation{Elements: []Element{{Ref: "e1", Role: "button", Name: "CHECK OUT · $15.98"}, {Ref: "e2", Role: "button", Name: "Add to cart"}}}
	press := func(ref string) []computeruse.Action { return []computeruse.Action{{Kind: "press", Ref: ref}} }
	if pastCheckout("add a pizza to the cart, stop before checkout", obs, press("e1")) == "" {
		t.Error("checkout pressed though the request says to stop before it")
	}
	if got := pastCheckout("add a pizza to the cart, stop before checkout", obs, press("e2")); got != "" {
		t.Errorf("add to cart refused: %s", got)
	}
	if got := pastCheckout("order me a pizza", obs, press("e1")); got != "" {
		t.Errorf("checkout refused on an order: %s", got)
	}
}

func TestPlaceDetails(t *testing.T) {
	obs := Observation{Elements: []Element{
		{Ref: "e1", Role: "textbox", Name: "Street Address"},
		{Ref: "e2", Role: "textbox", Name: "Search products"},
		{Ref: "e3", Role: "textbox", Name: "Mobile", Autocomplete: "tel-national"},
		{Ref: "e4", Role: "textbox", Name: "Company name"},
	}}
	typ := func(ref, text string) []computeruse.Action {
		return []computeruse.Action{{Kind: "type", Ref: ref, Text: text}}
	}
	said := "order a pizza to 12 Elm Street, Springfield"
	saved := []Detail{{"callback_number", "Callback number", "555-0199"}, {"street", "Street", "9 Oak Lane"}}
	text := func(ds []Detail, ref, in string) string {
		t.Helper()
		out, q := placeDetails(said, ds, obs, typ(ref, in))
		if q != "" {
			return "ASK: " + q
		}
		return out[0].Text
	}
	for _, c := range []struct{ name, ref, in, want string }{
		{"an invented address with nothing to match it asks", "e1", "123 Main St", "ASK"},
		{"the request's own address is typed", "e1", "12 Elm Street", "12 Elm Street"},
		{"a search is not a detail", "e2", "pepperoni", "pepperoni"},
		{"an invented phone becomes the saved one by the field's token", "e3", "555-0100", "{{callback_number}}"},
		{"a saved detail's placeholder is typed", "e1", "{{street}}", "{{street}}"},
		{"a placeholder for nothing saved asks", "e4", "{{company}}", "ASK"},
	} {
		if got := text(saved, c.ref, c.in); got != c.want && !(c.want == "ASK" && strings.HasPrefix(got, "ASK")) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// Off the task's site no detail is offered: the placeholder and the invention both ask.
	for _, in := range []string{"{{callback_number}}", "555-0100"} {
		if got := text(nil, "e3", in); !strings.HasPrefix(got, "ASK") {
			t.Errorf("without details %q was typed as %q", in, got)
		}
	}
	if got := fillDetails("{{street}} / {{nope}}", saved); got != "9 Oak Lane / {{nope}}" {
		t.Errorf("fillDetails = %q", got)
	}
	msgs := []llm.Message{{Role: "user", Content: &llm.Content{Parts: []llm.Part{{Type: "text", Text: "Street: 9 Oak Lane", CachePrefix: 5}}}}}
	msgs = append(msgs, llm.Message{Role: "user", Content: llm.TextContent(`now holds "9 Oak Lane"`)})
	maskDetails(msgs, saved)
	if got := *msgs[1].Content.Text; got != `now holds "{{street}}"` {
		t.Errorf("masked text = %q", got)
	}
	if p := msgs[0].Content.Parts[0]; p.Text != "Street: {{street}}" || p.CachePrefix != 0 {
		t.Errorf("masked = %+v", p)
	}
	if line := detailsLine(saved); !strings.Contains(line, "{{street}} Street") || strings.Contains(line, "Oak") {
		t.Errorf("detailsLine = %q", line)
	}
}

func TestOtherSite(t *testing.T) {
	nav := func(u string) []computeruse.Action { return []computeruse.Action{{Kind: "navigate", URL: u}} }
	if otherSite("amazon.com", nav("https://www.bestbuy.com/")) == "" {
		t.Error("another shop opened from a task for amazon.com")
	}
	for _, u := range []string{"https://www.amazon.com/s?k=x", "https://smile.amazon.com/"} {
		if got := otherSite("amazon.com", nav(u)); got != "" {
			t.Errorf("%s refused: %s", u, got)
		}
	}
	if got := otherSite("", nav("https://www.bestbuy.com/")); got != "" {
		t.Errorf("a task with no site refused: %s", got)
	}
}

// The same button pressed round after round is stuck, even when each new snapshot gives it
// another ref and flips what the page shows.
func TestRepeatsAreCountedByElementName(t *testing.T) {
	page := func(order string) Observation {
		return Observation{URL: "https://shop.example.com/list", Title: order, Gen: 1,
			Elements: []Element{{Ref: "e1", Role: "button", Name: "price"}, {Ref: "e2", Role: "button", Name: "price"}}}
	}
	e := newEnv(t, page("start"))
	var script []string
	for i := 0; i < 12; i++ {
		e.exec.pages = append(e.exec.pages, page(strconv.Itoa(i)))
		script = append(script, actions(press("e"+strconv.Itoa(1+i%2), "Sort by price")))
	}
	e.script(script...)
	if out := e.loop.Run(context.Background(), task()); out.State != StateFailed || len(e.llm.reqs) > stuckGiveUp+1 {
		t.Fatalf("outcome %+v after %d calls", out, len(e.llm.reqs))
	}
}

func TestKeysAtLastElement(t *testing.T) {
	obs := Observation{Elements: []Element{{Ref: "e4", Role: "searchbox", Name: "Search"}, {Ref: "e5", Role: "button", Name: "Go"}}}
	hist := []Entry{{Kind: "set_value", On: `searchbox "Search"`}, {Kind: "said"}}
	got := keysAtLastElement(obs, hist, []computeruse.Action{{Kind: "keypress", Keys: []string{"Enter"}}})
	if got[0].Ref != "e4" {
		t.Errorf("Enter went to %q, not the search box just typed into", got[0].Ref)
	}
	got = keysAtLastElement(obs, []Entry{{Kind: "press", On: `link "Gone"`}}, []computeruse.Action{{Kind: "keypress", Keys: []string{"Enter"}}})
	if got[0].Ref != "" {
		t.Errorf("Enter went to %q though the last element is gone", got[0].Ref)
	}
}

// A saved detail is typed by placeholder: the computer gets the value, the model and the run's
// history never do, even once the page shows it.
func TestSavedDetailReachesOnlyTheComputer(t *testing.T) {
	page := formPage()
	filled := formPage()
	filled.Elements[0].Value = "Avery Quill"
	e := newEnv(t, page, filled)
	e.loop.Details = func(context.Context, *int64) []Detail { return []Detail{{"full_name", "Full name", "Avery Quill"}} }
	e.script(actions(`{"kind": "set_value", "ref": "e1", "text": "{{full_name}}", "intent": "Name"}`),
		`{"done": true, "summary": "Filled in.", "verify": {"url_contains": "/apply"}}`,
		`{"done": true, "summary": "Filled in.", "verify": {"url_contains": "/apply"}}`,
		`{"done": true, "summary": "Filled in.", "verify": {"url_contains": "/apply"}}`)
	apply := task()
	apply.Goal, apply.Target.Site = "Fill in the application with my details, don't send it", "forms.example.com"
	out := e.loop.Run(context.Background(), apply)
	if len(e.exec.batches) != 1 || e.exec.batches[0].Actions[0].Text != "Avery Quill" {
		t.Fatalf("the computer got %+v (outcome %+v)", e.exec.batches, out)
	}
	if !strings.Contains(userText(e.llm.reqs[0]), "{{full_name}} Full name") {
		t.Errorf("the model isn't told the detail exists:\n%s", userText(e.llm.reqs[0]))
	}
	for i, req := range e.llm.reqs {
		if strings.Contains(userText(req), "Avery") {
			t.Errorf("call %d shows the model the value:\n%s", i, userText(req))
		}
	}
	if n := len(e.llm.reqs); n != 2 {
		t.Errorf("%d calls: a task that says not to send is done with the form unsent", n)
	}
	run, err := e.loop.Store.Get(context.Background(), out.RunID)
	if err != nil || len(run.History) == 0 || strings.Contains(fmt.Sprint(run.History), "Avery") {
		t.Errorf("history = %+v, %v", run, err)
	}
}

func TestOnSite(t *testing.T) {
	for _, c := range []struct {
		site, page string
		want       bool
	}{
		{"shop.example", "https://www.shop.example/cart", true},
		{"https://www.shop.example/start", "https://pay.shop.example/x", true},
		{"http://127.0.0.1:8765/form.html", "http://127.0.0.1:8765/form.html", true},
		{"shop.example", "https://shop.example.evil.test/", false},
		{"shop.example", "https://evilshop.example/", false},
		{"", "https://shop.example/", false},
	} {
		if got := onSite(c.site, c.page); got != c.want {
			t.Errorf("onSite(%q, %q) = %v", c.site, c.page, got)
		}
	}
}

// A lookup's done with nothing found in its summary goes back once: the summary is the answer.
func TestLookupDoneWithoutFindingsAsksOnce(t *testing.T) {
	e := newEnv(t, formPage())
	e.script(`{"done": true, "summary": "Found the cheapest soaps.", "verify": {"url_contains": "/apply"}}`,
		`{"done": true, "summary": "Lavender soap, $4.", "verify": {"url_contains": "/apply"}}`)
	look := task()
	look.Goal = "List the cheapest soaps with their prices"
	out := e.loop.Run(context.Background(), look)
	if out.State != StateDone || len(e.llm.reqs) != 2 || !strings.Contains(out.Summary, "$4") {
		t.Fatalf("outcome %+v, %d calls", out, len(e.llm.reqs))
	}
	if txt := userText(e.llm.reqs[1]); !strings.Contains(txt, "your summary doesn't hold what you found") {
		t.Fatalf("no feedback:\n%s", txt)
	}
}

func TestLongIntentIsCut(t *testing.T) {
	r, err := parseReply(`{"actions": [{"kind": "press", "ref": "e1", "intent": "` + strings.Repeat("x", 400) + `"}]}`)
	if err != nil || len(r.Actions) != 1 || len([]rune(r.Actions[0].Intent)) != computeruse.MaxIntent {
		t.Fatalf("reply %+v, %v", r, err)
	}
}

func TestThoughtObjectThenAnswer(t *testing.T) {
	r, err := parseReply(`{"thought": "All filled in.", "actions": []}` + "\n\n" + `{"done": true, "summary": "Filled it in."}`)
	if err != nil || !r.Done || r.Summary != "Filled it in." || r.Thought != "All filled in." {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestFirstObjectIsTheAnswer(t *testing.T) {
	for _, tail := range []string{"\n{\"done\": true, \"summary\": \"x\"}", "\n```\nThat fills the form.", " }"} {
		r, err := parseReply(`{"actions": [{"kind": "press", "ref": "e1", "intent": "Open"}]}` + tail)
		if err != nil || len(r.Actions) != 1 || r.Done {
			t.Errorf("tail %q: %+v, %v", tail, r, err)
		}
	}
}

// Waiting on a page that isn't changing is sent back, and ends the task if it goes on.
func TestWaitingOnALoadedPageStops(t *testing.T) {
	e := newEnv(t, formPage())
	wait := actions(`{"kind": "wait", "ms": 2000, "intent": "Wait for the page to load"}`)
	for range 12 {
		e.script(wait)
	}
	out := e.run()
	if out.State != StateFailed || len(e.exec.batches) > 3 {
		t.Fatalf("outcome %+v after %d batches, %d calls", out, len(e.exec.batches), len(e.llm.reqs))
	}
	told := false
	for _, req := range e.llm.reqs {
		told = told || strings.Contains(userText(req), "Waiting again changes nothing")
	}
	if !told {
		t.Fatal("the model was never told the page had loaded")
	}
}
