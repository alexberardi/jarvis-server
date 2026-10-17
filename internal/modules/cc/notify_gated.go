package cc

import "context"

// gatedNote is one notification jarvisd originates itself (watch alerts, autorun reports).
type gatedNote struct {
	HouseholdID string
	UserID      *int64 // nil: the household

	Source, Category, Title, Summary, Body string

	Metadata  map[string]any
	DedupeKey string
	Push      bool
}

// notifyGated sends n through the attention broker (dedupe, budgets, quiet hours) when the
// household has it on, then posts the inbox item and its push (autonomy PLAN §0.2). It returns
// the inbox item id, or "" and the gate that withheld it.
func (m *Module) notifyGated(ctx context.Context, n gatedNote) (string, string) {
	targetType := "household"
	if n.UserID != nil {
		targetType = "user"
	}
	rung := "inbox"
	if n.Push {
		rung = "push"
	}
	d, gated := m.attentionGate(ctx, AttentionRequest{HouseholdID: n.HouseholdID, Source: n.Source, Category: n.Category,
		Title: n.Title, Summary: n.Summary, RequestedRung: rung, DedupeKey: n.DedupeKey, TargetUserID: n.UserID, Payload: n.Metadata})
	if gated && !d.Deliver {
		return "", d.WithheldBy
	}
	push := n.Push
	if gated {
		push = d.Rung == "push"
	}
	id := m.postInboxItem(ctx, n.HouseholdID, n.UserID, n.Title, n.Summary, n.Body, n.Category, n.Metadata, push, targetType)
	m.attentionOutcome(ctx, d, gated, id != "", id)
	return id, ""
}
