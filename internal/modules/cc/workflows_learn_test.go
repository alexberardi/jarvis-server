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

// secretPassword is a value the recorder should never have sent for the masked password field;
// bankTraceWithSecret puts it there, to show that it never reaches the model.
const secretPassword = "hunter2-do-not-send"

func bankTraceWithSecret(t *testing.T) json.RawMessage {
	t.Helper()
	return replaceMasked(t, `"masked":true,"value":"`+secretPassword+`"}`)
}

// bankTraceUnflagged is the bank trace with the password field's masked flag missing and its
// value sent, as a recorder that forgot to mask it would send it.
func bankTraceUnflagged(t *testing.T) json.RawMessage {
	t.Helper()
	return replaceMasked(t, `"value":"`+secretPassword+`"}`)
}

func replaceMasked(t *testing.T, with string) json.RawMessage {
	t.Helper()
	tr := bankTrace(t)
	out := bytes.Replace(tr, []byte(`"masked":true}`), []byte(with), 1)
	if bytes.Equal(out, tr) {
		t.Fatal("no masked event in the bank trace")
	}
	return out
}

// bankModel is what a model answers for the bank trace (its navigations after Sign in and
// Statements folded into those clicks): an invented event (42) and an invented param, and a
// param of an unknown kind on a real candidate (event 6), which the job drops.
const bankModel = `{"name": "Download bank statement", "triggers": ["download my bank statement", " "],
 "steps": [
  {"intent": "Open the bank's sign-in page", "event_seqs": [1], "target": "the sign-in page"},
  {"intent": "Enter the username", "event_seqs": [2], "target": "Username"},
  {"intent": "Enter the password", "event_seqs": [3], "target": "Password"},
  {"intent": "Sign in", "event_seqs": [4], "target": "Sign in"},
  {"intent": "Open the statements", "event_seqs": [5], "target": "Statements"},
  {"intent": "Enter the month {month}", "event_seqs": [6], "target": "Statement month"},
  {"intent": "Download the CSV", "event_seqs": [7, 8], "target": "Download CSV"},
  {"intent": "Log out", "event_seqs": [42], "target": "Log out"}
 ],
 "params": [
  {"name": "month", "kind": "month", "example": "2026-09", "from_seqs": [5, 6]},
  {"name": "account", "kind": "text", "example": "checking", "from_seqs": [42]},
  {"name": "statement_month", "kind": "colour", "example": "2026-09", "from_seqs": [6]}
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
	w := le.store("rec_bank1", bankTraceWithSecret(t), "this is how I get my bank statement")

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
	if len(reqs) != 1 || reqs[0].Label != llm.LabelBackground || len(reqs[0].Messages) != 2 {
		t.Fatalf("model calls %+v", reqs)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "learn_prompt.golden"))
	if err != nil {
		t.Fatal(err)
	}
	_, wantUser, ok := strings.Cut(string(golden), "\n\nUSER:\n")
	if !ok {
		t.Fatal("no USER part in learn_prompt.golden")
	}
	sys, user := reqs[0].Messages[0], reqs[0].Messages[1]
	if sys.Role != "system" || sys.Content.Text == nil || *sys.Content.Text != learnSystemPrompt {
		t.Fatalf("system message %+v", sys)
	}
	if user.Role != "user" || user.Content.Text == nil || *user.Content.Text != strings.TrimSuffix(wantUser, "\n") {
		t.Fatalf("user message differs from the golden's USER part:\n%+v", user)
	}
	if raw, err := json.Marshal(reqs[0]); err != nil || bytes.Contains(raw, []byte(secretPassword)) {
		t.Fatalf("the masked password reached the model (err %v)", err)
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

// TestLearnUnflaggedPassword: a password field's value is dropped even when the recorder
// didn't mark it masked.
func TestLearnUnflaggedPassword(t *testing.T) {
	le := newLearnEnv(t, bankModel)
	w := le.store("rec_bank1", bankTraceUnflagged(t), "this is how I get my bank statement")

	le.model.mu.Lock()
	reqs := slices.Clone(le.model.reqs)
	le.model.mu.Unlock()
	if len(reqs) != 1 || len(reqs[0].Messages) != 2 {
		t.Fatalf("model calls %+v", reqs)
	}
	if raw, err := json.Marshal(reqs[0]); err != nil || bytes.Contains(raw, []byte(secretPassword)) {
		t.Fatalf("the unflagged password reached the model (err %v)", err)
	}
	if user := reqs[0].Messages[1].Content.Text; user == nil || !strings.Contains(*user, `type into textbox "Password": •••`) {
		t.Fatalf("the password isn't shown as •••:\n%v", user)
	}
	if raw, err := json.Marshal(w); err != nil || bytes.Contains(raw, []byte(secretPassword)) {
		t.Fatalf("the unflagged password is in the stored workflow (err %v)", err)
	}
	if s := w.Steps[2]; s.Intent != "Enter the password" || s.Action.Text != maskedValue || s.Risk != computeruse.CatSensitiveInput {
		t.Fatalf("password step %+v", s)
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
	s := WStep{Intent: "Open the 2026-08 statements", Action: computeruse.Action{Kind: "navigate", URL: "https://bank.example.com/statements?month=2026-08"}}
	substitute(&s, paramCandidate{Event: 7, Kind: "month", Value: "2026-08", Key: "month"}, "month")
	if s.Action.URL != "https://bank.example.com/statements?month={month}" || s.Intent != "Open the {month} statements" || s.Action.Intent != s.Intent {
		t.Fatalf("step %+v", s)
	}
	s.Action.Intent = "x"
	if err := s.Action.Validate(); err != nil {
		t.Fatal(err)
	}
}

// shortTrace is parseTrace's events as one line each: "kind url → result" (tab, when not 0).
func shortTrace(t *testing.T, raw string) []string {
	t.Helper()
	evs, err := parseTrace(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		s := e.Kind
		if e.Element != nil {
			s += " " + e.Element.Name
		}
		if e.Kind == "navigate" {
			s += " " + e.URL
		}
		if e.ResultURL != "" {
			s += " → " + e.ResultURL
		}
		out = append(out, s)
	}
	return out
}

// TestFoldNavigationsReloads: rec_c69371244277e2ca (2026-10-19), recorded without access to
// websites, was 15 navigations and nothing else; the repeats (reloads, a tab's URL reported
// again) aren't steps.
func TestFoldNavigationsReloads(t *testing.T) {
	var lines []string
	for i, n := range []struct {
		tab int
		url string
	}{
		{3, "http://127.0.0.1:7790/"}, {3, "http://127.0.0.1:7790/"},
		{4, "http://localhost:7710/"}, {4, "http://localhost:7710/"}, {4, "http://localhost:7710/"},
		{4, "http://localhost:7710/chat"}, {4, "http://localhost:7710/chat"}, {4, "http://localhost:7710/chat"},
		{3, "http://127.0.0.1:7790/"},
		{4, "http://localhost:7710/recordings"}, {4, "http://localhost:7710/recordings"},
		{4, "http://localhost:7710/"}, {4, "http://localhost:7710/"}, {4, "http://localhost:7710/"},
		{4, "http://localhost:7710/"},
	} {
		lines = append(lines, fmt.Sprintf(`{"seq":%d,"ts":%d,"kind":"navigate","tab":%d,"url":%q}`, i+1, 1760900000000+int64(i)*3000, n.tab, n.url))
	}
	got := shortTrace(t, "["+strings.Join(lines, ",")+"]")
	want := []string{"navigate http://127.0.0.1:7790/", "navigate http://localhost:7710/", "navigate http://localhost:7710/chat",
		"navigate http://localhost:7710/recordings", "navigate http://localhost:7710/"}
	if !slices.Equal(got, want) {
		t.Fatalf("events\n got %q\nwant %q", got, want)
	}
}

// TestFoldNavigationsIntoActions: a navigation right after a click, submit or key in its tab is
// what that action did, not a page the user opened.
func TestFoldNavigationsIntoActions(t *testing.T) {
	const u = "http://localhost:7710"
	for _, c := range []struct {
		name, trace string
		want        []string
	}{
		{"click then pushState", `[
			{"seq":1,"ts":1000,"tab":4,"kind":"navigate","url":"` + u + `/"},
			{"seq":2,"ts":5000,"tab":4,"kind":"click","url":"` + u + `/","element":{"role":"link","name":"Add-ons"}},
			{"seq":3,"ts":5300,"tab":4,"kind":"navigate","url":"` + u + `/addons"},
			{"seq":4,"ts":9000,"tab":4,"kind":"click","url":"` + u + `/addons","element":{"role":"link","name":"Chat"}},
			{"seq":5,"ts":9100,"tab":4,"kind":"navigate","url":"` + u + `/chat"},
			{"seq":6,"ts":9200,"tab":4,"kind":"navigate","url":"` + u + `/chat"}]`,
			[]string{"navigate " + u + "/", "click Add-ons → " + u + "/addons", "click Chat → " + u + "/chat"}},
		{"redirects end where the click went", `[
			{"seq":1,"ts":5000,"tab":4,"kind":"click","url":"` + u + `/","element":{"role":"button","name":"Sign in"}},
			{"seq":2,"ts":5400,"tab":4,"kind":"navigate","url":"` + u + `/login"},
			{"seq":3,"ts":5900,"tab":4,"kind":"navigate","url":"` + u + `/home"}]`,
			[]string{"click Sign in → " + u + "/home"}},
		{"the navigation reported before the click", `[
			{"seq":1,"ts":5300,"tab":4,"kind":"navigate","url":"` + u + `/addons"},
			{"seq":2,"ts":5000,"tab":4,"kind":"click","url":"` + u + `/","element":{"role":"link","name":"Add-ons"}}]`,
			[]string{"click Add-ons → " + u + "/addons"}},
		{"Enter in a search box", `[
			{"seq":1,"ts":5000,"tab":4,"kind":"change","url":"` + u + `/","element":{"role":"textbox","name":"Search"},"value":"x"},
			{"seq":2,"ts":5100,"tab":4,"kind":"key","key":"Enter","url":"` + u + `/","element":{"role":"textbox","name":"Search"}},
			{"seq":3,"ts":5600,"tab":4,"kind":"navigate","url":"` + u + `/search?q=x"}]`,
			[]string{"change Search", "key Search → " + u + "/search?q=x"}},
		// Headless Firefox (lane-events.jsonl): the form's pushState came in before the typing,
		// Enter and submit that caused it.
		{"Enter in a chat box, the navigation first", `[
			{"seq":6,"ts":67775,"tab":1,"kind":"click","url":"` + u + `/chat","element":{"role":"textbox","name":"Message"}},
			{"seq":7,"ts":68141,"tab":1,"kind":"navigate","url":"` + u + `/chat?sent=1"},
			{"seq":8,"ts":68136,"tab":1,"kind":"input","url":"` + u + `/chat","element":{"role":"textbox","name":"Message"},"value":"what time is it?"},
			{"seq":9,"ts":68136,"tab":1,"kind":"key","key":"Enter","url":"` + u + `/chat","element":{"role":"textbox","name":"Message"}},
			{"seq":10,"ts":68138,"tab":1,"kind":"submit","url":"` + u + `/chat","element":{"role":"form","name":"f"}},
			{"seq":11,"ts":69649,"tab":1,"kind":"click","url":"` + u + `/chat?sent=1","element":{"role":"button","name":"Switch to light mode"}}]`,
			[]string{"click Message", "input Message", "key Message", "submit f → " + u + "/chat?sent=1", "click Switch to light mode"}},
		{"too late, or in another tab: opened by the user", `[
			{"seq":1,"ts":5000,"tab":4,"kind":"click","url":"` + u + `/","element":{"role":"button","name":"Switch to light mode"}},
			{"seq":2,"ts":5200,"tab":9,"kind":"navigate","url":"https://example.com/"},
			{"seq":3,"ts":9000,"tab":4,"kind":"navigate","url":"` + u + `/settings"}]`,
			[]string{"click Switch to light mode", "navigate https://example.com/", "navigate " + u + "/settings"}},
		{"an old trace without times: right after the action", `[
			{"seq":1,"kind":"click","url":"` + u + `/","element":{"role":"link","name":"Chat"}},
			{"seq":2,"kind":"navigate","url":"` + u + `/chat"},
			{"seq":3,"kind":"change","url":"` + u + `/chat","element":{"role":"textbox","name":"Message"},"value":"hi"},
			{"seq":4,"kind":"navigate","url":"` + u + `/"}]`,
			[]string{"click Chat → " + u + "/chat", "change Message", "navigate " + u + "/"}},
	} {
		if got := shortTrace(t, c.trace); !slices.Equal(got, c.want) {
			t.Errorf("%s\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// The model sees where a click went.
func TestDescribeFoldedClick(t *testing.T) {
	e := traceEvent{Kind: "click", Element: &traceElement{Role: "link", Name: "Chat"}, ResultURL: "http://localhost:7710/chat"}
	if got := describeEvent(e); got != `click link "Chat" (opens http://localhost:7710/chat)` {
		t.Fatalf("got %s", got)
	}
}
