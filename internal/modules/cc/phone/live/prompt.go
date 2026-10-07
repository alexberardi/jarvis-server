package live

import (
	"strings"
)

// The call brain's prompt (services/prompt.py). The disclosure is always the first thing the
// callee hears and seeds the history; the brief is the guardrail boundary.

// Brief is what the live model may know: the session's goal, confirmed details, constraint
// envelope and the initiator's display name ("" falls back to "a customer").
type Brief struct {
	Goal, Details, Constraints, InitiatorName string
}

func (b Brief) user() string {
	if b.InitiatorName == "" {
		return "a customer"
	}
	return b.InitiatorName
}

// WithNoThink re-asserts the thinking directive on a caller turn.
func WithNoThink(heard string) string { return heard + " " + NoThinkDirective }

// SoundsLikeFarewell reports whether the other party has closed the conversation. A greeting
// cue wins over a stray "bye" (STT turns "hi" into "bye").
func SoundsLikeFarewell(heard string) bool {
	text := strings.ToLower(heard)
	for _, g := range greetingCues {
		if strings.Contains(text, g) {
			return false
		}
	}
	for _, c := range farewellCues {
		if strings.Contains(text, c) {
			return true
		}
	}
	return false
}

// BuildDisclosure is the first agent turn, spoken before anything else. Never skipped.
func BuildDisclosure(b Brief) string {
	return strings.ReplaceAll(disclosureTemplate, "{user}", b.user())
}

// BuildSystemPrompt builds the live model's system prompt from the brief alone.
func BuildSystemPrompt(b Brief) string {
	goal, details, env := pyStrip(b.Goal), pyStrip(b.Details), pyStrip(b.Constraints)
	var parts []string
	for _, s := range promptSections {
		switch {
		case strings.Contains(s, "\x00GOAL\x00"):
			if goal == "" {
				continue
			}
			s = strings.ReplaceAll(s, "\x00GOAL\x00", goal)
		case strings.Contains(s, "\x00DETAILS\x00"):
			if details == "" {
				continue
			}
			s = strings.ReplaceAll(s, "\x00DETAILS\x00", details)
		case strings.Contains(s, "\x00ENV\x00"):
			if env == "" {
				continue
			}
			s = strings.ReplaceAll(s, "\x00ENV\x00", env)
		}
		parts = append(parts, strings.ReplaceAll(s, "\x00USER\x00", b.user()))
	}
	return strings.Join(parts, "\n\n") + "\n\n" + NoThinkDirective
}

// InitialMessages seeds a call: the system prompt plus the already-spoken disclosure as the
// first assistant turn, so the model never re-introduces itself.
func InitialMessages(b Brief) [][2]string {
	return [][2]string{{"system", BuildSystemPrompt(b)}, {"assistant", BuildDisclosure(b)}}
}
