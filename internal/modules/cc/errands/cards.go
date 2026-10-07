package errands

import (
	"context"
	"fmt"
	"strings"
)

// Cards (docs/cc/09 §3.3): the plan card, the mid-run delta card, the completion card and the
// couldn't-plan card. Mobile renders them generically, so the metadata keys are a contract:
// household_id (copied into POST /callbacks), target:"server", the instruction seeding only on
// Revise, and no editor_schema (more than two editable fields disables the buttons).

const (
	callbackCommand = "errand"

	CallbackApprove      = "approve_errand_plan"
	CallbackRefine       = "refine_errand_plan"
	CallbackDiscard      = "discard_errand_plan"
	CallbackApproveRepl  = "approve_replan"
	CallbackStop         = "stop_errand"
	planCategory         = "errand_plan"
	completionCategory   = "errand"
	planTitlePrefix      = "🗒️ Errand plan: "
	replanTitlePrefix    = "🔀 Errand update: "
	couldntPlanCardTitle = "🗒️ Couldn't plan that errand"
)

// CallbackCommand is the command name every errand card button targets.
const CallbackCommand = callbackCommand

func targetType(uid *int64) string {
	if uid != nil {
		return "user"
	}
	return "household"
}

func stepLabel(s Step) string {
	if s.Label != "" {
		return s.Label
	}
	return s.Command
}

// planCardBody is _plan_card_body.
func planCardBody(summary string, steps []Step) string {
	lines := []string{summary, "", "**Plan:**"}
	for i, st := range steps {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, stepLabel(st)))
	}
	lines = append(lines, "",
		"Tap **Run** to start it. To change it, tell me what to change above and "+
			"tap **Revise**, or tap **Cancel** to discard.")
	return strings.Join(lines, "\n")
}

func element(id, label, callback string, data map[string]any) map[string]any {
	return map[string]any{
		"id": id, "label": label, "command": callbackCommand, "callback": callback, "target": "server", "data": data,
	}
}

// PlanCardMetadata is build_plan_card_metadata.
func PlanCardMetadata(planID string, revision int, steps []Step, hh string) map[string]any {
	ident := func() map[string]any { return map[string]any{"plan_id": planID, "revision": revision} }
	revise := ident()
	revise["instruction"] = ""
	return map[string]any{
		"household_id": hh,
		"plan_id":      planID,
		"revision":     revision,
		"steps":        stepsOrEmpty(steps),
		"editable_fields": []any{map[string]any{
			"label": "Tell me what to change", "initial": "", "data_key": "instruction",
			"input_type": "text", "required": true,
		}},
		"interactive_elements": []any{
			element("run-errand", "Run", CallbackApprove, ident()),
			element("revise-errand", "Revise", CallbackRefine, revise),
			element("cancel-errand", "Cancel", CallbackDiscard, ident()),
		},
	}
}

func stepsOrEmpty(steps []Step) []Step {
	if steps == nil {
		return []Step{}
	}
	return steps
}

// postPlanCard is _post_errand_plan_card: update the plan's card in place when it has one,
// else post a new card. Returns the card id ("" on failure; best-effort).
func (s *Service) postPlanCard(ctx context.Context, p *planRow) string {
	if s.Cards == nil {
		return ""
	}
	summary := firstNonEmpty(p.Summary, p.Goal)
	c := Card{
		HouseholdID: p.HouseholdID, UserID: p.UserID, Title: planTitlePrefix + summary, Summary: summary,
		Body: planCardBody(summary, p.Steps), Category: planCategory,
		Metadata: PlanCardMetadata(p.ID, p.Revision, p.Steps, p.HouseholdID), Push: true, TargetType: targetType(p.UserID),
	}
	if p.InboxItemID != "" && s.Cards.Update(ctx, p.InboxItemID, c) {
		return p.InboxItemID
	}
	return s.Cards.Post(ctx, c)
}

// replanCardBody is _replan_card_body.
func replanCardBody(title, reason string, add []Step) string {
	lines := []string{"While running **" + title + "**, I have a change to propose:", ""}
	if reason != "" {
		lines = append(lines, reason, "")
	}
	lines = append(lines, "**Proposed additional steps:**")
	for i, st := range add {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, stepLabel(st)))
	}
	lines = append(lines, "", "Tap **Approve** to include these and continue, or **Stop** to end the errand here.")
	return strings.Join(lines, "\n")
}

// ReplanCardMetadata is build_replan_card_metadata.
func ReplanCardMetadata(workflowID string, revision int, add []Step, hh string) map[string]any {
	ident := func() map[string]any { return map[string]any{"workflow_id": workflowID, "revision": revision} }
	return map[string]any{
		"household_id": hh,
		"workflow_id":  workflowID,
		"revision":     revision,
		"steps":        stepsOrEmpty(add),
		"interactive_elements": []any{
			element("approve-replan", "Approve", CallbackApproveRepl, ident()),
			element("stop-errand", "Stop", CallbackStop, ident()),
		},
	}
}

// postReplanCard is _post_replan_card: post (or update in place) the delta card and store its
// id on the run.
func (s *Service) postReplanCard(ctx context.Context, w *workflowRow, reason string, add []Step) {
	if s.Cards == nil {
		return
	}
	title := w.DisplayTitle()
	summary := reason
	if summary == "" {
		summary = "I have a change to propose."
	}
	c := Card{
		HouseholdID: w.HouseholdID, UserID: w.UserID, Title: replanTitlePrefix + title, Summary: summary,
		Body: replanCardBody(title, reason, add), Category: planCategory,
		Metadata: ReplanCardMetadata(w.ID, w.Revision, add, w.HouseholdID), Push: true, TargetType: targetType(w.UserID),
	}
	id := ""
	if w.InboxItemID != "" && s.Cards.Update(ctx, w.InboxItemID, c) {
		id = w.InboxItemID
	} else {
		id = s.Cards.Post(ctx, c)
	}
	if id != "" && id != w.InboxItemID {
		if _, err := s.DB.Write.ExecContext(ctx, `UPDATE cc_workflows SET inbox_item_id = ?, updated_at = ? WHERE id = ?`,
			id, dbTime(s.now()), w.ID); err != nil {
			s.log().Warn("errands: store delta card id", "workflow", w.ID, "err", err)
		}
	}
}

// statusCard is _ERRAND_STATUS_CARD: (icon, headline) per terminal status.
var statusCard = map[string][2]string{
	"success": {"✅", "done"},
	"partial": {"⚠️", "finished with issues"},
	"failed":  {"⚠️", "couldn't finish"},
	"timeout": {"⏱️", "didn't finish"},
}

// postCompletionCard is _post_errand_completion_card: ONE honest card per finished errand, no
// buttons.
func (s *Service) postCompletionCard(ctx context.Context, hh, title string, agg Aggregate, uid *int64) {
	if s.Cards == nil {
		return
	}
	status := agg.Status
	if status == "" {
		status = "failed"
	}
	ih, ok := statusCard[status]
	if !ok {
		ih = statusCard["failed"]
	}
	summary := agg.Message
	if summary == "" {
		summary = "The errand finished."
	}
	s.Cards.Post(ctx, Card{
		HouseholdID: hh, UserID: uid, Title: ih[0] + " Errand " + ih[1] + ": " + title, Summary: summary, Body: summary,
		Category: completionCategory, Metadata: map[string]any{"household_id": hh, "status": status},
		Push: true, TargetType: targetType(uid),
	})
}

// postCouldntPlanCard is _post_couldnt_plan_card (never-vanish: the voice ack promised a card).
func (s *Service) postCouldntPlanCard(ctx context.Context, hh string, uid *int64, summary, body string) {
	if s.Cards == nil {
		return
	}
	s.Cards.Post(ctx, Card{
		HouseholdID: hh, UserID: uid, Title: couldntPlanCardTitle, Summary: summary, Body: body,
		Category: planCategory, Metadata: map[string]any{"household_id": hh}, Push: true, TargetType: targetType(uid),
	})
}

// expirePlanCard rewrites an expired draft's card in place with no buttons (D40 09.Q6: new in
// the port; legacy drafts never expired).
func (s *Service) expirePlanCard(ctx context.Context, p *planRow) {
	if s.Cards == nil || p.InboxItemID == "" {
		return
	}
	summary := firstNonEmpty(p.Summary, p.Goal)
	s.Cards.Update(ctx, p.InboxItemID, Card{
		HouseholdID: p.HouseholdID, UserID: p.UserID, Title: "🗒️ Errand plan expired: " + summary,
		Summary:  summary,
		Body:     summary + "\n\nThis plan expired before it was run. Ask me again if you still want it done.",
		Category: planCategory,
		Metadata: map[string]any{"household_id": p.HouseholdID, "plan_id": p.ID, "revision": p.Revision,
			"steps": stepsOrEmpty(p.Steps), "state": "expired"},
	})
}

// inbox builds a callback reply's context_data.inbox.
func inbox(hh, title, summary string) map[string]any {
	return map[string]any{"inbox": map[string]any{
		"title": title, "summary": summary, "metadata": map[string]any{"household_id": hh},
	}}
}
