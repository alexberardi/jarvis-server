package dictate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// Failures a small model can't misread. A local 9B model once answered "Done. I've typed that
// into Omerite." over {success: false, error: "no open window matches ..."}: the error said what
// went wrong but not what that meant for the user. So a failed call's error is a sentence that
// starts with the outcome ("Nothing was typed"), says why, and says what to tell the user; and
// the island shows the outcome itself, whatever the model then says.

// outcome is what a failed call of the tool did: "Nothing was typed".
func outcome(tool string) string {
	switch tool {
	case ToolType:
		return "Nothing was typed"
	case ToolPress:
		return "No key was pressed"
	}
	return "Nothing was done"
}

// FailureText is a failed call's error for the model: the outcome, the reason, and what to tell
// the user.
func FailureText(tool string, err error) string {
	what := outcome(tool)
	tell := strings.ToLower(what[:1]) + what[1:]
	var we *winmatch.Error
	if errors.As(err, &we) {
		switch we.Code {
		case winmatch.CodeNotFound:
			s := fmt.Sprintf("%s: no open window matches %q.", what, we.Query)
			if len(we.Apps) > 0 {
				s += " Open apps: " + strings.Join(we.Apps, ", ") + "."
			}
			return s + " Tell the user " + tell + " and ask which app they meant."
		case winmatch.CodeAmbiguous:
			// The windows are in candidates, not here: an error is logged, and titles can be
			// private.
			s := fmt.Sprintf("%s: more than one window could be %q", what, we.Query)
			if len(we.Candidates) > 0 {
				s += fmt.Sprintf(" (%d, listed in candidates)", len(we.Candidates))
			}
			return s + ". Tell the user " + tell + " and ask which one they meant."
		}
	}
	if errors.Is(err, errSubmit) {
		return fmt.Sprintf("The text was typed, but Enter wasn't pressed (%s). Tell the user it was typed but not sent.", unwrapMsg(err))
	}
	return fmt.Sprintf("%s: %s. Tell the user %s and why.", what, unwrapMsg(err), tell)
}

// unwrapMsg is an error's message without a trailing period.
func unwrapMsg(err error) string {
	return strings.TrimRight(strings.TrimSpace(err.Error()), ".")
}

// FailureNote is the island's line for a failed call: "Couldn't find Omerite — nothing typed".
func FailureNote(tool string, err error) string {
	nothing := "nothing done"
	switch tool {
	case ToolType:
		nothing = "nothing typed"
	case ToolPress:
		nothing = "no key pressed"
	}
	var we *winmatch.Error
	switch {
	case errors.As(err, &we) && we.Code == winmatch.CodeNotFound:
		return "Couldn't find " + Preview(strings.TrimSpace(we.Query), 40) + " — " + nothing
	case errors.As(err, &we) && we.Code == winmatch.CodeAmbiguous:
		return "Which " + Preview(strings.TrimSpace(we.Query), 40) + "? — " + nothing
	case errors.As(err, &we):
		return "Couldn't use " + Preview(strings.TrimSpace(we.Query), 40) + " — " + nothing
	case errors.Is(err, errSubmit):
		return "Typed, but couldn't press Enter"
	case errors.Is(err, errFocus):
		return "Couldn't switch windows — " + nothing
	case errors.Is(err, ErrUnavailable):
		return "Can't do that on this desktop — " + nothing
	}
	return "That didn't work — " + nothing
}

// report adds the named app to a result: which app it went to, and when the name was a near
// spelling, what the user said (so the model can say "typed into omawrite").
func (tg *target) report(out map[string]any) {
	if tg == nil {
		return
	}
	out["matched_app"] = tg.app
	if tg.fuzzy {
		out["fuzzy_match"] = true
		out["heard_as"] = tg.asked
	}
}
