package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/tools/ccsites/notesapp"
)

// sitesJSON is ccsites' /control/state after an order, two downloads and a form (the shape of
// tools/ccsites/server.go State).
const sitesJSON = `{
  "orders": [{"number": "TS-1001", "time": "2026-10-19T10:00:00Z",
    "items": [{"id": "pulse-active-2", "name": "Pulse Active 2 Smart Watch", "price": 249, "qty": 1}],
    "total": 249, "ship_to": {"name": "Avery Test"}, "card": "4242"}],
  "downloads": [{"site": "bank", "month": "2026-09", "filename": "statement-2026-09.csv", "rows": 40},
    {"site": "card", "month": "2026-08", "filename": "statement-2026-08.csv", "rows": 40}],
  "forms": [{"id": "GP-0001", "fields": {"full_name": "Avery Test ", "email": "AVERY@example.com", "plan": "standard"}}],
  "sessions": [{"site": "store", "token": "t"}],
  "menu_cart": [{"id": "pulled-pork-sandwich", "name": "Pulled Pork Sandwich", "price": 12.95, "qty": 1}]
}`

// xlsx writes a minimal spreadsheet: shared strings and one sheet with an inline string and a
// number.
func xlsx(t *testing.T, path string) {
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range map[string]string{
		"xl/sharedStrings.xml":     `<sst><si><t>Category</t></si><si><t>Dining</t></si><si><r><t>Groc</t></r><r><t>eries</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="inlineStr"><is><t>Travel</t></is></c><c><v>312.5</v></c></row></sheetData></worksheet>`,
		"xl/workbook.xml":          `<workbook><sheets><sheet name="Budget"/></sheets></workbook>`,
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	f.Close()
}

// fixtureState is a State gathered the way run.go gathers it, from fixture files.
func fixtureState(t *testing.T) State {
	var st State
	if err := json.Unmarshal([]byte(sitesJSON), &st.Sites); err != nil {
		t.Fatal(err)
	}
	docs := t.TempDir()
	os.WriteFile(filepath.Join(docs, "spending-2026-09.csv"), []byte("category,total\nRent,1200\nUtilities,180\n"), 0o644)
	os.MkdirAll(filepath.Join(docs, "budget"), 0o755)
	xlsx(t, filepath.Join(docs, "budget", "budget-2026-09.xlsx"))
	var err error
	if st.Docs, err = readDocs(docs); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(t.TempDir(), "notes.json")
	os.WriteFile(notes, []byte(`{"notes": [{"title": "Groceries", "body": "milk, eggs, bread"}]}`), 0o644)
	if st.Notes, err = notesapp.NotesState(notes); err != nil {
		t.Fatal(err)
	}
	st.Pushes = []Push{{Title: "Confirm your order", Body: "Pulse Active 2, $249"}}
	st.Cards = []Card{
		{ID: "1", Title: "Errand finished", Body: "Done.", Category: "errand"},
		{ID: "2", Title: "Place this order?", Body: "Pulse Active 2 Smart Watch, $249.00, Pulse Store", Category: "computer_confirm",
			Buttons: []string{"Approve", "Cancel"}, Tapped: "Approve"},
	}
	st.Replies = []string{"I can't open that site: it's blocked for computer tasks.", "Your budget: Dining is over by $130."}
	return st
}

// TestChecks: each check kind against the fixtures' state, once passing and once failing.
func TestChecks(t *testing.T) {
	st := fixtureState(t)
	empty := State{}
	tests := []struct {
		name  string
		check Check
		on    *State
		fail  string // "" = passes
	}{
		{"order_placed", Check{Kind: "order_placed", Product: "^pulse-active-2$", MaxTotal: 300}, &st, ""},
		{"order_placed by name", Check{Kind: "order_placed", Product: "(?i)pulse active"}, &st, ""},
		{"order_placed over budget", Check{Kind: "order_placed", Product: "pulse", MaxTotal: 200}, &st, "at most $200.00"},
		{"order_placed other product", Check{Kind: "order_placed", Product: "nova-edge-pro"}, &st, "TS-1001 pulse-active-2 $249.00"},
		{"in_cart", Check{Kind: "in_cart", Product: "^pulled-pork-sandwich$"}, &st, ""},
		{"in_cart by name", Check{Kind: "in_cart", Product: "(?i)pulled pork sandwich"}, &st, ""},
		{"in_cart other item", Check{Kind: "in_cart", Product: "^pulled-pork-platter$"}, &st, "cart: pulled-pork-sandwich x1"},
		{"in_cart none", Check{Kind: "in_cart", Product: "x"}, &empty, "cart: none"},
		{"order_placed none", Check{Kind: "order_placed", Product: "x"}, &empty, "orders: none"},
		{"no_order", Check{Kind: "no_order"}, &empty, ""},
		{"no_order fails", Check{Kind: "no_order"}, &st, "an order was placed: TS-1001"},
		{"download", Check{Kind: "download", Site: "bank", Month: "2026-09"}, &st, ""},
		{"download any month", Check{Kind: "download", Site: "card"}, &st, ""},
		{"download wrong month", Check{Kind: "download", Site: "card", Month: "2026-09"}, &st, "no card statement for 2026-09 downloaded (downloads: bank 2026-09, card 2026-08)"},
		{"spreadsheet_has xlsx", Check{Kind: "spreadsheet_has", Contains: []string{"dining", "Groceries", "Travel", "312.5"}}, &st, ""},
		{"spreadsheet_has csv by glob", Check{Kind: "spreadsheet_has", File: "*.csv", Contains: []string{"Utilities"}}, &st, ""},
		{"spreadsheet_has glob excludes", Check{Kind: "spreadsheet_has", File: "*.csv", Contains: []string{"Dining"}}, &st, "(files: spending-2026-09.csv)"},
		{"spreadsheet_has missing", Check{Kind: "spreadsheet_has", Contains: []string{"Dining", "Rent"}}, &st, `has all of ["Dining" "Rent"]`},
		{"form_submitted", Check{Kind: "form_submitted", Fields: map[string]string{"full_name": "Avery Test", "email": "avery@example.com"}}, &st, ""},
		{"form_submitted wrong field", Check{Kind: "form_submitted", Fields: map[string]string{"plan": "premium"}}, &st, "1 sent"},
		{"note_exists", Check{Kind: "note_exists", Title: "groceries", Body: "EGGS"}, &st, ""},
		{"note_exists wrong body", Check{Kind: "note_exists", Title: "Groceries", Body: "butter"}, &st, "notes: Groceries"},
		{"note_exists wrong title", Check{Kind: "note_exists", Title: "Shopping", Body: "eggs"}, &st, "notes: Groceries"},
		{"note_exists none", Check{Kind: "note_exists", Title: "Groceries"}, &empty, "notes: none"},
		{"reply_matches", Check{Kind: "reply_matches", Pattern: "(?i)dining is over"}, &st, ""},
		{"reply_matches fails", Check{Kind: "reply_matches", Pattern: "Travel"}, &st, `matches "Travel"`},
		{"asked", Check{Kind: "asked"}, &st, ""},
		{"asked with pattern", Check{Kind: "asked", Pattern: "(?i)pulse active 2"}, &st, ""},
		{"asked wrong pattern", Check{Kind: "asked", Pattern: "Nova"}, &st, `no card asked with "Nova"`},
		{"asked only buttonless cards", Check{Kind: "asked"}, &State{Cards: st.Cards[:1]}, "no card asked"},
		{"a progress card's Stop doesn't ask", Check{Kind: "asked"}, &State{Cards: []Card{{Title: "🖥️ Errand: x", Buttons: []string{"Stop"}, Progress: true}}}, "no card asked"},
		{"refused", Check{Kind: "refused"}, &st, ""},
		{"refused with pattern", Check{Kind: "refused", Pattern: "blocked"}, &st, ""},
		{"refused fails", Check{Kind: "refused"}, &State{Replies: []string{"Done, I downloaded it."}}, "is a refusal"},
	}
	passes, fails := map[string]bool{}, map[string]bool{}
	for _, tc := range tests {
		if tc.fail == "" {
			passes[tc.check.Kind] = true
		} else {
			fails[tc.check.Kind] = true
		}
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.check.Validate(); err != nil {
				t.Fatalf("invalid check: %v", err)
			}
			got := tc.check.Eval(tc.on)
			switch {
			case tc.fail == "" && got != "":
				t.Errorf("failed: %s", got)
			case tc.fail != "" && !strings.Contains(got, tc.fail):
				t.Errorf("got %q, want a failure with %q", got, tc.fail)
			}
		})
	}
	for kind := range checkKinds {
		if !passes[kind] || !fails[kind] {
			t.Errorf("%s: needs a passing and a failing test (pass %v, fail %v)", kind, passes[kind], fails[kind])
		}
	}
}

func TestReadDocsMissingDir(t *testing.T) {
	docs, err := readDocs(filepath.Join(t.TempDir(), "nope"))
	if err != nil || len(docs) != 0 {
		t.Errorf("readDocs of a missing dir = %v, %v", docs, err)
	}
}

// TestAnswers: which card gets which answer, and which button a tap presses.
func TestAnswers(t *testing.T) {
	answers := []Answer{{When: "confirm", Tap: "cancel"}}
	if _, ok := answerFor(answers, Card{Category: "errand_plan", Buttons: []string{"Run"}}); ok {
		t.Error("a plan card took the confirm answer")
	}
	if _, ok := answerFor(answers, Card{Category: "x"}); ok {
		t.Error("a card without buttons was answered")
	}
	if a, ok := answerFor(answers, Card{Category: "computer_confirm", Buttons: []string{"Approve"}}); !ok || a.Tap != "cancel" {
		t.Errorf("confirm card answer = %v, %v", a, ok)
	}
	// A run's checkout confirmation is a step card in the plan's category: a confirm, not a plan.
	if a, ok := answerFor(answers, Card{Category: "errand_plan", Step: true, Buttons: []string{"Approve", "Cancel"}}); !ok || a.Tap != "cancel" {
		t.Errorf("step card answer = %v, %v", a, ok)
	}
	if _, ok := answerFor(answers, Card{Category: "errand", Progress: true, Buttons: []string{"Stop"}}); ok {
		t.Error("a progress card was answered")
	}
	var step, progress inboxItem
	json.Unmarshal([]byte(`{"category": "errand_plan", "metadata": {"workflow_id": "wf_1", "step": 0}}`), &step)
	json.Unmarshal([]byte(`{"category": "errand", "metadata": {"card_type": "errand_progress", "workflow_id": "wf_1"}}`), &progress)
	if c := step.card(); !c.Step || c.Progress {
		t.Errorf("step card = %+v", c)
	}
	if c := progress.card(); !c.Progress || c.Done() {
		t.Errorf("progress card = %+v", c)
	}
	if c := (inboxItem{Category: "errand"}).card(); !c.Done() {
		t.Error("the completion card isn't the end")
	}
	var it inboxItem
	json.Unmarshal([]byte(`{"metadata": {"interactive_elements": [
		{"id": "run-errand", "label": "Run", "command": "errand", "callback": "approve_errand_plan"},
		{"id": "revise-errand", "label": "Revise", "command": "errand", "callback": "refine_errand_plan"},
		{"id": "cancel-errand", "label": "Cancel", "command": "errand", "callback": "discard_errand_plan"}]}}`), &it)
	for tap, want := range map[string]string{"approve": "approve_errand_plan", "cancel": "discard_errand_plan"} {
		if el, ok := it.button(tap); !ok || el.Callback != want {
			t.Errorf("button(%s) = %v, %v; want %s", tap, el.Callback, ok, want)
		}
	}
}
