package cc

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
)

var updateRevise = flag.Bool("update-revise", false, "rewrite testdata/revise_prompt.golden")

func reviseWorkflowFixture() TaughtWorkflow {
	return TaughtWorkflow{ID: "tw_1", HouseholdID: cbHH, OwnerID: 5, Name: "Download bank statement",
		Params: []Param{{Name: "month", Kind: "month", Example: "2026-09"}},
		Steps: []WStep{
			{Intent: "Open the bank", Target: "browser", Site: "bank.example", Goal: "Start at the bank",
				Action: computeruse.Action{Kind: "navigate", Intent: "Open the bank", URL: "https://bank.example/"},
				Expect: &computer.SuccessCheck{URLContains: "bank.example"}},
			{Intent: "Pick the account", Target: "browser", Site: "bank.example",
				Action:  computeruse.Action{Kind: "press", Intent: "Pick the account", Ref: computer.RecordedRef},
				Element: &RecordedElement{Role: "link", Name: "Checking"}, Expect: &computer.SuccessCheck{Screen: "The account page is open"}},
			{Intent: "Download the statement for {month}", Target: "browser", Site: "bank.example",
				Action:  computeruse.Action{Kind: "press", Intent: "Download the statement for {month}", Ref: computer.RecordedRef},
				Element: &RecordedElement{Role: "button", Name: "Download CSV"}, Expect: &computer.SuccessCheck{Download: &computer.DownloadCheck{NameLike: "statement*.csv"}}},
		},
		Success: computer.SuccessCheck{Download: &computer.DownloadCheck{NameLike: "statement*.csv"}},
		Skill: computer.Skill{Goal: computer.Goal{Reason: "Keep the records", EndState: "The statement for {month} is in Downloads"},
			Prerequisites: []computer.Prerequisite{{Kind: computer.PrereqSignedIn, Site: "bank.example"}},
			Notes:         []computer.Note{{Text: "The bank signs out after five minutes"}}},
	}
}

func reviseRunFixture() *computer.RunRecord {
	return &computer.RunRecord{RunID: "cr_1", State: computer.RecordWaiting,
		Ask: "I did step 2 (Pick the account), but I couldn't confirm it worked: The account page is open: no, it shows savings.",
		Items: []computer.RunItem{
			{Kind: "prerequisite", Text: "signed in to bank.example", State: computer.ItemDone},
			{Kind: "step", Text: "Open the bank", State: computer.ItemVerified},
			{Kind: "step", Text: "Pick the account", State: computer.ItemAsked, Seen: "it shows the savings account"},
			{Kind: "step", Text: "Download the statement for 2026-10", State: computer.ItemWaiting},
			{Kind: "result", Text: "The statement for 2026-10 is in Downloads", State: computer.ItemWaiting},
		}}
}

func TestRevisePromptGolden(t *testing.T) {
	got := revisePrompt(reviseWorkflowFixture(), reviseRunFixture(), "it picked the wrong account at step 2,\nit should be checking")
	path := filepath.Join("testdata", "revise_prompt.golden")
	if *updateRevise {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("revise prompt changed (rerun with -update-revise and review the diff):\n%s", got)
	}
}

// The model's edits land on the fields it named and nowhere else; what a step does on the
// computer is never the model's to change.
func TestApplyRevision(t *testing.T) {
	w := reviseWorkflowFixture()
	yes := true
	var out reviseOutput
	out.Summary = "Step 2 now names the checking account."
	out.Steps = append(out.Steps, struct {
		N        int    `json:"n"`
		Intent   string `json:"intent"`
		Goal     string `json:"goal"`
		Expect   string `json:"expect"`
		AskFirst *bool  `json:"ask_first"`
	}{N: 2, Intent: "Pick the checking account", Expect: "The checking account's page is open", AskFirst: &yes})
	out.Steps = append(out.Steps, out.Steps[0])
	out.Steps[1].N = 9 // no such step
	out.Notes = []string{"There are two accounts; this task is for checking", "The bank signs out after five minutes"}
	out.Prerequisites = []computer.Prerequisite{{Kind: computer.PrereqFileOpen, File: "budget-{month}.xlsx"}, {Kind: "teleport"}}
	draft, changes := applyRevision(w, out, "x")
	if got, want := strings.Join(changes, "; "), "Step 2: what it does, what to expect, asks first; A new thing to watch for; A new prerequisite"; got != want {
		t.Fatalf("changes: %s\nwant:    %s", got, want)
	}
	st := draft.Steps[1]
	if st.Intent != "Pick the checking account" || st.Action.Intent != st.Intent || st.Expect.Screen != "The checking account's page is open" || st.Risk == "" {
		t.Fatalf("step 2: %+v", st)
	}
	if st.Action.Kind != "press" || st.Element.Name != "Checking" || draft.Steps[2].Expect.Download == nil {
		t.Fatalf("what the steps do changed: %+v", draft.Steps)
	}
	if len(draft.Skill.Notes) != 2 || len(draft.Skill.Prerequisites) != 2 || draft.Skill.Prerequisites[1].File != "budget-{month}.xlsx" {
		t.Fatalf("skill: %+v", draft.Skill)
	}
	if w.Steps[1].Intent != "Pick the account" || len(w.Skill.Notes) != 1 {
		t.Fatal("the stored workflow was changed in place")
	}
	if err := draft.Validate(); err != nil {
		t.Fatal(err)
	}
}

// The route: only whoever taught the workflow, nothing stored, and a note when the model has
// nothing to change.
func TestReviseRoute(t *testing.T) {
	le := newLearnEnv(t, `{"summary": "Step 2 names the account.", "steps": [{"n": 2, "intent": "Pick the checking account"}]}`)
	stored, err := le.m.createWorkflow(context.Background(), reviseWorkflowFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := (taughtWorkflows{le.m}).Record(context.Background(), stored.ID, *reviseRunFixture()); err != nil {
		t.Fatal(err)
	}
	owner := bearer(le.member)
	admin := bearer(le.auth.addUser(6, cbHH, authn.RoleAdmin))
	outsider := bearer(le.auth.addUser(7, "hh-other", authn.RoleMember))
	url := wfBase + "/" + stored.ID + "/revise"
	say := map[string]any{"feedback": "it picked the wrong account at step 2"}

	got := le.do("POST", url, say, owner).want(200).json()
	draft := got["draft"].(map[string]any)
	if draft["steps"].([]any)[1].(map[string]any)["intent"] != "Pick the checking account" || got["summary"] != "Step 2 names the account." ||
		got["changes"].([]any)[0] != "Step 2: what it does" {
		t.Fatalf("draft: %v", got)
	}
	if p := le.model.userPrompt(t); !strings.Contains(p, `The owner's note: "it picked the wrong account at step 2"`) || !strings.Contains(p, "found: it shows the savings account") {
		t.Fatalf("the model wasn't shown the note and the run:\n%s", p)
	}
	if now, _ := le.m.getWorkflow(context.Background(), stored.ID); now.Version != stored.Version || now.Steps[1].Intent != "Pick the account" {
		t.Fatalf("revise stored something: v%d %q", now.Version, now.Steps[1].Intent)
	}
	// The draft saves as it came.
	le.do("PUT", wfBase+"/"+stored.ID, draft, owner).want(200)

	le.do("POST", url, say, admin).detail(403, "Only whoever taught a workflow can revise it from a run")
	le.do("POST", url, say, outsider).detail(404, "Workflow not found")
	le.do("POST", url, map[string]any{"feedback": "  "}, owner).detail(400, "Say what went wrong first")
	le.do("POST", url, map[string]any{"feedback": strings.Repeat("x", maxReviseFeedback+1)}, owner).want(400)

	// The model changes nothing: the owner's words are kept as a note.
	le.model.mu.Lock()
	le.model.reply = `{"summary": "nothing"}`
	le.model.mu.Unlock()
	got = le.do("POST", url, map[string]any{"feedback": "it felt slow"}, owner).want(200).json()
	notes := got["draft"].(map[string]any)["skill"].(map[string]any)["notes"].([]any)
	if last := notes[len(notes)-1].(map[string]any)["text"]; last != "it felt slow" || got["changes"].([]any)[0] != "A new thing to watch for" {
		t.Fatalf("fallback: %v", got)
	}
}
