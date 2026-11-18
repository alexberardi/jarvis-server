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
	// Warning is Jarvis's own, never the model's: how the order differs from what was asked,
	// when its check found a difference the model didn't put right (orderDiffers).
	Warning string `json:"warning,omitempty"`
}

// Reply is one parsed model answer: a batch of actions (with an optional checkout), done, or a
// question.
type Reply struct {
	Thought string `json:"thought,omitempty"`
	// Type is the shape as a schema-held answer names it (replySchema): "actions", "done", "ask".
	Type     string               `json:"type,omitempty"`
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
	// The first whole object that says something is the answer. Gemma 4 12B writes its thought
	// as one object ({"thought": ..., "actions": []}) and its answer as the next, or goes on
	// after the answer with a code fence: every such answer was thrown away (2026-11-17).
	if first, ok := firstAnswer(s[i:]); ok {
		r = first
	} else if err := json.Unmarshal([]byte(s[i:j+1]), &r); err != nil {
		// The loop runs at temperature 0, so a retry reproduces the model's slip byte for byte
		// (#528): the deterministic ones seen — a dropped "y" key in a coordinate pair, an
		// action closed before its "intent", and an answer missing only its closing braces —
		// are repaired instead of killing the run.
		if json.Unmarshal([]byte(repairSlips(s[i:j+1])), &r) != nil {
			fixed, ok := closeTruncated(repairSlips(strings.TrimRight(s[i:], " \t\r\n")))
			if !ok || json.Unmarshal([]byte(fixed), &r) != nil {
				rest, thought, ok := dropBrokenThought(s[i : j+1])
				if !ok || json.Unmarshal([]byte(rest), &r) != nil {
					return Reply{}, fmt.Errorf("the answer is not valid JSON: %v", err)
				}
				r.Thought = thought
			}
		}
	}
	r.Done = r.Done || r.Type == "done"
	if len(r.Actions) == 0 && !r.Done && strings.TrimSpace(r.Ask) == "" {
		// One action sent bare, without the object around it ({"kind": "press", "ref": "a2",
		// …}): it is the batch. Gemma 4 26B answered so twice running and the task ended as
		// unreadable with its document already saved (2026-11-15).
		var one computeruse.Action
		if json.Unmarshal([]byte(repairSlips(s[i:j+1])), &one) == nil && one.Kind != "" {
			r.Actions = []computeruse.Action{one}
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
			// An intent says what the action is for; one that runs on is cut, not a failed
			// answer: a 9B's 20-round task ended on "intent is longer than 200 characters".
			if rs := []rune(a.Intent); len(rs) > computeruse.MaxIntent {
				r.Actions[i].Intent = string(rs[:computeruse.MaxIntent-1]) + "…"
			}
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
		for _, a := range r.Actions {
			// A question written as an action: Qwen3.5 9B did it at a checkout, was told only
			// that the kind is unknown, and answered the same again (2026-11-15).
			if a.Kind == "ask" {
				return Reply{}, errors.New(`there is no "ask" action. To pay, order, send or delete, ` +
					`put the action itself in "actions" (press the button): Jarvis asks the user before ` +
					`it runs. For any other question, answer {"ask": "<the question>", "reason": "unclear"}`)
			}
		}
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

// earlyClose is an action the model closed before its "intent":
// {"kind":"click","x":72,"y":56}, "intent":"Click the New button"} (Gemma 4 26B on every
// coordinate click, 2026-11-14). The intent goes back inside.
var earlyClose = regexp.MustCompile(`\}\s*,\s*("intent"\s*:\s*"(?:[^"\\]|\\.)*")\s*\}`)

// repairSlips is s with the model's known deterministic JSON slips put right.
func repairSlips(s string) string {
	return earlyClose.ReplaceAllString(droppedY.ReplaceAllString(s, `$1,"y":$2`), `,$1}`)
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

// afterThought finds where an answer's next key starts after its thought.
var afterThought = regexp.MustCompile(`",\s*"(actions|checkout|done|ask)"\s*:`)

// dropBrokenThought reads an answer whose thought quotes something without escaping it (Qwen3.5 9B
// quoting the prompt's "checkout" back, 2026-11-16): the thought ends where the rest of the answer
// reads as JSON without it. It returns that rest as an object and the thought as written.
func dropBrokenThought(s string) (rest, thought string, ok bool) {
	open := regexp.MustCompile(`^\{\s*"thought"\s*:\s*"`).FindString(s)
	if open == "" {
		return "", "", false
	}
	for _, m := range afterThought.FindAllStringIndex(s, -1) {
		if m[0] < len(open) {
			continue
		}
		rest = "{" + strings.TrimLeft(s[m[0]+2:], " \t\r\n")
		if json.Valid([]byte(rest)) {
			return rest, s[len(open):m[0]], true
		}
	}
	return "", "", false
}

// salvageFirstAction reads the first whole action out of an answer that was cut off inside its
// "actions" list: the house model, looping, repeats a few actions until its budget ends, and
// asked again does the same. Its first action is run alone; it then looks again.
func salvageFirstAction(s string) (Reply, bool) {
	i := strings.Index(s, `"actions"`)
	if i < 0 {
		return Reply{}, false
	}
	j := strings.IndexByte(s[i:], '[')
	if j < 0 {
		return Reply{}, false
	}
	dec := json.NewDecoder(strings.NewReader(s[i+j:]))
	if _, err := dec.Token(); err != nil || !dec.More() {
		return Reply{}, false
	}
	var first json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return Reply{}, false
	}
	r, err := parseReply(`{"thought": "(the answer was cut off: its first action only)", "actions": [` + string(first) + `]}`)
	return r, err == nil && len(r.Actions) == 1
}

// firstAnswer reads the objects s starts with, one after another, and returns the first that
// acts, is done or asks, with the thought of an empty one before it. ok is false when the
// first object can't be read (parseReply's repairs then have their turn).
func firstAnswer(s string) (Reply, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	var first Reply
	for n := 0; ; n++ {
		var r Reply
		if dec.Decode(&r) != nil {
			return first, n > 0
		}
		if n == 0 {
			first = r
		}
		if len(r.Actions) > 0 || r.Done || r.Type == "done" || strings.TrimSpace(r.Ask) != "" {
			if r.Thought == "" {
				r.Thought = first.Thought
			}
			return r, true
		}
	}
}

// replySchema is a Reply as one flat object for an engine that holds an answer to a schema
// (llm.ResponseSchema; decision 2026-11-17-engine-enforced-reply-schema): "type" says which
// of the shapes it is before the fields it selects, and an action's "kind" comes first and
// brings the fields that kind needs (a press its ref, a type its text): Gemma 4 12B lost two
// good runs to a set_value with no ref and an empty action list. What it still can't say (a
// done needs a summary) parseReply checks.
var replySchema = json.RawMessage(`{"name": "reply", "schema": {"type": "object", "properties": {"thought": {"type": "string", "maxLength": 300}, "type": {"type": "string", "enum": ["actions", "done", "ask"]}, "actions": {"type": "array", "minItems": 1, "maxItems": 12, "items": {"anyOf": [{"type": "object", "properties": {"kind": {"type": "string", "enum": ["press"]}, "intent": {"type": "string", "maxLength": 200}, "ref": {"type": "string"}, "name": {"type": "string"}}, "required": ["kind", "intent", "ref"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["set_value", "select"]}, "intent": {"type": "string", "maxLength": 200}, "ref": {"type": "string"}, "text": {"type": "string"}, "name": {"type": "string"}}, "required": ["kind", "intent", "ref", "text"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["type"]}, "intent": {"type": "string", "maxLength": 200}, "text": {"type": "string"}, "ref": {"type": "string"}, "name": {"type": "string"}}, "required": ["kind", "intent", "text"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["navigate"]}, "intent": {"type": "string", "maxLength": 200}, "url": {"type": "string"}}, "required": ["kind", "intent", "url"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["keypress"]}, "intent": {"type": "string", "maxLength": 200}, "keys": {"type": "array", "items": {"type": "string"}, "minItems": 1}, "ref": {"type": "string"}, "name": {"type": "string"}}, "required": ["kind", "intent", "keys"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["wait"]}, "intent": {"type": "string", "maxLength": 200}, "ms": {"type": "integer"}}, "required": ["kind", "intent", "ms"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["click", "double_click", "move"]}, "intent": {"type": "string", "maxLength": 200}, "x": {"type": "integer"}, "y": {"type": "integer"}}, "required": ["kind", "intent", "x", "y"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["drag"]}, "intent": {"type": "string", "maxLength": 200}, "path": {"type": "array", "items": {"type": "array", "items": {"type": "integer"}}}}, "required": ["kind", "intent", "path"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["clipboard_write"]}, "intent": {"type": "string", "maxLength": 200}, "text": {"type": "string"}}, "required": ["kind", "intent", "text"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["screenshot", "clipboard_read"]}, "intent": {"type": "string", "maxLength": 200}}, "required": ["kind", "intent"]}, {"type": "object", "properties": {"kind": {"type": "string", "enum": ["scroll"]}, "intent": {"type": "string", "maxLength": 200}, "dx": {"type": "integer"}, "dy": {"type": "integer"}}, "required": ["kind", "intent"]}]}}, "checkout": {"type": "object", "properties": {"product": {"type": "string"}, "price": {"type": "string"}, "currency": {"type": "string"}, "seller": {"type": "string"}, "ship_to": {"type": "string"}, "card": {"type": "string"}}}, "summary": {"type": "string"}, "verify": {"type": "object", "properties": {"url_contains": {"type": "string"}, "text_present": {"type": "array", "items": {"type": "string"}}, "download": {"type": "object", "properties": {"name_like": {"type": "string"}, "min_bytes": {"type": "integer"}}}, "file": {"type": "object", "properties": {"path": {"type": "string"}, "contains": {"type": "string"}}}, "screen": {"type": "string"}}}, "ask": {"type": "string"}, "reason": {"type": "string", "enum": ["page_instructions", "unclear", "login_needed"]}}, "required": ["thought", "type"]}}`)
