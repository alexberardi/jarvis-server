package errands

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
)

// The "Ran on its own" card (autonomy PLAN §1; blueprint Interfaces "Card metadata"): what an
// autorun errand did, step by step, and which rule allowed each tool. Mobile renders it from
// card_type autonomy_ran, so the metadata keys are a contract; old app builds still get a
// working Stop from interactive_elements.

const (
	// KindAutorun is the cc_workflows kind of a run the household's rules launched.
	KindAutorun = "errand_auto"
	// CategoryAutonomy is the "Ran on its own" card's inbox category.
	CategoryAutonomy = "autonomy"
	ranTitlePrefix   = "🤖 Ran on its own: "
)

// RanStep is one step on the card: status pending | ok | failed | skipped; Result is the step's
// message or error ("" while pending).
type RanStep struct {
	Label, Command, Status, Result string
}

// RanRule is what allowed one tool: Scope is household | watch | goal | automation | tool.
type RanRule struct {
	Tool, Effect, Scope, Note, Reason string
	RuleID                            *int64
}

// RanCard is the card's content. State is running | waiting | done | partial | failed | stopped.
// WorkflowID and PlanID are "" for an automation.
type RanCard struct {
	HouseholdID, State, WorkflowID, PlanID, SourceKind, SourceLabel string
	Revision                                                        int
	Steps                                                           []RanStep
	Rules                                                           []RanRule
}

// RanRuleOf is the card's line for the verdict that allowed tool.
func RanRuleOf(tool string, v policy.Verdict) RanRule {
	r := RanRule{Tool: tool, Effect: string(v.Effect), Scope: v.Scope, Reason: v.Reason}
	if v.Rule != nil {
		id := v.Rule.ID
		r.RuleID, r.Note = &id, v.Rule.Note
	}
	return r
}

// RanCardMetadata is the card's metadata (the mobile contract). Stop is offered only while the
// run is running or waiting.
func RanCardMetadata(c RanCard) map[string]any {
	steps := make([]any, len(c.Steps))
	for i, st := range c.Steps {
		steps[i] = map[string]any{"label": st.Label, "command": st.Command, "status": st.Status, "result": st.Result}
	}
	rules := make([]any, len(c.Rules))
	for i, r := range c.Rules {
		var id any
		if r.RuleID != nil {
			id = *r.RuleID
		}
		rules[i] = map[string]any{"tool": r.Tool, "effect": r.Effect, "scope": r.Scope, "rule_id": id, "note": r.Note,
			"reason": r.Reason}
	}
	md := map[string]any{
		"household_id": c.HouseholdID,
		"card_type":    "autonomy_ran",
		"state":        c.State,
		"source":       map[string]any{"kind": c.SourceKind, "label": c.SourceLabel},
		"steps":        steps,
		"rules":        rules,
	}
	if c.WorkflowID != "" {
		md["workflow_id"] = c.WorkflowID
	}
	if c.PlanID != "" {
		md["plan_id"] = c.PlanID
	}
	if c.State == "running" || c.State == "waiting" {
		md["interactive_elements"] = []any{
			element("stop-errand", "Stop", CallbackStop, map[string]any{"workflow_id": c.WorkflowID, "revision": c.Revision}),
		}
	}
	return md
}

var ranStepIcon = map[string]string{"ok": "✅", "failed": "⚠️", "skipped": "⏭️", "pending": "⏳"}

// RanCardBody is the card's markdown body (what an app without the structured layout shows).
func RanCardBody(c RanCard) string {
	lines := []string{"I ran this on its own because your household's rules allow it.", ""}
	for i, st := range c.Steps {
		line := fmt.Sprintf("%d. %s %s", i+1, ranStepIcon[st.Status], st.Label)
		if st.Result != "" {
			line += " — " + st.Result
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "Allowed by:")
	for _, r := range c.Rules {
		line := "- " + r.Tool + ": " + r.Reason
		if r.Note != "" {
			line += " — “" + r.Note + "”"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// ranCard builds the card for a run: its steps in order (paired with results in order; pending
// past the end) and, per distinct tool, the verdict that allows it now.
func (s *Service) ranCard(ctx context.Context, w *workflowRow, results []Result, state string) RanCard {
	c := RanCard{HouseholdID: w.HouseholdID, State: state, WorkflowID: w.ID, PlanID: s.planOf(ctx, w.ID),
		SourceKind: "errand", SourceLabel: "Errand", Revision: w.Revision, Steps: []RanStep{}, Rules: []RanRule{}}
	var steps []Step
	for _, st := range w.Steps {
		cmd := parse.PyStrip(st.Command)
		if cmd == "" {
			continue
		}
		st.Command = cmd
		steps = append(steps, st)
		rs := RanStep{Label: firstNonEmpty(st.Label, cmd), Command: cmd, Status: "pending"}
		if n := len(c.Steps); n < len(results) {
			r := results[n]
			switch skip, skipped := resultSkip(r); {
			case skipped:
				rs.Status, rs.Result = "skipped", skip
			case r.Success:
				rs.Status, rs.Result = "ok", r.Message
			default:
				rs.Status, rs.Result = "failed", firstNonEmpty(r.Error, "failed")
			}
		}
		c.Steps = append(c.Steps, rs)
	}
	verdicts := s.decideSteps(ctx, w.HouseholdID, steps, policy.SourceErrandStep)
	seen := map[string]bool{}
	for i, st := range steps {
		if seen[st.Command] || controlCommands[st.Command] {
			continue
		}
		seen[st.Command] = true
		c.Rules = append(c.Rules, RanRuleOf(st.Command, verdicts[i]))
	}
	return c
}

func resultSkip(r Result) (string, bool) {
	if r.Data == nil {
		return "", false
	}
	v, ok := r.Data.Get("skipped")
	if !ok {
		return "", false
	}
	reason, _ := v.(string)
	return reason, true
}

// planOf is the id of the plan that launched a run ("" when none).
func (s *Service) planOf(ctx context.Context, workflowID string) string {
	var id sql.NullString
	_ = s.DB.Read.QueryRowContext(ctx, `SELECT id FROM cc_errand_plans WHERE workflow_id = ?`, workflowID).Scan(&id)
	return id.String
}

var ranSummary = map[string]string{
	"running": "Running on its own.", "waiting": "Waiting on a step.", "done": "Done.",
	"partial": "Finished with issues.", "failed": "Couldn't finish.", "stopped": "Stopped.",
}

// updateRanCard rewrites the run's card in place (nothing when the gate withheld it). summary ""
// is the state's default.
func (s *Service) updateRanCard(ctx context.Context, w *workflowRow, results []Result, state, summary string) {
	if w.InboxItemID == "" {
		return
	}
	s.putRanCard(ctx, w, s.ranCard(ctx, w, results, state), summary)
}

func (s *Service) putRanCard(ctx context.Context, w *workflowRow, c RanCard, summary string) {
	if s.Cards == nil || w.InboxItemID == "" {
		return
	}
	s.Cards.Update(ctx, w.InboxItemID, s.ranCardOf(w, c, summary))
}

func (s *Service) ranCardOf(w *workflowRow, c RanCard, summary string) Card {
	if summary == "" {
		summary = ranSummary[c.State]
	}
	return Card{
		HouseholdID: w.HouseholdID, UserID: w.UserID, Title: ranTitlePrefix + w.DisplayTitle(), Summary: summary,
		Body: RanCardBody(c), Category: CategoryAutonomy, Metadata: RanCardMetadata(c), Push: true,
		TargetType: targetType(w.UserID),
	}
}

// ranState is the card state for a finished run's aggregate.
func ranState(a Aggregate) string {
	switch a.Status {
	case "success":
		return "done"
	case "partial", "stopped":
		return a.Status
	}
	return "failed"
}

// recordStopped logs a member's Stop of an autorun run.
func (s *Service) recordStopped(ctx context.Context, w *workflowRow, by *int64) {
	s.record(ctx, policy.Event{HouseholdID: w.HouseholdID, UserID: by, Source: policy.SourceErrandPlan,
		SourceID: s.planOf(ctx, w.ID), Effect: policy.Allow, Outcome: policy.OutcomeStopped,
		Reason: "stopped by a household member", Detail: map[string]any{"workflow_id": w.ID}})
}
