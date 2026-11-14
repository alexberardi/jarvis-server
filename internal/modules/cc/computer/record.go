package computer

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A workflow's last run, as its page shows it (docs/agent/WORKFLOW-SKILLS.md, verified-replay
// Q3): each prerequisite, each step and the result, with what was expected and, where a check
// failed, what was seen. It is rebuilt from the run's history after every step and at every
// end of a call, so the page can follow a run as it goes. It holds what the run looked for and
// found, as text; never a screenshot, and never a value the recorder masked.

// Run record states.
const (
	RecordRunning = "running"
	RecordWaiting = "waiting" // on the user: a question or a confirmation
	RecordDone    = "done"
	RecordFailed  = "failed"
	RecordStopped = "stopped"
)

// Item states.
const (
	ItemWaiting  = "waiting"  // not reached yet
	ItemDone     = "done"     // done, with nothing to check or on someone's word
	ItemVerified = "verified" // done and its check passed
	ItemAsked    = "asked"    // the run is asking about it
	ItemFailed   = "failed"
)

// RunRecord is a workflow's last run.
type RunRecord struct {
	RunID     string            `json:"run_id"`
	State     string            `json:"state"`
	Params    map[string]string `json:"params,omitempty"`
	Summary   string            `json:"summary,omitempty"` // how it ended
	Ask       string            `json:"ask,omitempty"`     // what it is asking the user
	StartedAt time.Time         `json:"started_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Items     []RunItem         `json:"items"`
}

// RunItem is one prerequisite, step or the result.
type RunItem struct {
	Kind     string `json:"kind"` // prerequisite | step | result
	Text     string `json:"text"`
	State    string `json:"state"`
	Expected string `json:"expected,omitempty"`
	Seen     string `json:"seen,omitempty"` // what a failed check found
}

// recordRun stores wf's run as it stands. out is nil while the run is going.
func (l *Loop) recordRun(ctx context.Context, wf *Workflow, steps []WorkflowStep, params map[string]string, run *Run, out *Outcome) {
	if l.Workflows == nil || run == nil || wf.ID == "" {
		return
	}
	_ = l.Workflows.Record(ctx, wf.ID, buildRecord(wf, steps, params, run, out, l.now()))
}

func buildRecord(wf *Workflow, steps []WorkflowStep, params map[string]string, run *Run, out *Outcome, now time.Time) RunRecord {
	rec := RunRecord{RunID: run.ID, State: RecordRunning, Params: params, StartedAt: run.CreatedAt, UpdatedAt: now}
	if out != nil {
		switch out.State {
		case StateDone:
			rec.State, rec.Summary = RecordDone, out.Summary
		case StateAsk, StateNeedsApproval, StatePaused:
			rec.State, rec.Ask = RecordWaiting, firstNonEmpty(out.Ask, "Waiting for you to approve the next action.")
		case StateStopped:
			rec.State, rec.Summary = RecordStopped, out.Summary
		default:
			rec.State, rec.Summary = RecordFailed, out.Summary
		}
	}
	bind := func(s string) string { return wf.BindText(s, params) }
	for i, p := range wf.Skill.Prerequisites {
		it := RunItem{Kind: "prerequisite", Text: bind(prereqSentence(p)), State: ItemWaiting}
		switch state, rest := lastPrereq(run.History, fmt.Sprintf("p%d", i+1)); state {
		case "ok":
			it.State = ItemDone
		case "asked":
			it.State, it.Seen = ItemAsked, rest
		}
		rec.Items = append(rec.Items, it)
	}
	done := stepsDone(run.History)
	for i, st := range steps {
		it := RunItem{Kind: "step", Text: oneLine(st.Intent), State: ItemWaiting, Expected: expectText(st.Expect)}
		seen := lastCheck(run.History, i+1)
		switch {
		case i < done && seen == "" && it.Expected != "":
			it.State = ItemVerified
		case i < done:
			it.State, it.Seen = ItemDone, seen // nothing to check, or carried on past a failed check
		case i == done && seen != "":
			it.State, it.Seen = ItemAsked, seen
		case i == done && rec.State == RecordFailed:
			it.State, it.Seen = ItemFailed, rec.Summary
		}
		rec.Items = append(rec.Items, it)
	}
	if len(steps) > 0 {
		check := wf.Success.with(bind)
		it := RunItem{Kind: "result", Text: firstNonEmpty(oneLine(bind(wf.Skill.Goal.EndState)), "The result"), State: ItemWaiting,
			Expected: expectText(&check)}
		switch {
		case rec.State == RecordDone && out.Checked != "":
			it.State = ItemVerified
		case rec.State == RecordDone:
			it.State, it.Seen = ItemDone, rec.Summary
		case done >= len(steps) && rec.State == RecordWaiting:
			it.State, it.Seen = ItemAsked, rec.Ask
		case done >= len(steps) && rec.State == RecordFailed:
			it.State, it.Seen = ItemFailed, rec.Summary
		}
		rec.Items = append(rec.Items, it)
	}
	return rec
}

// lastPrereq is prerequisite ref's last state ("ok", "asked", "") and what followed it.
func lastPrereq(h []Entry, ref string) (state, rest string) {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Kind == entryPrereq && h[i].Ref == ref {
			state, rest, _ = strings.Cut(h[i].Result, ":")
			return state, strings.TrimSpace(rest)
		}
	}
	return "", ""
}

// lastCheck is what step wstep's last failed check found ("" when none failed).
func lastCheck(h []Entry, wstep int) string {
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Kind == entryCheck && h[i].WStep == wstep {
			return strings.TrimSpace(strings.TrimPrefix(h[i].Result, "unverified:"))
		}
	}
	return ""
}

// expectText is a check as a person reads it ("" for none).
func expectText(c *SuccessCheck) string {
	if c == nil || c.empty() {
		return ""
	}
	var parts []string
	if c.URLContains != "" {
		parts = append(parts, fmt.Sprintf("the address contains %q", c.URLContains))
	}
	for _, t := range c.TextPresent {
		if strings.TrimSpace(t) != "" {
			parts = append(parts, fmt.Sprintf("the page shows %q", t))
		}
	}
	if c.Download != nil {
		parts = append(parts, fmt.Sprintf("a file like %q is in Downloads", c.Download.NameLike))
	}
	if c.File != nil {
		parts = append(parts, "the file "+c.File.Path+" is there")
	}
	if s := oneLine(c.Screen); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
