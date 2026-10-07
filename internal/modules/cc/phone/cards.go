package phone

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/notifications"
)

// Inbox cards (phone_call_service._post_card and the card builders). Titles and emoji are
// kept byte-for-byte: mobile may key on them. Callee-derived text is always attributed.

const (
	cardCategory = "phone_call"
	cardSource   = "jarvis-command-center"
	cardTimeFmt  = "2006-01-02T15:04:05Z"
)

// card is one inbox item plus its push.
type card struct {
	HouseholdID string
	UserID      *int64
	Title       string
	Summary     string
	Body        string
	Metadata    map[string]any
}

// postCard is post_inbox_item_sync(push=True, category=phone_call, target user when there is
// one, else household). Best effort: a failed card is logged, never fatal.
func (s *Service) postCard(ctx context.Context, c card) {
	if s.Notify == nil {
		s.log().Warn("phone: inbox card dropped (notifications unavailable)", "title", c.Title)
		return
	}
	ctx = context.WithoutCancel(ctx)
	item, err := s.Notify.CreateInboxItem(ctx, nil, notifications.NewInboxItem{
		HouseholdID: c.HouseholdID, UserID: c.UserID, Title: c.Title, Summary: c.Summary, Body: c.Body,
		Category: cardCategory, SourceService: cardSource, Metadata: c.Metadata,
	})
	if err != nil {
		s.log().Warn("phone: inbox card failed", "title", c.Title, "err", err)
		return
	}
	n := notifications.Notification{
		TargetType: "household", TargetID: c.HouseholdID, Title: c.Title, Body: c.Summary,
		Data: map[string]any{"type": cardCategory, "inbox_item_id": item.ID}, Priority: "high", Category: cardCategory,
	}
	if c.UserID != nil {
		n.TargetType, n.TargetID = "user", strconv.FormatInt(*c.UserID, 10)
	}
	if _, err := s.Notify.Notify(ctx, nil, cardSource, n); err != nil {
		s.log().Warn("phone: push failed", "title", c.Title, "err", err)
	}
}

func hhMeta(hh string) map[string]any { return map[string]any{"household_id": hh} }

func sessMeta(sess *Session) map[string]any {
	return map[string]any{"household_id": sess.HouseholdID, "session_id": sess.ID}
}

// postOutcomeCard is post_outcome_card. facts may be a list (what the call loop sends), a
// dict or a scalar.
func (s *Service) postOutcomeCard(ctx context.Context, sess *Session, corrective bool) {
	var outcome map[string]any
	if sess.OutcomeJSON != "" {
		_ = json.Unmarshal([]byte(sess.OutcomeJSON), &outcome)
	}
	achieved, hasVerdict := outcome["goal_achieved"].(bool)
	summary, _ := outcome["summary"].(string)
	if summary == "" {
		summary = "The call ended."
	}
	factLines := renderFacts(outcome["facts"])
	factsBlock := ""
	if factLines != "" {
		factsBlock = "\n\nWhat the business said:\n" + factLines
	}
	icon := "⚠️"
	switch {
	case hasVerdict && achieved:
		icon = "✅"
	case !hasVerdict:
		icon = "📞"
	}
	meta := sessMeta(sess)
	if sess.AudioKey != "" {
		meta["audio_object_key"] = sess.AudioKey
	}
	cardSummary := summary
	if corrective {
		// D40 11.Q11: the reaper already reported a failure; this corrects it.
		cardSummary = "Actually, the call finished: " + summary
	}
	s.postCard(ctx, card{
		HouseholdID: sess.HouseholdID, UserID: sess.UserID,
		Title:   icon + " Call finished: " + sess.ContactName,
		Summary: cardSummary,
		Body: summary + factsBlock + "\n\n" +
			"_Summary generated from the call transcript; statements above " +
			"are the business's, not Jarvis's._",
		Metadata: meta,
	})
}

func renderFacts(v any) string {
	switch f := v.(type) {
	case nil:
		return ""
	case map[string]any:
		var lines []string
		for _, k := range sortedKeysAny(f) {
			if truthy(f[k]) {
				lines = append(lines, "- **"+k+"**: "+pyFmt(f[k]))
			}
		}
		return strings.Join(lines, "\n")
	case []any:
		var lines []string
		for _, x := range f {
			if truthy(x) {
				lines = append(lines, "- "+pyFmt(x))
			}
		}
		return strings.Join(lines, "\n")
	default:
		if !truthy(f) {
			return ""
		}
		return "- " + pyFmt(f)
	}
}

func sortedKeysAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// JSON object order is lost by the decode; sort for a deterministic card.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func pyFmt(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// postEscalationCard pushes the mid-call question to the initiator. The callee's question is
// untrusted: it is quoted and attributed, and the only actions are our fixed chips.
func (s *Service) postEscalationCard(ctx context.Context, sess *Session, question string) {
	who := sess.ContactName
	if who == "" {
		who = sess.DialedNumber
	}
	meta := sessMeta(sess)
	meta["editor_schema"] = 2
	meta["editable_fields"] = []any{
		map[string]any{"label": "Your answer", "initial": "", "data_key": "answer", "input_type": "multiline", "required": true},
	}
	meta["expires_at"] = s.now().Add(10 * time.Minute).Format(cardTimeFmt)
	meta["interactive_elements"] = []any{
		map[string]any{"id": "send-answer", "label": "Send answer", "command": ToolName, "callback": CallbackEscalationAnswer,
			"target": "server", "data": map[string]any{"session_id": sess.ID, "answer": ""}},
		map[string]any{"id": "end-call", "label": "End the call", "command": ToolName, "callback": CallbackCancel,
			"target": "server", "data": map[string]any{"session_id": sess.ID}},
	}
	s.postCard(ctx, card{
		HouseholdID: sess.HouseholdID, UserID: sess.UserID,
		Title:   "📞 The call needs your input",
		Summary: fmt.Sprintf("They asked: \"%s\"", question),
		Body: "On the call to **" + who + "**, the person asked:\n\n> " + question + "\n\n" +
			"Answer quickly — Jarvis is holding the line and will offer a " +
			"call-back if this window passes.",
		Metadata: meta,
	})
}
