package dictate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Confirmation modes (--confirm-dictation).
const (
	ConfirmOff    = "off"    // never ask
	ConfirmSubmit = "submit" // ask before anything risky is sent: Enter after typing on the desktop; in the browser, a click or submitting fill that pays, sends, deletes, shares or changes the account (browser/risk.go). A search runs (the default)
	ConfirmAll    = "all"    // ask before every typing, key press, fill or click
)

// ParseConfirmMode reads --confirm-dictation. Unset is submit: page or screen text the model
// read can steer it, so nothing is sent without the user's yes unless they turned that off.
func ParseConfirmMode(s string) (string, error) {
	switch m := strings.ToLower(strings.TrimSpace(s)); m {
	case "":
		return ConfirmSubmit, nil
	case ConfirmOff, ConfirmSubmit, ConfirmAll:
		return m, nil
	}
	return "", fmt.Errorf("--confirm-dictation must be off, submit or all (got %q)", s)
}

// DefaultConfirmTimeout is how long a question waits for its answer before it is cancelled.
const DefaultConfirmTimeout = 60 * time.Second

// ToolConfirm answers a pending confirmation.
const ToolConfirm = "desktop_confirm"

// Confirmer holds at most one action waiting for the user's yes. The action runs only through
// Answer with a yes in the user's words, before the deadline; a no, an unclear answer, a
// timeout or a newer question cancels it. The question itself is asked by the model (a
// request_validation question, which a voice node asks aloud and listens for without the wake
// word, and the app shows with its options), so any way the user answers Jarvis works: voice,
// a tap, typed text.
type Confirmer struct {
	Mode    string
	Timeout time.Duration    // 0: DefaultConfirmTimeout
	Now     func() time.Time // nil: time.Now (tests inject a clock)
	// Show, when set, puts the question on the island while it waits ("" clears it).
	Show func(text string, d time.Duration)

	mu      sync.Mutex
	seq     int
	pending *pending
}

type pending struct {
	id       string
	question string
	deadline time.Time
	act      func(ctx context.Context) (map[string]any, error)
}

// Gate kinds: what an action does, for the mode to decide whether to ask.
type Gate int

const (
	GateType   Gate = iota // types or presses keys, nothing is sent
	GateSubmit             // sends something risky: Enter after text, a risky browser click or submitting fill
)

// Needs reports whether an action of this kind must be confirmed first.
func (c *Confirmer) Needs(g Gate) bool {
	if c == nil {
		return false
	}
	switch c.Mode {
	case ConfirmAll:
		return true
	case ConfirmSubmit:
		return g == GateSubmit
	}
	return false
}

// On reports whether confirmation is in use at all (desktop_confirm is offered).
func (c *Confirmer) On() bool { return c != nil && c.Mode != "" && c.Mode != ConfirmOff }

func (c *Confirmer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Confirmer) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultConfirmTimeout
}

// WaitingPrefix marks a parked action's question on the island: asked, not done.
const WaitingPrefix = "Waiting for your yes: "

// Ask parks act behind question and returns the tool output that tells the model to ask the
// user. A question still waiting is cancelled: only the newest can be confirmed. The output is
// not a success (#420): a 3 ms "browser_fill succeeded" that filled nothing read as done to
// the logs, the island and the model.
func (c *Confirmer) Ask(question string, act func(ctx context.Context) (map[string]any, error)) map[string]any {
	c.mu.Lock()
	c.seq++
	p := &pending{id: fmt.Sprintf("confirm-%d", c.seq), question: question, deadline: c.now().Add(c.timeout()), act: act}
	c.pending = p
	c.mu.Unlock()
	if c.Show != nil {
		c.Show(WaitingPrefix+question, c.timeout())
	}
	return map[string]any{
		"success": false, "done": false, "status": "awaiting_confirmation",
		"confirmation_id": p.id, "question": question,
		"next": "Nothing has been done yet. Ask the user exactly this question (use request_validation with the options " +
			"[\"Yes\", \"Cancel\"]), then call " + ToolConfirm + " with this confirmation_id and the user's own words. " +
			"Never answer it yourself.",
		"expires_in_seconds": int(c.timeout().Seconds()),
	}
}

// Pending reports whether the question with this id (an Ask output's confirmation_id) is still
// waiting for its answer.
func (c *Confirmer) Pending(id any) bool {
	s, _ := id.(string)
	if c == nil || s == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending != nil && c.pending.id == s && !c.now().After(c.pending.deadline)
}

// Answer resolves the pending question with the user's words. It returns the action's output on
// a yes, a cancelled output otherwise, and an error for an unknown or already used id.
func (c *Confirmer) Answer(ctx context.Context, id, answer string) (map[string]any, error) {
	c.mu.Lock()
	p := c.pending
	if p == nil || p.id != strings.TrimSpace(id) {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: no question %q is waiting (it was answered, replaced or never asked)", errBadArgs, id)
	}
	c.pending = nil // single use, whatever the answer
	expired := c.now().After(p.deadline)
	c.mu.Unlock()
	if c.Show != nil {
		c.Show("", 0)
	}
	cancelled := func(why string) (map[string]any, error) {
		return map[string]any{"success": true, "done": false, "cancelled": true, "reason": why}, nil
	}
	if expired {
		return cancelled(fmt.Sprintf("no answer within %d seconds, so it was cancelled; nothing was done", int(c.timeout().Seconds())))
	}
	switch ClassifyAnswer(answer) {
	case AnswerYes:
		return p.act(ctx)
	case AnswerNo:
		return cancelled("the user said no; nothing was done")
	}
	return cancelled("the answer wasn't a clear yes, so it was cancelled; nothing was done")
}
