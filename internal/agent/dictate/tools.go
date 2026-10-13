package dictate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// Tool names.
const (
	ToolType  = "desktop_type_text"
	ToolPress = "desktop_press_key"
)

// Focus confirmation: a focus request is asynchronous everywhere, so the active window is
// polled until it is the one asked for. Nothing is typed otherwise.
const (
	focusTries = 20
	focusPoll  = 50 * time.Millisecond
)

// ShowFor is how long the island shows what was typed.
const ShowFor = 2500 * time.Millisecond

// Tools is dictation: desktop_type_text and desktop_press_key (offered while typing works),
// and desktop_confirm (offered while confirmation is on; the browser's fill and click use the
// same Confirmer).
type Tools struct {
	KB      Keyboard
	Win     Windows    // nil: named apps are unavailable
	Confirm *Confirmer // nil or mode off: nothing is asked first
	// Show, when set, shows text on the island for d ("" clears it).
	Show func(text string, d time.Duration)
	Log  *slog.Logger
	// Sleep waits between focus checks; nil: a real timer (tests pass one that returns at once).
	Sleep func(ctx context.Context, d time.Duration) error
}

var appParam = node.Param{Name: "app", Type: "string",
	Description: "The app or window to switch to first, in the user's words: \"claude\", \"slack\", \"the terminal\", " +
		"or words from its title. Leave it out to use whatever window is in front."}

func typeSpec() node.Spec {
	return node.Spec{Name: ToolType,
		Description: "Type text on the user's computer, as if typed on the keyboard: into the window in front, or into the app " +
			"the user names (it is brought to the front first). Use it for dictation: \"type hello world\" → {text: \"hello world\"}; " +
			"\"tell claude to run the tests\" → {app: \"claude\", text: \"run the tests\", submit: true}; \"write thanks in slack\" → " +
			"{app: \"slack\", text: \"thanks\"}. Put only the words to type in text, never the instruction around them, and " +
			"keep the user's wording. submit presses Enter afterwards: true when the user says \"tell X to …\", \"send …\", \"ask X …\" " +
			"or \"… and press enter\"; false for \"type …\", \"write …\", \"put … in\". For a field on a web page use " +
			"browser_fill instead. If the result says awaiting_confirmation, nothing was typed yet: ask the user the question.",
		Params: []node.Param{
			{Name: "text", Type: "string", Required: true, Description: "Exactly the words to type"},
			appParam,
			{Name: "submit", Type: "bool", Description: "Press Enter after typing, to send it (default false)"},
		},
		Keywords: []string{"type", "dictate", "write", "tell", "send", "enter", "keyboard", "claude", "terminal", "message"},
		Examples: []node.Example{
			{VoiceCommand: "tell claude to test def", Params: map[string]any{"app": "claude", "text": "test def", "submit": true}, Primary: true},
			{VoiceCommand: "type hello world", Params: map[string]any{"text": "hello world"}},
			{VoiceCommand: "write see you at five in slack", Params: map[string]any{"app": "slack", "text": "see you at five"}},
			{VoiceCommand: "type git status in the terminal and press enter", Params: map[string]any{"app": "terminal", "text": "git status", "submit": true}},
		},
		Risky: true,
	}
}

func pressSpec() node.Spec {
	keys := make([]string, len(Keys))
	for i, k := range Keys {
		keys[i] = string(k)
	}
	return node.Spec{Name: ToolPress,
		Description: "Press one key on the user's computer, in the window in front or in the app the user names. Use it to answer a " +
			"dialog box on the computer: \"click OK\", \"press enter\", \"say yes to that\", \"accept it\" → enter (the default " +
			"button); \"cancel\", \"hit cancel\", \"close that dialog\", \"press escape\", \"no\" → escape; \"next button\" → tab; " +
			"\"previous button\" → shift+tab; space presses the highlighted button or ticks a box; arrows move between choices. " +
			"For a button on a web page use browser_click instead.",
		Params: []node.Param{
			{Name: "key", Type: "string", Required: true, Enum: keys, Description: "The key to press"},
			appParam,
		},
		Keywords: []string{"press", "key", "enter", "escape", "cancel", "ok", "dialog", "button", "tab", "click", "accept"},
		Examples: []node.Example{
			{VoiceCommand: "press enter", Params: map[string]any{"key": "enter"}, Primary: true},
			{VoiceCommand: "click OK", Params: map[string]any{"key": "enter"}},
			{VoiceCommand: "hit cancel", Params: map[string]any{"key": "escape"}},
			{VoiceCommand: "press escape in firefox", Params: map[string]any{"key": "escape", "app": "firefox"}},
		},
	}
}

func confirmSpec() node.Spec {
	return node.Spec{Name: ToolConfirm,
		Description: "Answer a question one of the computer tools asked (status awaiting_confirmation) with the user's reply. " +
			"Call it only after asking the user, and pass their own words: \"OK\", \"yes\", \"send it\", \"confirm\" go ahead; " +
			"\"cancel\", \"no\", \"stop\" or anything unclear cancels. Never call it without the user's answer.",
		Params: []node.Param{
			{Name: "confirmation_id", Type: "string", Required: true, Description: "The confirmation_id from the tool's result"},
			{Name: "answer", Type: "string", Required: true, Description: "The user's reply, in their own words"},
		},
		Keywords: []string{"yes", "no", "ok", "cancel", "confirm", "send"},
	}
}

// Available offers typing and keys while the keyboard works, and desktop_confirm while
// confirmation is on.
func (t *Tools) Available(ctx context.Context) []node.ToolDef {
	var out []node.ToolDef
	if t.KB != nil {
		if ok, _ := t.KB.Available(ctx); ok {
			out = append(out, typeSpec().Render(), pressSpec().Render())
		}
	}
	if t.Confirm.On() {
		out = append(out, confirmSpec().Render())
	}
	return out
}

// Call runs a tool. Failures are outputs with error and error_code.
func (t *Tools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	var (
		out map[string]any
		err error
	)
	switch name {
	case ToolType:
		out, err = t.typeText(ctx, args)
	case ToolPress:
		out, err = t.pressKey(ctx, args)
	case ToolConfirm:
		if !t.Confirm.On() {
			return nil, false
		}
		id, _ := args["confirmation_id"].(string)
		answer, _ := args["answer"].(string)
		out, err = t.Confirm.Answer(ctx, id, answer)
	default:
		return nil, false
	}
	if err != nil {
		o := node.Fail("%v", err)
		o["error_code"] = Code(err)
		var we *winmatch.Error
		if errors.As(err, &we) && len(we.Candidates) > 0 {
			o["candidates"] = we.Candidates
		}
		t.log().Info("dictation", "tool", name, "error_code", o["error_code"])
		return o, true
	}
	if _, ok := out["success"]; !ok {
		out["success"] = true
	}
	return out, true
}

func (t *Tools) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return t.Log
}

var errFocus = errors.New("focus not confirmed")

// Code maps an error to the output's error_code.
func Code(err error) string {
	var we *winmatch.Error
	var coded interface{ ErrorCode() string }
	switch {
	case errors.As(err, &coded):
		return coded.ErrorCode()
	case errors.As(err, &we):
		return we.Code
	case errors.Is(err, errBadArgs):
		return "invalid_argument"
	case errors.Is(err, ErrUnavailable):
		return "not_supported"
	case errors.Is(err, errFocus):
		return "focus_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "failed"
}

// BoolArg reads a JSON bool, or the strings small models send ("true", "yes").
func BoolArg(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "1":
			return true
		}
	case float64:
		return v != 0
	}
	return false
}

// target is the app the user named, resolved before anything is asked or typed.
type target struct {
	id   string
	name string // the app's display name, for the island and the result
}

// resolve finds the one window the user named. No name: the window in front (nothing to do).
func (t *Tools) resolve(ctx context.Context, app string) (*target, error) {
	if winmatch.Normalize(app) == "" {
		return nil, nil
	}
	if t.Win == nil {
		return nil, fmt.Errorf("%w: switching to a named app isn't supported on this computer; ask the user to click into it and try again without naming it", ErrUnavailable)
	}
	if ok, why := t.Win.Available(ctx); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	ws, err := t.Win.List(ctx)
	if err != nil {
		return nil, err
	}
	active, _ := t.Win.Active(ctx)
	cands := make([]winmatch.Candidate, len(ws))
	focused := -1
	for i, w := range ws {
		cands[i] = winmatch.Candidate{Apps: w.Apps, Title: w.Title, Recency: w.Recency, Visible: w.Visible}
		if active != "" && t.Win.Same(w.ID, active) {
			focused = i
		}
	}
	i, err := winmatch.PickUnique(cands, app, focused)
	if err != nil {
		return nil, err
	}
	return &target{id: ws[i].ID, name: winmatch.DisplayName(cands[i])}, nil
}

// focus brings the target to the front and waits until it really is: typing into whatever
// happened to be in front instead is the one thing this must never do.
func (t *Tools) focus(ctx context.Context, tg *target) error {
	if tg == nil {
		return nil
	}
	if active, err := t.Win.Active(ctx); err == nil && t.Win.Same(tg.id, active) {
		return nil
	}
	if err := t.Win.Focus(ctx, tg.id); err != nil {
		return fmt.Errorf("%w: couldn't bring %s to the front (%v); nothing was typed", errFocus, tg.name, err)
	}
	sleep := t.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	for range focusTries {
		if active, err := t.Win.Active(ctx); err == nil && t.Win.Same(tg.id, active) {
			return nil
		}
		if err := sleep(ctx, focusPoll); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: asked %s to come to the front, but it didn't get the keyboard focus; nothing was typed", errFocus, tg.name)
}

func (t *Tools) show(text string) {
	if t.Show != nil {
		t.Show(text, ShowFor)
	}
}

// Preview is text cut for the island and questions.
func Preview(s string, n int) string {
	return node.Truncate(s, n)
}

func (t *Tools) typeText(ctx context.Context, args map[string]any) (map[string]any, error) {
	if t.KB == nil {
		return nil, fmt.Errorf("%w: typing isn't supported here", ErrUnavailable)
	}
	if ok, why := t.KB.Available(ctx); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	raw, _ := args["text"].(string)
	text, err := Sanitize(raw)
	if err != nil {
		return nil, err
	}
	submit := BoolArg(args, "submit")
	app, _ := args["app"].(string)
	tg, err := t.resolve(ctx, app)
	if err != nil {
		return nil, err
	}
	where := "the window in front"
	if tg != nil {
		where = tg.name
	}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.focus(ctx, tg); err != nil {
			return nil, err
		}
		label := "Typing"
		if tg != nil {
			label = "Typing into " + tg.name
		}
		t.show(label + ": " + Preview(text, 120))
		if err := t.KB.Type(ctx, text); err != nil {
			return nil, err
		}
		out := map[string]any{"typed_chars": len([]rune(text)), "into": where, "submitted": false}
		if submit {
			if err := t.KB.Press(ctx, KeyEnter); err != nil {
				return nil, fmt.Errorf("typed the text but couldn't press Enter: %w", err)
			}
			out["submitted"] = true
		}
		t.log().Info("dictation", "tool", ToolType, "into", where, "chars", out["typed_chars"], "submit", submit, "backend", t.KB.Name())
		return out, nil
	}
	gate := GateType
	if submit {
		gate = GateSubmit
	}
	if t.Confirm.Needs(gate) {
		q := fmt.Sprintf("Type %q into %s?", Preview(text, 80), where)
		if submit {
			q = fmt.Sprintf("Send %q to %s?", Preview(text, 80), where)
		}
		return t.Confirm.Ask(q, act), nil
	}
	return act(ctx)
}

func (t *Tools) pressKey(ctx context.Context, args map[string]any) (map[string]any, error) {
	if t.KB == nil {
		return nil, fmt.Errorf("%w: key presses aren't supported here", ErrUnavailable)
	}
	if ok, why := t.KB.Available(ctx); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, why)
	}
	raw, _ := args["key"].(string)
	k, err := ParseKey(raw)
	if err != nil {
		return nil, err
	}
	app, _ := args["app"].(string)
	tg, err := t.resolve(ctx, app)
	if err != nil {
		return nil, err
	}
	where := "the window in front"
	if tg != nil {
		where = tg.name
	}
	act := func(ctx context.Context) (map[string]any, error) {
		if err := t.focus(ctx, tg); err != nil {
			return nil, err
		}
		t.show("Pressing " + keyLabel(k))
		if err := t.KB.Press(ctx, k); err != nil {
			return nil, err
		}
		t.log().Info("dictation", "tool", ToolPress, "into", where, "key", string(k), "backend", t.KB.Name())
		return map[string]any{"pressed": string(k), "into": where}, nil
	}
	// A key press answers a dialog: it is the user's answer, so only "all" asks again.
	if t.Confirm.Needs(GateType) {
		return t.Confirm.Ask(fmt.Sprintf("Press %s in %s?", keyLabel(k), where), act), nil
	}
	return act(ctx)
}

func keyLabel(k Key) string {
	switch k {
	case KeyShiftTab:
		return "Shift+Tab"
	}
	s := string(k)
	return strings.ToUpper(s[:1]) + s[1:]
}
