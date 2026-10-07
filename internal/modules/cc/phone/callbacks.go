package phone

import (
	"context"
	"strings"
)

// Server-plane card callbacks (phone_call_service.py:837-1039). They arrive as card taps on
// doc 13's POST /callbacks, which checks household membership and dispatches here by
// "<command>.<callback>". Any household member may confirm (D21).

// Tool and callback names.
const (
	ToolName                 = "make_phone_call"
	CallbackConfirm          = "confirm_call"
	CallbackCancel           = "cancel_call"
	CallbackEscalationAnswer = "escalation_answer"
)

// CallbackContext is ServerCallbackContext: the tapping user, their household, the card data.
type CallbackContext struct {
	HouseholdID string
	UserID      int64
	Data        map[string]any
}

// CallbackResult is ServerCallbackResult.
type CallbackResult struct {
	Success     bool
	Error       string
	ContextData map[string]any
}

func cbErr(msg string) CallbackResult { return CallbackResult{Error: msg} }

func inboxCtx(title, summary string, meta map[string]any) map[string]any {
	return map[string]any{"inbox": map[string]any{"title": title, "summary": summary, "metadata": meta}}
}

// CallbackFunc handles one server callback.
type CallbackFunc func(ctx context.Context, cb CallbackContext) CallbackResult

// Callbacks returns the phone's server callbacks keyed "make_phone_call.<callback>", for the
// doc 13 registry.
func (s *Service) Callbacks() map[string]CallbackFunc {
	return map[string]CallbackFunc{
		ToolName + "." + CallbackConfirm:          s.ConfirmCall,
		ToolName + "." + CallbackCancel:           s.CancelCall,
		ToolName + "." + CallbackEscalationAnswer: s.AnswerEscalation,
	}
}

func dataStr(d map[string]any, k string) string {
	v, _ := d[k].(string)
	return v
}

// ConfirmCall is _handle_confirm_call: gate re-check, single use, TTL, caps, number
// re-validation, do-not-call by number (D40 11.Q4), audit fields, then draft → confirmed and
// the dial hand-off. The confirmed → dialing CAS in the dialer stays the authorisation.
func (s *Service) ConfirmCall(ctx context.Context, cb CallbackContext) CallbackResult {
	id := dataStr(cb.Data, "session_id")
	if id == "" {
		return cbErr("Missing session_id")
	}
	sess, err := s.sessionIn(ctx, s.DB.Read, id, cb.HouseholdID)
	if err != nil {
		if !isNoRows(err) {
			s.log().Error("phone: confirm lookup failed", "err", err)
		}
		return cbErr("Call plan not found")
	}
	if !s.Enabled(ctx, cb.HouseholdID) {
		_, _ = s.transition(ctx, sess, StateDeclined, map[string]any{"error_message": "phone_calls.enabled turned off"})
		s.resumeErrand(ctx, sess)
		return cbErr("Phone calls are disabled for this household.")
	}
	if sess.State != StateDraft {
		return CallbackResult{Success: true, ContextData: inboxCtx("📞 Already handled",
			"This call plan is already "+sess.State+".", hhMeta(cb.HouseholdID))}
	}
	if !sess.ExpiresAt.IsZero() && s.now().After(sess.ExpiresAt) {
		_, _ = s.transition(ctx, sess, StateExpired, nil)
		s.resumeErrand(ctx, sess)
		return cbErr("This call plan expired. Ask Jarvis again to get a fresh one.")
	}
	if refusal := s.checkCaps(ctx, cb.HouseholdID); refusal != "" {
		_, _ = s.transition(ctx, sess, StateDeclined, map[string]any{"error_message": refusal})
		s.resumeErrand(ctx, sess)
		return cbErr(refusal)
	}
	dialed, err := NormalizeUS(strings.TrimSpace(dataStr(cb.Data, "dialed_number")))
	if err != nil {
		return cbErr(err.Error())
	}
	dnc, err := s.dncNumbers(ctx, cb.HouseholdID)
	if err != nil {
		s.log().Error("phone: do-not-call check failed — blocking", "err", err)
		return cbErr("Phone calls are temporarily unavailable.")
	}
	if dnc[dialed] {
		return cbErr("That number is marked do-not-call for this household.")
	}
	details := strings.TrimSpace(dataStr(cb.Data, "details"))
	if details == "" {
		return cbErr("The call details can't be empty.")
	}
	edited := 0
	if dialed != sess.ResolvedNumber {
		edited = 1
	}
	ok, err := s.transition(ctx, sess, StateConfirmed, map[string]any{
		"dialed_number": dialed, "number_edited": edited, "details": details, "confirmed_by": cb.UserID,
	})
	if err != nil || !ok {
		if err != nil {
			s.log().Error("phone: confirm write failed", "err", err)
		}
		return cbErr("Couldn't start the call — try again in a minute.")
	}
	sess.DialedNumber, sess.Details, sess.NumberEdited = dialed, details, edited == 1
	uid := cb.UserID
	sess.ConfirmedBy = &uid
	if err := s.enqueueDial(sess); err != nil {
		s.log().Error("phone: dial hand-off failed", "session", sess.ID, "err", err)
		_, _ = s.transition(ctx, sess, StateFailed, map[string]any{"error_message": "dial enqueue failed: " + err.Error()})
		s.resumeErrand(ctx, sess)
		return cbErr("Couldn't start the call — try again in a minute.")
	}
	s.log().Info("phone: call confirmed", "session", sess.ID, "edited", sess.NumberEdited, "by", cb.UserID)
	return CallbackResult{Success: true, ContextData: inboxCtx("📞 Calling "+sess.ContactName+"…",
		"You'll get a summary card when the call ends.", sessMeta(sess))}
}

// CancelCall is _handle_cancel_call, made real by D40 11.Q3: a draft or a confirmed call is
// declined (a confirmed one then loses the claim), and a live call is hung up and marked
// failed ("cancelled by user"). The card text is unchanged.
func (s *Service) CancelCall(ctx context.Context, cb CallbackContext) CallbackResult {
	sess, err := s.sessionIn(ctx, s.DB.Read, dataStr(cb.Data, "session_id"), cb.HouseholdID)
	if err != nil {
		return cbErr("Call plan not found")
	}
	switch {
	case sess.State == StateDraft || sess.State == StateConfirmed:
		if ok, _ := s.transition(ctx, sess, StateDeclined, map[string]any{"error_message": "cancelled by user"}); ok {
			s.resumeErrand(ctx, sess)
		}
	case isActive(sess.State):
		s.cancelLive(ctx, sess, "cancelled by user")
	}
	return CallbackResult{Success: true, ContextData: inboxCtx("🚫 Call cancelled",
		"Won't call "+sess.ContactName+".", hhMeta(cb.HouseholdID))}
}

// AnswerEscalation is _handle_escalation_answer: the answer goes straight into the live
// call's open escalation window (a channel send, D16).
func (s *Service) AnswerEscalation(ctx context.Context, cb CallbackContext) CallbackResult {
	id := dataStr(cb.Data, "session_id")
	answer := strings.TrimSpace(dataStr(cb.Data, "answer"))
	sess, err := s.sessionIn(ctx, s.DB.Read, id, cb.HouseholdID)
	rt := s.runtime(id)
	if err != nil || rt == nil {
		return cbErr("This call is no longer active.")
	}
	if !isActive(sess.State) {
		return cbErr("The call already ended.")
	}
	if !rt.escalation.Deliver(answer) {
		return cbErr("Couldn't reach the call — it may have just ended.")
	}
	return CallbackResult{Success: true}
}

// resumeErrand hands a terminal session to the errand engine (direct deliver, not only the
// 20 s sweep), off the caller's path.
func (s *Service) resumeErrand(ctx context.Context, sess *Session) {
	if s.Errands == nil || sess.ErrandID == "" || !isTerminal(sess.State) {
		return
	}
	cur, err := s.session(ctx, s.DB.Read, sess.ID)
	if err != nil {
		cur = sess
	}
	go s.Errands.CallTerminal(context.WithoutCancel(ctx), snapshotOf(cur))
}
