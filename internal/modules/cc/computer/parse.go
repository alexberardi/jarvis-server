package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Ask reasons the model may give.
const (
	ReasonPageInstructions = "page_instructions"
	ReasonUnclear          = "unclear"
	ReasonLoginNeeded      = "login_needed"
)

// Checkout is what the model reports when the next actions buy something: the confirmation card
// shows it (piece 11).
type Checkout struct {
	Product  string `json:"product"`
	Price    string `json:"price"`
	Currency string `json:"currency,omitempty"`
	Seller   string `json:"seller,omitempty"`
	ShipTo   string `json:"ship_to,omitempty"`
	Card     string `json:"card,omitempty"` // last 4 only
}

// Reply is one parsed model answer: a batch of actions (with an optional checkout), done, or a
// question.
type Reply struct {
	Thought  string               `json:"thought,omitempty"`
	Actions  []computeruse.Action `json:"actions,omitempty"`
	Checkout *Checkout            `json:"checkout,omitempty"`
	Done     bool                 `json:"done,omitempty"`
	Summary  string               `json:"summary,omitempty"`
	Verify   *SuccessCheck        `json:"verify,omitempty"`
	Ask      string               `json:"ask,omitempty"`
	Reason   string               `json:"reason,omitempty"`
}

// parseReply reads the model's JSON answer. It tolerates a code fence or text around the object;
// anything else that isn't one of the four shapes is an error (worded for the model, which gets it
// back on the retry).
func parseReply(s string) (Reply, error) {
	s = strings.TrimSpace(s)
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return Reply{}, errors.New("the answer is not a JSON object")
	}
	var r Reply
	if err := json.Unmarshal([]byte(s[i:j+1]), &r); err != nil {
		// The loop runs at temperature 0, so a retry reproduces the model's slip byte for byte
		// (#528): the two deterministic ones seen — a dropped "y" key in a coordinate pair and
		// an answer missing only its closing braces — are repaired instead of killing the run.
		if json.Unmarshal([]byte(droppedY.ReplaceAllString(s[i:j+1], `$1,"y":$2`)), &r) != nil {
			fixed, ok := closeTruncated(droppedY.ReplaceAllString(strings.TrimRight(s[i:], " \t\r\n"), `$1,"y":$2`))
			if !ok || json.Unmarshal([]byte(fixed), &r) != nil {
				return Reply{}, fmt.Errorf("the answer is not valid JSON: %v", err)
			}
		}
	}
	switch {
	case r.Done:
		if strings.TrimSpace(r.Summary) == "" {
			return Reply{}, errors.New(`"done" needs a "summary"`)
		}
		r.Actions, r.Checkout, r.Ask = nil, nil, ""
	case strings.TrimSpace(r.Ask) != "":
		switch r.Reason {
		case ReasonPageInstructions, ReasonUnclear, ReasonLoginNeeded:
		default:
			r.Reason = ReasonUnclear
		}
		r.Actions, r.Checkout = nil, nil
	default:
		thought, _, _ := strings.Cut(strings.TrimSpace(r.Thought), "\n")
		if utf8.RuneCountInString(thought) > computeruse.MaxIntent || len(r.Actions) > 1 {
			thought = ""
		}
		for i, a := range r.Actions {
			if a.Kind == "click" && a.Ref != "" && a.X == nil && a.Y == nil {
				r.Actions[i].Kind = "press" // a click on a listed element
			}
			if strings.TrimSpace(a.Intent) == "" {
				// The house model leaves it out; the risk comes from the element, not the intent.
				r.Actions[i].Intent = thought
				if thought == "" {
					r.Actions[i].Intent = strings.TrimSpace(r.Actions[i].Kind + " " + a.Ref)
				}
			}
		}
		r.Actions = splitLongTypes(r.Actions)
		if err := computeruse.ValidateBatch(r.Actions); err != nil {
			return Reply{}, fmt.Errorf(`"actions": %v`, err)
		}
		if c := r.Checkout; c != nil && (strings.TrimSpace(c.Product) == "" || strings.TrimSpace(c.Price) == "") {
			return Reply{}, errors.New(`"checkout" needs a "product" and a "price"`)
		}
	}
	return r, nil
}

// splitLongTypes turns a type action whose text is over a type action's limit into several, in
// order, each within it: the text is what the user asked for, and splitting it is arithmetic,
// not the model's job. The house model, told the limit and then told again, still sent the
// whole summary and the task failed (suite on Qwen3.5-9B, 2026-11-13); Sonnet needed a retry.
// Parts end at a paragraph, else a sentence, else a word. A type into a named element (ref) sets
// its value whole, so it is left to fail validation as before.
func splitLongTypes(actions []computeruse.Action) []computeruse.Action {
	var out []computeruse.Action
	for _, a := range actions {
		if a.Kind != "type" || a.Ref != "" || utf8.RuneCountInString(a.Text) <= computeruse.MaxText {
			out = append(out, a)
			continue
		}
		for _, part := range splitText(a.Text, computeruse.MaxText) {
			p := a
			p.Text = part
			out = append(out, p)
		}
	}
	return out
}

// splitText cuts s into parts of at most max runes whose concatenation is s, each ending at
// the last paragraph break in its second half, else the last sentence end, else the last space,
// else at max.
func splitText(s string, max int) []string {
	r := []rune(s)
	var parts []string
	for len(r) > max {
		cut := max
		for _, ends := range []string{"\n", ".!?", " "} {
			found := false
			for i := max; i > max/2; i-- {
				// A sentence ends at its mark when a space or the end follows.
				if strings.ContainsRune(ends, r[i-1]) && (ends != ".!?" || i == len(r) || r[i] == ' ' || r[i] == '\n') {
					cut, found = i, true
					break
				}
			}
			if found {
				break
			}
		}
		parts = append(parts, string(r[:cut]))
		r = r[cut:]
	}
	if len(r) > 0 {
		parts = append(parts, string(r))
	}
	return parts
}

// droppedY is a coordinate pair whose "y" key the model dropped: "x":170,777. The bare second
// number can only be the y, so the key is put back.
var droppedY = regexp.MustCompile(`("x"\s*:\s*-?\d+)\s*,\s*(-?\d+)`)

// closeTruncated appends the closers a truncated JSON object is missing. Only unclosed objects
// and arrays are repaired: an answer cut mid-string stays broken, rather than guessing at its
// lost content.
func closeTruncated(s string) (string, bool) {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if n := len(stack); n == 0 || (c == '}') != (stack[n-1] == '{') {
				return "", false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inStr || len(stack) == 0 {
		return "", false
	}
	b := []byte(s)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			b = append(b, '}')
		} else {
			b = append(b, ']')
		}
	}
	return string(b), true
}
