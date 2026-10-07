package errands

import (
	"context"
	"time"
)

// Hooks for sibling subsystems that land in parallel (phone, doc 11; schedules, doc 08). Each
// is an interface field on Service; nil means the feature is unavailable.

// ErrandCall is one workflow-linked call to draft (create_call_plan with errand_id/errand_step).
type ErrandCall struct {
	HouseholdID string
	UserID      *int64
	Business    string
	Goal        string
	WorkflowID  string
	Step        int
	// PriorContext summarizes the errand's earlier steps for the call brief ("" when none).
	PriorContext string
}

// CallOutcome is a phone session's state as the errand sees it (the resume snapshot).
type CallOutcome struct {
	SessionID  string
	WorkflowID string // the session's errand_id (a cc_workflows id)
	Step       int    // the session's errand_step
	// State is the session state; done | failed | declined | expired are terminal.
	State        string
	ContactName  string
	ErrorMessage string
	// OutcomeJSON is the call's wrap-up: {"goal_achieved": bool|null, "summary": str,
	// "facts": [...]}. "" when there is none.
	OutcomeJSON string
	// ConfirmedAt is when the user confirmed the call card (zero while it is a draft). The
	// errand's phone deadline runs from it (D40 09.Q6).
	ConfirmedAt time.Time
}

// Terminal reports a finished call.
func (o CallOutcome) Terminal() bool {
	switch o.State {
	case "done", "failed", "declined", "expired":
		return true
	}
	return false
}

// PhoneCalls is the phone module (D16: in process).
type PhoneCalls interface {
	// Enabled is phone_calls_enabled(household): make_phone_call joins the errand menu only
	// when it is on (fails closed).
	Enabled(ctx context.Context, householdID string) bool
	// PlaceErrandCall drafts the call (its confirm card goes to the user). "" or an error means
	// the call couldn't be drafted: a failed step.
	PlaceErrandCall(ctx context.Context, c ErrandCall) (sessionID string, err error)
	// CallStatus reads a session (the deadline's safety net). ok=false: no such session.
	CallStatus(ctx context.Context, sessionID string) (CallOutcome, bool, error)
	// DeclineErrandCalls declines the workflow's not-yet-dialled sessions (draft/confirmed) so
	// a cancelled or timed-out errand never dials; true if any was declined.
	DeclineErrandCalls(ctx context.Context, workflowID string) (bool, error)
}

// NewSchedule is create_schedule's arguments.
type NewSchedule struct {
	HouseholdID string
	NodeID      string
	UserID      *int64
	Intent      string
	FireAt      time.Time // UTC
	Timezone    string    // the node's IANA zone
	// Recurrence is the JSON spec ({"type":"interval","interval_seconds":N} or
	// {"type":"cron","cron":"m h dom mon dow"}), "" for a one-shot.
	Recurrence string
}

// Schedules is the errand schedules store (doc 08 owns cc_schedules, its trigger, the mobile
// routes and the list card). A due schedule calls back into Service.DraftErrand.
type Schedules interface {
	CreateSchedule(ctx context.Context, s NewSchedule) (string, error)
	// PostListCard posts the "your scheduled errands" management card (Cancel buttons) and
	// returns the live schedule count; with none it posts nothing.
	PostListCard(ctx context.Context, householdID string, userID *int64) (int, error)
}
