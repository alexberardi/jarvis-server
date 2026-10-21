package dictate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
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

// ShowFor is how long the island shows what was typed; ShowFailFor, why nothing was.
const (
	ShowFor     = 2500 * time.Millisecond
	ShowFailFor = 5 * time.Second
)

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
	// Sleep waits between focus and window checks; nil: a real timer (tests pass one that
	// returns at once).
	Sleep func(ctx context.Context, d time.Duration) error
	// Launched is desktop_open's launch record: typing into an app launched moments ago waits
	// for its window (nil: never waits).
	Launched *launches.Tracker
	// Apps is the installed-app catalogue, to tell "not open" from "no such app" (nil: not told).
	Apps func() ([]apps.App, error)
	// WindowWait bounds the wait for a just-launched app's window (0: DefaultWindowWait).
	WindowWait time.Duration
	// JarvisPage says whether front (nil: the backend can't tell which window is in front)
	// shows the Jarvis web page of this agent's server. Typing with no app named then goes to
	// the window used before it, never into the chat (jarvispage.go). Nil: no guard.
	JarvisPage func(ctx context.Context, front *Window) bool
}

// Open, then type (DESIGN.md §14): a launch is fire-and-forget, so typing into an app launched
// within LaunchRecent waits up to WindowWait for its window, checking every windowPoll.
//
// LaunchImplicit: a typing call that names no app within this long of a launch is for the app
// launched (the model's "open X" then "type Y" leaves app out; the window in front is still
// the old one).
const (
	LaunchImplicit    = 15 * time.Second
	LaunchRecent      = time.Minute
	DefaultWindowWait = 10 * time.Second
	windowPoll        = 250 * time.Millisecond
)

var appParam = node.Param{Name: "app", Type: "string",
	Description: "The app the user named ('in slack', 'into X'); always set when named"}

func typeSpec() node.Spec {
	return node.Spec{Name: ToolType,
		Description: "Type text into an app on the computer ('type X', 'write X in slack', 'tell claude to X'); it switches to the named app itself, no need to open it first. Web page fields: browser_fill.",
		Params: []node.Param{
			{Name: "text", Type: "string", Required: true, Description: "Only the words to type"},
			appParam,
			{Name: "submit", Type: "bool", Description: "Press Enter after ('tell X to', 'send', 'and press enter')"},
		},
		Keywords: []string{"type", "dictate", "write", "tell", "send", "enter", "keyboard", "claude", "terminal", "message"},
		Examples: []node.Example{
			{VoiceCommand: "tell claude to test def", Params: map[string]any{"app": "claude", "text": "test def", "submit": true}, Primary: true},
			{VoiceCommand: "type hello world", Params: map[string]any{"text": "hello world"}},
			{VoiceCommand: "write see you at five in slack", Params: map[string]any{"app": "slack", "text": "see you at five"}},
			{VoiceCommand: "type into quillpad that the build is green", Params: map[string]any{"app": "quillpad", "text": "the build is green"}},
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
		Description: "Press a key on the computer ('press enter', 'hit cancel', 'press escape'): enter = OK, escape = cancel. Web page buttons: browser_click.",
		Params: []node.Param{
			{Name: "key", Type: "string", Required: true, Enum: keys, Description: ""},
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
		// A failure is spelled out for the model (small models have told the user "done"
		// over a bare error), shown on the island whatever the model says, and logged by code.
		code := Code(err)
		o := node.Fail("%s", FailureText(name, err))
		o["error_code"] = code
		var we *winmatch.Error
		if errors.As(err, &we) {
			if len(we.Candidates) > 0 {
				o["candidates"] = we.Candidates
			}
			if len(we.Apps) > 0 {
				o["open_apps"] = we.Apps
			}
		}
		if t.Show != nil {
			t.Show(FailureNote(name, err), ShowFailFor)
		}
		t.log().Info("dictation failed", "tool", name, "error_code", code)
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

var (
	errFocus   = errors.New("focus not confirmed")
	errSubmit  = errors.New("enter not pressed")
	errNoFront = errors.New("no window in front")
)

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
	case errors.Is(err, errSubmit):
		return "submit_failed"
	case errors.Is(err, errNoFront):
		return "no_focused_window"
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
	id    string
	name  string // the app's display name, for the island and the result
	app   string // the app's own name ("omawrite"), for the result
	fuzzy bool   // picked by a near spelling of what the user said (a misheard name)
	asked string // what the user called it
	// insteadOfPage: no app was named and the Jarvis page was in front, so this is the window
	// used before it (jarvispage.go).
	insteadOfPage bool
}

// resolve finds the one window the user named. No name: the window in front (nothing to do).
func (t *Tools) resolve(ctx context.Context, app string) (*target, error) {
	if winmatch.Normalize(app) == "" {
		// "Open X and type Y": a model often leaves app out of the typing call. Right after a
		// launch, the window in front is still the old one; the text is for the new app.
		if l, ok := t.Launched.Recent(LaunchImplicit); ok && t.Win != nil {
			if ok, _ := t.Win.Available(ctx); ok {
				t.log().Info("dictation: no app named; typing into the app just launched", "app", l.Name)
				return t.awaitWindow(ctx, l.Name, l)
			}
		}
		// The Jarvis web page in front is the chat the user is typing in: not a target.
		return t.awayFromJarvisPage(ctx)
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
	m, err := winmatch.PickUniqueMatch(cands, app, focused)
	var we *winmatch.Error
	if err != nil && errors.As(err, &we) && we.Code == winmatch.CodeNotFound {
		if l, ok := t.Launched.Recent(LaunchRecent); ok && launchedIs(l, app) {
			return t.awaitWindow(ctx, app, l)
		}
		return nil, t.notOpen(app, err)
	}
	if err != nil {
		return nil, err
	}
	return t.targetOf(ws[m.Index], app, m.Fuzzy), nil
}

func (t *Tools) targetOf(w Window, asked string, fuzzy bool) *target {
	c := winmatch.Candidate{Apps: w.Apps, Title: w.Title, Recency: w.Recency, Visible: w.Visible}
	tg := &target{id: w.ID, name: winmatch.DisplayName(c), app: c.App(), fuzzy: fuzzy, asked: strings.TrimSpace(asked)}
	if i := strings.LastIndex(tg.app, "."); i >= 0 && i < len(tg.app)-1 {
		tg.app = tg.app[i+1:]
	}
	if fuzzy {
		t.log().Info("dictation: near spelling", "app", tg.app)
	}
	return tg
}

// launchedIs: the name the user gave is the app launched (as said, or misheard the way
// desktop_open would resolve it).
func launchedIs(l launches.Launch, asked string) bool {
	_, err := apps.Find([]apps.App{{Name: l.Name, ID: l.ID}}, asked)
	return err == nil
}

// awaitWindow waits for a just-launched app's window: one the name matches (as said, or the
// launched app's own name), or else the one window that appeared since the launch (a
// terminal app's window is the terminal's). Several new windows and no name match are
// ambiguous; none within the wait is app_not_ready. Nothing is typed in either case.
func (t *Tools) awaitWindow(ctx context.Context, asked string, l launches.Launch) (*target, error) {
	wait := t.WindowWait
	if wait <= 0 {
		wait = DefaultWindowWait
	}
	tries := max(1, int(wait/windowPoll))
	for i := 0; ; i++ {
		ws, err := t.Win.List(ctx)
		if err != nil {
			return nil, err
		}
		cands := make([]winmatch.Candidate, len(ws))
		for j, w := range ws {
			cands[j] = winmatch.Candidate{Apps: w.Apps, Title: w.Title, Recency: w.Recency, Visible: w.Visible}
		}
		for _, q := range []string{asked, l.Name} {
			if m, err := winmatch.PickUniqueMatch(cands, q, -1); err == nil {
				t.log().Info("dictation: waited for the launched app's window", "app", l.Name, "polls", i)
				return t.targetOf(ws[m.Index], asked, m.Fuzzy || q != asked), nil
			}
		}
		if l.Before != nil {
			var fresh []Window
			for _, w := range ws {
				if !l.Before[w.ID] {
					fresh = append(fresh, w)
				}
			}
			if len(fresh) == 1 {
				t.log().Info("dictation: the launched app's new window", "app", l.Name, "polls", i)
				tg := t.targetOf(fresh[0], asked, false)
				tg.name, tg.app = l.Name, l.Name
				return tg, nil
			}
		}
		if i+1 >= tries {
			break
		}
		if err := t.sleep(ctx, windowPoll); err != nil {
			return nil, err
		}
	}
	return nil, &appError{code: CodeNotReady, app: l.Name,
		msg: fmt.Sprintf("%s was opened, but its window hasn't appeared yet", l.Name)}
}

// notOpen turns "no window matches" into "that app is installed but not open" when the
// catalogue knows the name, so the model can open it first.
func (t *Tools) notOpen(asked string, notFound error) error {
	if t.Apps == nil {
		return notFound
	}
	list, err := t.Apps()
	if err != nil {
		return notFound
	}
	p, err := apps.Find(list, asked)
	if err != nil {
		return notFound
	}
	return &appError{code: CodeNotOpen, app: p.App.Name,
		msg: fmt.Sprintf("%s is installed but not open", p.App.Name)}
}

// Error codes of a named app that has no window (yet).
const (
	CodeNotOpen  = "app_not_open"  // installed, no window: open it with desktop_open first
	CodeNotReady = "app_not_ready" // launched moments ago, its window didn't appear in time
)

// appError is a named app with no window to type into.
type appError struct {
	code, app, msg string
}

func (e *appError) Error() string     { return e.msg }
func (e *appError) ErrorCode() string { return e.code }

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
		return fmt.Errorf("%w: couldn't bring %s to the front (%v)", errFocus, tg.name, err)
	}
	for range focusTries {
		if active, err := t.Win.Active(ctx); err == nil && t.Win.Same(tg.id, active) {
			return nil
		}
		if err := t.sleep(ctx, focusPoll); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: asked %s to come to the front, but it didn't get the keyboard focus", errFocus, tg.name)
}

func (t *Tools) sleep(ctx context.Context, d time.Duration) error {
	if t.Sleep != nil {
		return t.Sleep(ctx, d)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// front names the window in front, when the window backend can tell, so the result says
// where the keys went; and when it can tell that no window has the keyboard focus, typing
// would go nowhere, so that is an error.
func (t *Tools) front(ctx context.Context) (name, app string, err error) {
	if t.Win == nil {
		return "", "", nil
	}
	if ok, _ := t.Win.Available(ctx); !ok {
		return "", "", nil
	}
	active, aerr := t.Win.Active(ctx)
	if aerr != nil {
		return "", "", nil // can't tell: the keys go to whatever has focus, as before
	}
	if active == "" {
		return "", "", fmt.Errorf("%w: no window has the keyboard focus; ask the user to click into the app, or to name it", errNoFront)
	}
	ws, lerr := t.Win.List(ctx)
	if lerr != nil {
		return "", "", nil
	}
	for _, w := range ws {
		if t.Win.Same(w.ID, active) {
			c := winmatch.Candidate{Apps: w.Apps, Title: w.Title, Recency: w.Recency, Visible: w.Visible}
			return winmatch.DisplayName(c), c.App(), nil
		}
	}
	return "", "", nil
}

// inFront fills where and the result for typing into the window in front.
func (t *Tools) inFront(ctx context.Context, where *string, out map[string]any) error {
	name, app, err := t.front(ctx)
	if err != nil {
		return err
	}
	if name != "" {
		*where = name + " (the window in front)"
		out["front_app"] = app
	}
	return nil
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
		out := map[string]any{"submitted": false}
		where := where
		if tg == nil {
			if err := t.inFront(ctx, &where, out); err != nil {
				return nil, err
			}
		}
		label := "Typing"
		if tg != nil {
			label = "Typing into " + tg.name
		}
		t.show(label + ": " + Preview(text, 120))
		if err := t.KB.Type(ctx, text); err != nil {
			return nil, err
		}
		out["typed_chars"], out["into"] = len([]rune(text)), where
		tg.report(out)
		if submit {
			if err := t.KB.Press(ctx, KeyEnter); err != nil {
				return nil, fmt.Errorf("%w: %v", errSubmit, err)
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
		out := map[string]any{}
		where := where
		if tg == nil {
			if err := t.inFront(ctx, &where, out); err != nil {
				return nil, err
			}
		}
		t.show("Pressing " + keyLabel(k))
		if err := t.KB.Press(ctx, k); err != nil {
			return nil, err
		}
		t.log().Info("dictation", "tool", ToolPress, "into", where, "key", string(k), "backend", t.KB.Name())
		out["pressed"], out["into"] = string(k), where
		tg.report(out)
		return out, nil
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
