package computer

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
		return Reply{}, fmt.Errorf("the answer is not valid JSON: %v", err)
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
		for i, a := range r.Actions {
			if a.Kind == "click" && a.Ref != "" && a.X == nil && a.Y == nil {
				r.Actions[i].Kind = "press" // a click on a listed element
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
