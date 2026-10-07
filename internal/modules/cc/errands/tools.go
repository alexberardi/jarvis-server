package errands

import (
	"context"
	"fmt"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// The voice server tools (docs/cc/09 §2): run_errand, schedule_errand, list_scheduled_errands.
// Definitions are the legacy bytes. Errands are household broadcasts when the speaker is
// unknown (legacy; D21 leaves approvals untightened), so none of them refuses an unknown
// speaker; a known speaker gets user-targeted cards.

// ToolNames are the errand tools errands.enabled gates (D40 09.Q12).
var ToolNames = []string{"run_errand", "schedule_errand", "list_scheduled_errands"}

// CommandsFunc returns the conversation's merged available commands (the node's menu source).
type CommandsFunc func(conversationID string) []*pyjson.Object

func errObj(code, msg string) *pyjson.Object { return servertools.Obj("error", code, "message", msg) }

func speakerPtr(t servertools.Turn) *int64 {
	if !t.Speaker.Known() {
		return nil
	}
	id := t.Speaker.UserID
	return &id
}

func disabledResult() *pyjson.Object {
	return errObj("errands_disabled", "Errands are turned off for this household.")
}

// RunErrandTool is run_errand: queue the planning job and speak the ack.
type RunErrandTool struct {
	Svc      func() *Service
	Commands CommandsFunc
}

func (*RunErrandTool) Name() string { return "run_errand" }

func (*RunErrandTool) Definition() *pyjson.Object { return servertools.LegacyDefinition("run_errand") }

func (t *RunErrandTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	goal := parse.PyStrip(call.Str("goal"))
	if goal == "" {
		return errObj("missing_goal", "I need to know what the errand should do."), nil
	}
	if turn.ConversationID == "" {
		return errObj("no_conversation", "No conversation context available"), nil
	}
	if turn.HouseholdID == "" || turn.NodeID == "" {
		return errObj("no_context", "I couldn't tell which node to run this on."), nil
	}
	s := t.Svc()
	if s == nil {
		return errObj("task_failed", "I couldn't start planning that errand: errands are unavailable"), nil
	}
	if !s.Enabled(ctx, turn.HouseholdID) {
		return disabledResult(), nil
	}
	speaker := speakerPtr(turn)
	var available []*pyjson.Object
	if t.Commands != nil {
		available = t.Commands(turn.ConversationID)
	}
	menu := s.FullMenu(ctx, available, turn.HouseholdID, speaker)
	if err := s.DraftErrand(ctx, DraftRequest{HouseholdID: turn.HouseholdID, NodeID: turn.NodeID, UserID: speaker,
		Goal: goal, Menu: menu}); err != nil {
		return errObj("task_failed", "I couldn't start planning that errand: "+err.Error()), nil
	}
	s.log().Info("errands: draft started", "household", turn.HouseholdID, "node", turn.NodeID, "goal", goal)
	return servertools.Obj("status", "accepted", "message",
		"On it — I'm putting together a plan for that errand and I'll send "+
			"it to your phone to review. Tap Run when you're ready."), nil
}

// ScheduleErrandTool is schedule_errand: parse the time in the node's zone and create the
// schedule. The insert is synchronous (legacy fired it after acking, so a failure was
// invisible: D8).
type ScheduleErrandTool struct {
	Svc func() *Service
}

func (*ScheduleErrandTool) Name() string { return "schedule_errand" }

func (*ScheduleErrandTool) Definition() *pyjson.Object {
	return servertools.LegacyDefinition("schedule_errand")
}

func (t *ScheduleErrandTool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	goal := parse.PyStrip(call.Str("goal"))
	fireRaw := parse.PyStrip(call.Str("fire_at"))
	if goal == "" {
		return errObj("missing_goal", "I need to know what to schedule."), nil
	}
	if fireRaw == "" {
		return errObj("missing_time", "When would you like me to do that?"), nil
	}
	if turn.ConversationID == "" {
		return errObj("no_conversation", "No conversation context available"), nil
	}
	if turn.HouseholdID == "" || turn.NodeID == "" {
		return errObj("no_context", "I couldn't tell which node to schedule this on."), nil
	}
	s := t.Svc()
	if s == nil || s.Schedules == nil {
		return errObj("task_failed", "I couldn't schedule that: scheduling is unavailable"), nil
	}
	if !s.Enabled(ctx, turn.HouseholdID) {
		return disabledResult(), nil
	}
	tz := turn.Timezone
	if tz == "" {
		tz = "UTC"
	}
	now := s.now().UTC()
	fire, ok := ResolveFireAt(fireRaw, tz, now)
	recRaw := call.Str("recurrence")
	recDesc := parse.PyStrip(recRaw)
	if !ok && recDesc != "" {
		fire, ok = DefaultRecurringFire(tz, now), true
	}
	if !ok {
		return errObj("bad_time", "I couldn't work out when to run that — tell me a day and a time, like 'tomorrow at 9am'."), nil
	}
	rec := BuildRecurrence(recDesc, fire, tz)
	if !fire.After(now) {
		if rec == "" {
			return errObj("past_time", "That time has already passed — give me a future time."), nil
		}
		rolled, ok := NextFire(rec, now, tz)
		if !ok {
			return errObj("bad_time", "I couldn't work out the schedule — try 'every day at 9am'."), nil
		}
		fire = rolled
	}
	if _, err := s.Schedules.CreateSchedule(ctx, NewSchedule{HouseholdID: turn.HouseholdID, NodeID: turn.NodeID,
		UserID: speakerPtr(turn), Intent: goal, FireAt: fire, Timezone: tz, Recurrence: rec}); err != nil {
		return errObj("task_failed", "I couldn't schedule that: "+err.Error()), nil
	}
	s.log().Info("errands: schedule created", "household", turn.HouseholdID, "fire_at", fire, "recurrence", rec)
	when := friendlyWhen(fire, tz)
	if rec != "" {
		return servertools.Obj("status", "accepted", "message", fmt.Sprintf(
			"Okay — I'll do that %s, starting %s. Each time, I'll send a plan to your phone to approve first.",
			friendlyRecurrence(recRaw), when)), nil
	}
	return servertools.Obj("status", "accepted", "message", fmt.Sprintf(
		"Okay — I'll get to that %s. I'll send a plan to your phone to approve when the time comes.", when)), nil
}

// ListScheduledErrandsTool is list_scheduled_errands: speak a count and post the management
// card (Cancel is a tap, never a voice action).
type ListScheduledErrandsTool struct {
	Svc func() *Service
}

func (*ListScheduledErrandsTool) Name() string { return "list_scheduled_errands" }

func (*ListScheduledErrandsTool) Definition() *pyjson.Object {
	return servertools.LegacyDefinition("list_scheduled_errands")
}

func (t *ListScheduledErrandsTool) Execute(ctx context.Context, _ servertools.Call, turn servertools.Turn) (any, error) {
	if turn.ConversationID == "" {
		return errObj("no_conversation", "No conversation context available"), nil
	}
	if turn.HouseholdID == "" {
		return errObj("no_context", "I couldn't tell which household to check."), nil
	}
	s := t.Svc()
	if s == nil || s.Schedules == nil {
		return errObj("unavailable", "I can't check your scheduled errands right now."), nil
	}
	n, err := s.Schedules.PostListCard(ctx, turn.HouseholdID, speakerPtr(turn))
	if err != nil {
		return nil, err
	}
	switch n {
	case 0:
		return servertools.Obj("status", "ok", "message", "You don't have any errands scheduled right now."), nil
	case 1:
		return servertools.Obj("status", "ok", "message",
			"You have one errand scheduled. I've put it on your phone, where you can cancel it."), nil
	}
	return servertools.Obj("status", "ok", "message", fmt.Sprintf(
		"You have %d errands scheduled. I've sent the full list to your phone, where you can cancel any of them.", n)), nil
}
