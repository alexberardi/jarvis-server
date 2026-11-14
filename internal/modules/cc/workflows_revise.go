package cc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/computer"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Revising a workflow from what its owner says went wrong (docs/agent/WORKFLOW-SKILLS.md,
// verified-replay Q3a): the owner watches a run and writes a sentence; the model reads it with
// the workflow and the run's record and answers edits to named fields; the server applies them
// to a copy and hands that back as a draft, like Relearn's. Nothing is stored: the workflow
// changes only when its owner saves the draft. The model edits text the owner could edit on the
// page (a step's wording, goal and expectation, "ask me first", the end state, notes, a
// prerequisite); it can't add, drop or reorder steps, or change what a step does on the
// computer. Feedback it can't turn into an edit is proposed as a note.

const (
	maxReviseFeedback = 1000
	reviseMaxTokens   = 2000
)

const reviseSystemPrompt = `You improve a saved computer task from its owner's note about a run that went wrong. Answer only JSON.`

const reviseInstructions = `Decide which ONE thing the owner's note asks for, and answer with JSON holding only that. The note decides; the last run is background. Most answers have one key besides "summary".

The keys:
- "summary": one sentence saying what you changed.
- "steps": [{"n": <step number>, ...}] to change a step. Find the step the note is ABOUT by what the step does (a note about downloading is about the step that downloads), not the step that failed. In it give only what changes:
    "intent": what the step does, "goal": why, "expect": what is visible on the screen once it worked,
    "ask_first": true only when the owner wants to be asked before that step.
- "end_state": only when the owner says the finished task came out wrong.
- "notes": ["..."] for something to watch for that fits no step, or when the note doesn't say what to change.
- "prerequisites": [{"kind": "app_open|file_open|page_open|signed_in|other", ...}] only for something NEW that must be true before step 1.
Never repeat a note, a prerequisite or an end state that is already there. Never invent a step, a site or an app. Write a value that changes each run as its {name} from "Values".

Examples, for a task whose steps are "1. Open the shop | 2. Search for {item} | 3. Add it to the cart | 4. Pay":
note "it bought the wrong size, I wanted large" -> {"summary": "Step 3 now picks size large.", "steps": [{"n": 3, "intent": "Add the large size to the cart", "expect": "The cart shows the item in size large"}]}
note "check with me before paying" -> {"summary": "Step 4 asks first.", "steps": [{"n": 4, "ask_first": true}]}
note "my shopping list has to be open before it starts" -> {"summary": "The shopping list must be open first.", "prerequisites": [{"kind": "file_open", "file": "shopping list"}]}
note "the site was slow" -> {"summary": "Kept as a note.", "notes": ["The site can be slow"]}`

// reviseOutput is the model's answer.
type reviseOutput struct {
	Summary string `json:"summary"`
	Steps   []struct {
		N        int    `json:"n"`
		Intent   string `json:"intent"`
		Goal     string `json:"goal"`
		Expect   string `json:"expect"`
		AskFirst *bool  `json:"ask_first"`
	} `json:"steps"`
	EndState      string                  `json:"end_state"`
	Notes         []string                `json:"notes"`
	Prerequisites []computer.Prerequisite `json:"prerequisites"`
}

// revisePrompt is the workflow, its last run and the owner's note, as the model reads them.
func revisePrompt(w TaughtWorkflow, rec *computer.RunRecord, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The task: %q\n", w.Name)
	if g := w.Skill.Goal; g.Reason != "" || g.EndState != "" {
		fmt.Fprintf(&b, "Why: %s\nDone when: %s\n", orDefault(g.Reason, "(not written)"), orDefault(g.EndState, "(not written)"))
	}
	b.WriteString("Values: ")
	if len(w.Params) == 0 {
		b.WriteString("none")
	}
	for i, p := range w.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "{%s}", p.Name)
	}
	b.WriteString("\n\nBefore it starts:\n")
	if len(w.Skill.Prerequisites) == 0 {
		b.WriteString("(nothing)\n")
	}
	for _, p := range w.Skill.Prerequisites {
		fmt.Fprintf(&b, "- %s %s\n", p.Kind, strings.TrimSpace(strings.Join([]string{p.App, p.File, p.URL, p.Site, p.Text}, " ")))
	}
	b.WriteString("\nSteps:\n")
	for i, st := range w.Steps {
		fmt.Fprintf(&b, "%d. %s", i+1, oneLine(st.Intent))
		if st.Goal != "" {
			fmt.Fprintf(&b, " | goal: %s", oneLine(st.Goal))
		}
		if x := expectSentence(st.Expect); x != "" {
			fmt.Fprintf(&b, " | expect: %s", x)
		}
		if st.Risk != "" {
			b.WriteString(" | asks first")
		}
		b.WriteString("\n")
	}
	if len(w.Skill.Notes) > 0 {
		b.WriteString("\nNotes:\n")
		for _, n := range w.Skill.Notes {
			fmt.Fprintf(&b, "- %s\n", oneLine(n.Text))
		}
	}
	b.WriteString("\nThe last run:\n")
	switch {
	case rec == nil:
		b.WriteString("(no run is recorded)\n")
	default:
		fmt.Fprintf(&b, "It %s.", map[string]string{computer.RecordDone: "finished", computer.RecordFailed: "didn't finish",
			computer.RecordWaiting: "stopped to ask", computer.RecordStopped: "was stopped", computer.RecordRunning: "is still going"}[rec.State])
		if s := oneLine(firstNonEmptyStr(rec.Ask, rec.Summary)); s != "" {
			fmt.Fprintf(&b, " %s", truncateRunes(s, 400))
		}
		b.WriteString("\n")
		step := 0
		for _, it := range rec.Items {
			label := map[string]string{"prerequisite": "before", "result": "result"}[it.Kind]
			if it.Kind == "step" {
				step++
				label = fmt.Sprintf("step %d", step)
			}
			fmt.Fprintf(&b, "- %s: %s", label, it.State)
			if it.Seen != "" {
				fmt.Fprintf(&b, " (found: %s)", truncateRunes(oneLine(it.Seen), 200))
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\nThe owner's note: %q\n\n%s", truncateRunes(oneLine(feedback), maxReviseFeedback), reviseInstructions)
	return b.String()
}

// expectSentence is a step's expectation as the prompt shows it.
func expectSentence(c *computer.SuccessCheck) string {
	switch {
	case c == nil:
		return ""
	case c.Screen != "":
		return oneLine(c.Screen)
	case c.URLContains != "":
		return "the address contains " + c.URLContains
	case c.Download != nil:
		return "a file like " + c.Download.NameLike + " in Downloads"
	case len(c.TextPresent) > 0:
		return "the page shows " + strings.Join(c.TextPresent, ", ")
	}
	return ""
}

// applyRevision is w with the model's edits, and what changed, for the page to mark. An edit
// that names no step, repeats what is there or isn't valid is left out; with nothing left, the
// owner's own words become a note.
func applyRevision(w TaughtWorkflow, out reviseOutput, feedback string) (TaughtWorkflow, []string) {
	var changes []string
	steps := make([]WStep, len(w.Steps))
	copy(steps, w.Steps)
	w.Steps = steps
	for _, e := range out.Steps {
		if e.N < 1 || e.N > len(w.Steps) {
			continue
		}
		st := &w.Steps[e.N-1]
		var what []string
		if v := skillText(e.Intent, 300); v != "" && v != st.Intent {
			st.Intent, st.Action.Intent = v, v
			what = append(what, "what it does")
		}
		if v := skillText(e.Goal, computer.MaxStepGoal); v != "" && v != st.Goal {
			st.Goal = v
			what = append(what, "why")
		}
		// An expectation the recording proved (the page a step opened, a download) is checked on
		// the address or the disk, not the screen: a sentence can't replace it.
		proved := st.Expect != nil && st.Expect.Screen == "" && !st.Expect.Empty()
		if v := skillText(e.Expect, computer.MaxStepGoal); v != "" && v != expectSentence(st.Expect) && !proved {
			st.Expect = &computer.SuccessCheck{Screen: v}
			what = append(what, "what to expect")
		}
		if e.AskFirst != nil && *e.AskFirst && st.Risk == "" {
			st.Risk = computeruse.CatSubmitThirdParty // what the page's "Ask me first" sets
			what = append(what, "asks first")
		}
		if len(what) > 0 {
			changes = append(changes, fmt.Sprintf("Step %d: %s", e.N, strings.Join(what, ", ")))
		}
	}
	if v := skillText(out.EndState, computer.MaxSkillText); v != "" && v != w.Skill.Goal.EndState {
		w.Skill.Goal.EndState = v
		changes = append(changes, "It's done when")
	}
	notes := append([]computer.Note(nil), w.Skill.Notes...)
	addNote := func(text string) bool {
		text = skillText(text, computer.MaxSkillText)
		for _, n := range notes {
			if n.Text == text {
				return false
			}
		}
		if text == "" || len(notes) >= 50 {
			return false
		}
		notes = append(notes, computer.Note{Text: text})
		return true
	}
	for _, n := range out.Notes {
		if addNote(n) {
			changes = append(changes, "A new thing to watch for")
		}
	}
	prereqs := append([]computer.Prerequisite(nil), w.Skill.Prerequisites...)
	for _, p := range out.Prerequisites {
		p = onlyItsField(p)
		try := computer.Skill{Prerequisites: []computer.Prerequisite{p}}
		if try.Validate(w.Params) != nil || len(prereqs) >= 20 {
			continue
		}
		p = try.Prerequisites[0]
		dup := false
		for _, have := range prereqs {
			dup = dup || have == p
		}
		if !dup {
			prereqs = append(prereqs, p)
			changes = append(changes, "A new prerequisite")
		}
	}
	w.Skill.Notes, w.Skill.Prerequisites = notes, prereqs
	if w.Validate() != nil {
		changes = nil // an edit made it invalid (a {name} that is no value): fall back to the note
	}
	return w, changes
}

// reviseWorkflow asks the model for edits and applies them to a copy of w.
func (m *Module) reviseWorkflow(ctx context.Context, w TaughtWorkflow, rec *computer.RunRecord, feedback string) (TaughtWorkflow, []string, string, error) {
	if m.LLM == nil {
		return w, nil, "", fmt.Errorf("no llm module")
	}
	temp, maxTokens, think := 0.0, reviseMaxTokens, 0
	resp, err := m.LLM.Chat(ctx, llm.ChatRequest{
		Label:           m.featureSlot(ctx, SettingWorkflowsModel, llm.LabelBackground),
		Temperature:     &temp,
		MaxTokens:       &maxTokens,
		ReasoningBudget: &think,
		ResponseFormat:  &llm.ResponseFormat{Type: "json_object"},
		Messages: []llm.Message{
			{Role: "system", Content: llm.TextContent(reviseSystemPrompt)},
			{Role: "user", Content: llm.TextContent(revisePrompt(w, rec, feedback))},
		},
	})
	if err != nil {
		return w, nil, "", err
	}
	var out reviseOutput
	if i, j := strings.Index(resp.Content, "{"), strings.LastIndex(resp.Content, "}"); i >= 0 && j > i {
		_ = json.Unmarshal([]byte(resp.Content[i:j+1]), &out) // an unreadable answer proposes the note
	}
	draft, changes := applyRevision(w, out, feedback)
	if len(changes) == 0 {
		// Nothing usable: the owner's own words, as a note they can keep.
		draft = w
		draft.Skill.Notes = append(append([]computer.Note(nil), w.Skill.Notes...), computer.Note{Text: skillText(feedback, computer.MaxSkillText)})
		return draft, []string{"A new thing to watch for"}, "I couldn't tell which step to change, so I kept what you said as a note.", nil
	}
	return draft, changes, skillText(out.Summary, 300), nil
}

// handleReviseWorkflow proposes edits to a workflow from what its owner says went wrong, and
// answers the edited workflow in PUT's shape with what changed. Nothing is stored. Only the
// person who taught it may ask: the model is shown their last run, which is theirs alone
// (handleWorkflowLastRun), and the draft is theirs to save.
func (m *Module) handleReviseWorkflow(w http.ResponseWriter, r *http.Request, u authn.User) {
	wf, _, err := m.visibleWorkflow(r.Context(), u, r.PathValue("id"), true)
	if err != nil {
		m.writeErr(w, err)
		return
	}
	if wf.OwnerID != u.ID {
		detail(w, http.StatusForbidden, "Only whoever taught a workflow can revise it from a run")
		return
	}
	var in struct {
		Feedback string `json:"feedback"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflowBody))
	if err := dec.Decode(&in); err != nil {
		detail(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	feedback := strings.TrimSpace(in.Feedback)
	switch {
	case feedback == "":
		detail(w, http.StatusBadRequest, "Say what went wrong first")
		return
	case len([]rune(feedback)) > maxReviseFeedback:
		detail(w, http.StatusBadRequest, fmt.Sprintf("That is longer than %d characters", maxReviseFeedback))
		return
	}
	var raw string
	if err := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT last_run FROM cc_taught_workflows WHERE id = ?`, wf.ID).Scan(&raw); err != nil {
		m.writeErr(w, err)
		return
	}
	var rec *computer.RunRecord
	if raw != "" {
		rec = &computer.RunRecord{}
		if json.Unmarshal([]byte(raw), rec) != nil {
			rec = nil
		}
	}
	draft, changes, summary, err := m.reviseWorkflow(r.Context(), *wf, rec, feedback)
	if err != nil {
		m.deps.Log.Warn("cc: couldn't revise a workflow", "workflow", wf.ID, "err", err)
		detail(w, http.StatusServiceUnavailable, "Jarvis couldn't work on that just now. Try again in a moment")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"draft": workflowPut{Name: draft.Name, Triggers: draft.Triggers, Params: draft.Params, Steps: draft.Steps,
			Success: draft.Success, Shared: draft.Shared, Skill: &draft.Skill},
		"changes": changes, "summary": summary,
	})
}

// onlyItsField is p with the fields its kind doesn't use dropped (the model fills in extras), so
// a prerequisite that is already there is seen as the same.
func onlyItsField(p computer.Prerequisite) computer.Prerequisite {
	out := computer.Prerequisite{Kind: p.Kind}
	switch p.Kind {
	case computer.PrereqAppOpen:
		out.App = p.App
	case computer.PrereqFileOpen:
		out.File, out.App = p.File, p.App
	case computer.PrereqPageOpen:
		out.URL = p.URL
	case computer.PrereqSignedIn:
		out.Site = p.Site
	default:
		out.Text = p.Text
	}
	return out
}
