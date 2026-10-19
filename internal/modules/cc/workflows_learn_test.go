package cc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden")

// bankTrace is testdata/bank_trace.jsonl as the JSON list a recording stores.
func bankTrace(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "bank_trace.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var evs []json.RawMessage
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
			evs = append(evs, json.RawMessage(slices.Clone(line)))
		}
	}
	out, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// bankModel is what a model answers for the bank trace: an invented event (42) and an
// invented param, and a param of an unknown kind, which the job drops.
const bankModel = `{"name": "Download bank statement", "triggers": ["download my bank statement", " "],
 "steps": [
  {"intent": "Open the bank's sign-in page", "event_seqs": [1], "target": "the sign-in page"},
  {"intent": "Enter the username", "event_seqs": [2], "target": "Username"},
  {"intent": "Enter the password", "event_seqs": [3], "target": "Password"},
  {"intent": "Sign in", "event_seqs": [4, 5], "target": "Sign in"},
  {"intent": "Open the statements", "event_seqs": [6, 7], "target": "Statements"},
  {"intent": "Enter the month {month}", "event_seqs": [8], "target": "Statement month"},
  {"intent": "Download the CSV", "event_seqs": [9, 10], "target": "Download CSV"},
  {"intent": "Log out", "event_seqs": [42], "target": "Log out"}
 ],
 "params": [
  {"name": "month", "kind": "month", "example": "2026-09", "from_seqs": [7, 8]},
  {"name": "account", "kind": "text", "example": "checking", "from_seqs": [42]},
  {"name": "user", "kind": "colour", "example": "alex", "from_seqs": [2]}
 ],
 "success": {"download": {"name_like": "statement_*.csv"}}}`

type learnEnv struct {
	*cbEnv
	model *fakeWatchLLM
}

func newLearnEnv(t *testing.T, reply string) *learnEnv {
	t.Helper()
	withEnvExtra(t, envExtra{queue: true})
	model := &fakeWatchLLM{reply: reply}
	ce := newCBEnv(t, envOpts{noMQTT: true, configure: func(m *Module) { m.LLM = model }})
	return &learnEnv{cbEnv: ce, model: model}
}

// store hands a recording to the module as an agent's upload does, and waits for the workflow
// learnt from it.
func (le *learnEnv) store(id string, trace json.RawMessage, narration string) TaughtWorkflow {
	le.t.Helper()
	ctx := context.Background()
	if _, err := le.m.storeAgentRecording(ctx, Recording{ID: id, HouseholdID: cbHH, OwnerID: 5, NodeID: "node-mac",
		Mode: "browser", Trace: trace, Narration: narration}); err != nil {
		le.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ws, err := le.m.listWorkflows(ctx, cbHH, 5)
		if err != nil {
			le.t.Fatal(err)
		}
		for _, w := range ws {
			if w.RecordingID == id {
				return w
			}
		}
		if time.Now().After(deadline) {
			le.t.Fatal("no workflow learnt from", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLearnFromBankTrace(t *testing.T) {
	le := newLearnEnv(t, bankModel)
	w := le.store("rec_bank1", bankTrace(t), "this is how I get my bank statement")

	if w.Name != "Download bank statement" || w.OwnerID != 5 || w.Shared || w.HouseholdID != cbHH {
		t.Fatalf("workflow %+v", w)
	}
	if !slices.Equal(w.Triggers, []string{"download my bank statement"}) {
		t.Fatalf("triggers %q", w.Triggers)
	}
	type st struct{ intent, kind, text, url, ref, sel, frame, risk string }
	var got []st
	for _, s := range w.Steps {
		if s.Target != "browser" || s.Site != "bank.example.com" || s.Action.Intent != s.Intent {
			t.Fatalf("step %+v", s)
		}
		x := st{intent: s.Intent, kind: s.Action.Kind, text: s.Action.Text, url: s.Action.URL, ref: s.Action.Ref, risk: s.Risk}
		if s.Element != nil {
			x.sel, x.frame = s.Element.Selector, s.Element.Frame
		}
		got = append(got, x)
	}
	want := []st{
		{intent: "Open the bank's sign-in page", kind: "navigate", url: "https://bank.example.com/login"},
		{intent: "Enter the username", kind: "type", text: "alex", sel: "#username"},
		{intent: "Enter the password", kind: "type", text: maskedValue, sel: "#password", risk: computeruse.CatSensitiveInput},
		{intent: "Sign in", kind: "press", ref: recordedRef, sel: "#sign-in"},
		{intent: "Open the statements", kind: "press", ref: recordedRef, sel: "#nav-statements"},
		{intent: "Enter the month {month}", kind: "type", text: "{month}", sel: "#month"},
		{intent: "Download the CSV", kind: "press", ref: recordedRef, sel: "[data-testid=download-csv]", frame: "2"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("steps\n got %+v\nwant %+v", got, want)
	}
	if e := w.Steps[6].Element; e.Role != "button" || e.Name != "Download CSV" {
		t.Fatalf("element %+v", e)
	}
	if len(w.Params) != 1 || w.Params[0] != (Param{Name: "month", Kind: "month", Example: "2026-09"}) {
		t.Fatalf("params %+v", w.Params)
	}
	if d := w.Success.Download; d == nil || d.NameLike != "statement_*.csv" {
		t.Fatalf("success %+v", w.Success)
	}

	// One background call, with the trace in it and the password never.
	le.model.mu.Lock()
	reqs := slices.Clone(le.model.reqs)
	le.model.mu.Unlock()
	if len(reqs) != 1 || reqs[0].Label != llm.LabelBackground {
		t.Fatalf("model calls %+v", reqs)
	}

	// The card asks the owner to review it.
	le.notify.mu.Lock()
	items := slices.Clone(le.notify.items)
	le.notify.mu.Unlock()
	if len(items) != 1 {
		t.Fatalf("cards %+v", items)
	}
	it := items[0]
	path := "/workflows/" + w.ID
	if it.Title != `I learned "Download bank statement"` || it.Summary != `I learned "Download bank statement" (7 steps). Review it.` ||
		it.UserID == nil || *it.UserID != 5 || it.HouseholdID != cbHH || it.Category != learnCardCategory ||
		!strings.Contains(it.Body, "[Review]("+path+")") || it.Metadata["path"] != path || it.Metadata["workflow_id"] != w.ID {
		t.Fatalf("card %+v", it)
	}

	// The same recording again (a retried job) learns nothing new.
	if err := le.m.learnFromRecording(context.Background(), "rec_bank1"); err != nil {
		t.Fatal(err)
	}
	if ws, _ := le.m.listWorkflows(context.Background(), cbHH, 5); len(ws) != 1 {
		t.Fatalf("%d workflows", len(ws))
	}
}

func TestLearnRejectsBadAnswers(t *testing.T) {
	ctx := context.Background()
	le := newLearnEnv(t, "")
	for i, reply := range []string{
		`not json`,
		`{"name": "Nothing", "steps": [{"intent": "Make it up", "event_seqs": [99]}]}`,
	} {
		le.model.mu.Lock()
		le.model.reply = reply
		le.model.mu.Unlock()
		id := fmt.Sprintf("rec_bad%d", i)
		if _, err := le.m.createRecording(ctx, Recording{ID: id, HouseholdID: cbHH, OwnerID: 5, Mode: "browser", Trace: bankTrace(t)}); err != nil {
			t.Fatal(err)
		}
		if err := le.m.learnFromRecording(ctx, id); err == nil {
			t.Fatalf("%s: learnt", reply)
		}
		if ws, _ := le.m.listWorkflows(ctx, cbHH, 5); len(ws) != 0 || len(le.items()) != 0 {
			t.Fatalf("%s: %d workflows, cards %q", reply, len(ws), le.items())
		}
	}
	// A recording deleted before the job ran is nothing to do.
	if err := le.m.learnFromRecording(ctx, "rec_gone"); err != nil {
		t.Fatal(err)
	}
}

func TestParamCandidates(t *testing.T) {
	typed := func(v string) traceEvent {
		return traceEvent{Kind: "change", Value: v, Element: &traceElement{Role: "textbox", Name: "Field"}}
	}
	evs := []traceEvent{
		typed("2026-09"),
		typed("September"),
		typed("statement_2026-09.csv"),
		typed("$42.10"),
		typed("2026-10-14"),
		typed("10/14/2026"),
		{Kind: "navigate", URL: "https://bank.example.com/statements?month=09&acct=chk"},
		{Kind: "navigate", URL: "https://bank.example.com/statements/2026-08/view"},
		typed("alex"),
		{Kind: "change", Masked: true, Element: &traceElement{Role: "textbox", Name: "Password", Type: "password"}},
		{Kind: "download", URL: "https://bank.example.com/dl?id=7", Value: "statement_2026-09.csv"},
		typed("Pay 12.50 on Oct 3"),
	}
	type c struct {
		ev          int
		kind, value string
	}
	var got []c
	for _, p := range paramCandidates(evs) {
		got = append(got, c{p.Event, p.Kind, p.Value})
	}
	want := []c{
		{1, "month", "2026-09"},
		{2, "month", "September"},
		{3, "file", "statement_2026-09.csv"},
		{4, "number", "$42.10"},
		{5, "date", "2026-10-14"},
		{6, "date", "10/14/2026"},
		{7, "month", "09"},
		{8, "month", "2026-08"},
		{11, "file", "statement_2026-09.csv"},
		{12, "number", "12.50"},
		{12, "date", "Oct 3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates\n got %v\nwant %v", got, want)
	}
}

func TestRiskMarked(t *testing.T) {
	trace := `[
	{"seq":1,"kind":"navigate","url":"https://water.example.com/billing"},
	{"seq":2,"kind":"change","url":"https://water.example.com/billing","element":{"role":"textbox","name":"PIN","type":"password","selector":"#pin"},"masked":true},
	{"seq":3,"kind":"change","url":"https://water.example.com/billing","element":{"role":"textbox","name":"Amount","selector":"#amount"},"value":"$42.10"},
	{"seq":4,"kind":"click","url":"https://water.example.com/billing","element":{"role":"button","name":"Pay now","selector":"#pay"}},
	{"seq":5,"kind":"key","url":"https://water.example.com/billing","key":"Escape"}]`
	le := newLearnEnv(t, `{"name": "Pay the water bill", "triggers": ["pay the water bill"],
	 "steps": [{"intent": "Open billing", "event_seqs": [1]}, {"intent": "Enter the PIN", "event_seqs": [2]},
	  {"intent": "Enter {amount}", "event_seqs": [3]}, {"intent": "Pay", "event_seqs": [4]}, {"intent": "Close the dialog", "event_seqs": [5]}],
	 "params": [{"name": "Amount Due", "kind": "number", "example": "$42.10", "from_seqs": [3]}],
	 "success": {"text_present": ["Payment received"]}}`)
	w := le.store("rec_water", json.RawMessage(trace), "")
	var risks, kinds []string
	for _, s := range w.Steps {
		risks, kinds = append(risks, s.Risk), append(kinds, s.Action.Kind)
	}
	if !slices.Equal(risks, []string{"", computeruse.CatSensitiveInput, "", computeruse.CatPay, ""}) {
		t.Fatalf("risks %q", risks)
	}
	if !slices.Equal(kinds, []string{"navigate", "type", "type", "press", "keypress"}) || !slices.Equal(w.Steps[4].Action.Keys, []string{"escape"}) {
		t.Fatalf("kinds %q, keys %q", kinds, w.Steps[4].Action.Keys)
	}
	if len(w.Params) != 1 || w.Params[0].Name != "amount_due" || w.Steps[2].Action.Text != "{amount_due}" {
		t.Fatalf("params %+v, text %q", w.Params, w.Steps[2].Action.Text)
	}
	if !slices.Equal(w.Success.TextPresent, []string{"Payment received"}) || w.Success.Download != nil {
		t.Fatalf("success %+v", w.Success)
	}
}

func TestLearnPromptGolden(t *testing.T) {
	evs, err := parseTrace(bankTrace(t))
	if err != nil {
		t.Fatal(err)
	}
	got := "SYSTEM:\n" + learnSystemPrompt + "\n\nUSER:\n" + learnPrompt(evs, paramCandidates(evs), "this is how I get my bank statement") + "\n"
	if !strings.Contains(got, `type into textbox "Password": •••`) {
		t.Fatalf("the masked password isn't shown as •••:\n%s", got)
	}
	path := filepath.Join("testdata", "learn_prompt.golden")
	if *updateGolden {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("prompt differs from %s (run with -update after checking):\n%s", path, got)
	}
}

func TestLearnDefaults(t *testing.T) {
	evs, err := parseTrace(bankTrace(t))
	if err != nil {
		t.Fatal(err)
	}
	// A download with no check from the model checks the downloaded name, its numbers as *.
	if sc := successCheck(computer.SuccessCheck{}, evs); sc.Download == nil || sc.Download.NameLike != "statement_*.csv" {
		t.Fatalf("success %+v", sc)
	}
	// A param from a navigation's query becomes a placeholder in its URL.
	s := WStep{Intent: "Open the 2026-08 statements", Action: computeruse.Action{Kind: "navigate", URL: evs[6].URL}}
	substitute(&s, paramCandidate{Event: 7, Kind: "month", Value: "2026-08", Key: "month"}, "month")
	if s.Action.URL != "https://bank.example.com/statements?month={month}" || s.Intent != "Open the {month} statements" || s.Action.Intent != s.Intent {
		t.Fatalf("step %+v", s)
	}
	s.Action.Intent = "x"
	if err := s.Action.Validate(); err != nil {
		t.Fatal(err)
	}
}
