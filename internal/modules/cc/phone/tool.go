package phone

import (
	"context"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// make_phone_call (core/tools/make_phone_call_tool.py). Always offered, even with phone off
// (the gates list keeps it): without it the model improvises with other tools for "call X".
// It never dials: it starts a background plan and speaks an ack; the human tap on the confirm
// card is the authorisation.

// Tool is the make_phone_call server tool.
type Tool struct{ s *Service }

// Tool returns the server tool bound to this service.
func (s *Service) Tool() *Tool { return &Tool{s: s} }

func (*Tool) Name() string { return ToolName }

func (*Tool) Definition() *pyjson.Object { return servertools.LegacyDefinition(ToolName) }

func refusal(code, msg string) *pyjson.Object {
	return servertools.Obj("error", code, "message", msg)
}

func (t *Tool) Execute(ctx context.Context, call servertools.Call, turn servertools.Turn) (any, error) {
	business := strings.TrimSpace(call.Str("business"))
	goal := strings.TrimSpace(call.Str("goal"))
	if business == "" || goal == "" {
		return refusal("missing_params", "I need the business name and what the call should accomplish."), nil
	}
	if turn.ConversationID == "" {
		return refusal("no_conversation", "No conversation context available"), nil
	}
	if turn.HouseholdID == "" {
		return refusal("no_household", "No household context available"), nil
	}
	if !t.s.Enabled(ctx, turn.HouseholdID) {
		t.s.log().Info("phone: make_phone_call refused — phone_calls disabled", "household", turn.HouseholdID)
		return refusal("phone_calls_disabled", "Phone calls aren't set up on this Jarvis. A household "+
			"admin can enable them in Household Settings."), nil
	}
	if _, err := t.s.Telephony(ctx, turn.HouseholdID); isNotConfigured(err) {
		// AD6: say so now rather than after the user confirms a plan that can't dial.
		t.s.log().Info("phone: make_phone_call refused — no telephony account", "household", turn.HouseholdID)
		return refusal("phone_not_configured", NotConfiguredMessage(err)), nil
	}
	if !turn.Speaker.Known() {
		// D21: the confirm card must land on an identified user's phone. M14: say why when
		// speaker recognition is off.
		msg := "I couldn't tell who's asking, so I can't send the call " +
			"plan to your phone. Try again from the Jarvis app."
		if turn.Speaker.RecognitionOff {
			msg = "Speaker recognition is off, so I can't tell who's asking and can't send the call " +
				"plan to your phone. Try again from the Jarvis app."
		}
		return refusal("no_identified_speaker", msg), nil
	}
	uid := turn.Speaker.UserID
	t.s.planAsync(PlanRequest{Business: business, Goal: goal, HouseholdID: turn.HouseholdID, UserID: &uid})
	t.s.log().Info("phone: call plan started", "household", turn.HouseholdID, "user", uid)
	return servertools.Obj("status", "accepted", "message", "I've sent the call plan for "+business+
		" to your phone — review it and tap Call now to dial."), nil
}
