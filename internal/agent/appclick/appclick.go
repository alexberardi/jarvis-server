// Package appclick is voice "click X" in desktop apps (ELEMENT-LAYER-WORK.md slice 4b): funnel
// stage 1 resolves what was said against the focused app window's accessibility snapshot with
// internal/agent/targets, the same matcher as the browser's, and presses the control there. The
// browser's window is the browser's own (browser.Tools.Direct runs first); typing stays with
// dictation, which confirms focus before every keystroke (agent invariant 27).
package appclick

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// snapshotMax is how many controls of the window a click reads.
const snapshotMax = 150

var (
	// "click Bold", "tap OK", "press the Insert button", "select Table". Not "open" or "go to":
	// on the desktop those open apps and switch tabs (desktop.Tools.Direct).
	reVerb = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+)?(click|tap|press|select|choose|pick)\s+(?:on\s+)?(.+?)[\s.!?]*$`)
	// "Click" as whisper hears it at the start of the last sentence ("Quick bold"): a click only
	// when the rest names a control.
	reHeard = regexp.MustCompile(`(?i)(?:^|[.!?,]\s+)(quick|good|clique|klick|clip)\s+(?:on\s+)?([^.!?,]+?)[\s.!?]*$`)
	// "press enter" is a key (dictation's), not a control.
	reKey = regexp.MustCompile(`(?i)^(?:the\s+)?(?:enter|return|escape|esc|tab|space|backspace|delete|up|down|left|right|home|end)(?:\s+key)?$`)
)

// Source clicks controls in the focused app window by voice. Snapshot and Press are the
// accessibility tree's (atspi.Snapshot, atspi.Press on Linux); Allowed is the computer-control
// switch (clicking another app is using this computer, COMPUTER-CONTROL.md C11). Picker is the
// narrow model pick for descriptions; Confirm asks before a risky control (nil: never asks).
type Source struct {
	Windows  computer.Windows
	Snapshot func(ctx context.Context, pid, max int) (computeruse.Snapshot, error)
	Press    func(gen int, ref string) error
	Allowed  func() bool
	Picker   targets.Picker
	Confirm  *dictate.Confirmer

	mu      sync.Mutex
	confirm string // the risky click waiting for its yes
	// bound is the window in front when the turn began listening, or after the last click:
	// a command never lands in another one (§3, A15 F4).
	bound *computer.DesktopWindow
}

// TurnListening binds the turn to the window in front as it starts listening, a browser's
// included (a turn begun there never clicks in an app, A17 R1); the end unbinds.
func (s *Source) TurnListening(ctx context.Context, on bool) {
	var w *computer.DesktopWindow
	if on {
		if f, ok := s.focused(ctx); ok {
			w = &f
		} else {
			w = &computer.DesktopWindow{} // listening began with no window in front
		}
	}
	s.mu.Lock()
	s.bound = w
	s.mu.Unlock()
}

// Direct carries out a click on a control of the focused app window; ok false passes it on.
func (s *Source) Direct(ctx context.Context, said string) (map[string]any, bool) {
	target, literal, ok := parse(said)
	if !ok || s.Allowed == nil || !s.Allowed() {
		return nil, false
	}
	win, ok := s.front(ctx)
	if !ok {
		return nil, false
	}
	snap, err := s.Snapshot(ctx, win.PID, snapshotMax)
	if err != nil || len(snap.Elements) == 0 {
		return nil, false // no accessible controls: the model's (a computer task can use the screen)
	}
	ref, found := targets.Resolve(snap, target, true)
	if !found {
		p := s.Picker
		if p.Surface == "" {
			p.Surface = "app window"
		}
		switch r, res := p.Pick(ctx, snap, "click "+target); res {
		case targets.Selected:
			ref, found = r, true
		case targets.Uncertain:
			return fail("target_uncertain", "the intended control is ambiguous",
				"I'm not sure which control you mean. Say it again with another detail."), true
		}
	}
	if !found && !literal {
		return nil, false // "Good morning": speech, not a misheard click; the binding stays (A17 R2)
	}
	// A command from here on. It never lands in another window than the turn's: refused once,
	// and re-bound, so said again it acts (A15 F4).
	s.mu.Lock()
	bound := s.bound
	s.bound = &win
	s.mu.Unlock()
	if bound != nil && (bound.ID != win.ID || bound.PID != win.PID) {
		return fail("window_changed", "the window in front changed since listening began",
			"The window changed since you started. Say it again."), true
	}
	if !found {
		return fail("target_not_found", fmt.Sprintf("no control like %q in %s", target, appName(win)),
			"I couldn't find that control. Say it again with another detail."), true
	}
	return s.press(ctx, win, snap, ref), true
}

// AnswerChoice takes the yes or no to a risky click's question.
func (s *Source) AnswerChoice(ctx context.Context, said string) (map[string]any, bool) {
	s.mu.Lock()
	id := s.confirm
	s.confirm = ""
	s.mu.Unlock()
	if id == "" || s.Confirm == nil || !s.Confirm.Pending(id) {
		return nil, false
	}
	out, err := s.Confirm.Answer(ctx, id, said)
	if err != nil {
		return fail("failed", err.Error(), "Nothing was clicked: "+err.Error()), true
	}
	out[node.IslandOnlyKey] = true
	return out, true
}

// press acts on ref in snap, after asking when the control is risky. The yes acts only if the
// same window still has the focus (§3: a change is a stale snapshot, never a retarget).
func (s *Source) press(ctx context.Context, win computer.DesktopWindow, snap computeruse.Snapshot, ref string) map[string]any {
	e := snap.Find(ref)
	label := strings.TrimSpace(e.Name)
	nameless := label == "" // "  " is no name either (A15 F5)
	if nameless {
		label = e.Role
	}
	act := func(ctx context.Context) (map[string]any, error) {
		if s.Allowed == nil || !s.Allowed() {
			// Turned off while a question or a slow pick waited (A15 F2).
			return nil, fmt.Errorf("computer control is off")
		}
		if now, ok := s.front(ctx); !ok || now.ID != win.ID || now.PID != win.PID {
			return nil, fmt.Errorf("the window changed; say it again")
		}
		if err := s.Press(snap.Gen, ref); err != nil {
			return nil, fmt.Errorf("couldn't press %q: %w", label, err)
		}
		return map[string]any{"success": true, "clicked": label, node.IslandKey: "Clicked " + label, node.IslandOnlyKey: true}, nil
	}
	risk := computeruse.Classify(computeruse.Action{Kind: "press"}, e.Element(), computeruse.Target{Kind: "desktop", App: win.App, WindowID: win.ID})
	// The gate as the browser's: an ordinary click is GateType (asked only in "all" mode); a
	// risky control, or one with no name to judge it by, GateSubmit (DESKTOP-COMMANDS.md Q8).
	g := dictate.GateType
	if risk.Level == computeruse.LevelConfirm || nameless {
		g = dictate.GateSubmit
	}
	switch {
	case risk.Level == computeruse.LevelDeny:
		return fail("denied", risk.Reason, "Jarvis won't click "+label)
	case s.Confirm.Needs(g):
		q := fmt.Sprintf("Click %q in %s?", dictate.Preview(label, 60), appName(win))
		out := s.Confirm.Ask(q, act)
		s.mu.Lock()
		s.confirm, _ = out["confirmation_id"].(string)
		s.mu.Unlock()
		out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = dictate.WaitingPrefix+q, true, true
		return out
	}
	out, err := act(ctx)
	if err != nil {
		return fail("failed", err.Error(), "Nothing was clicked: "+err.Error())
	}
	return out
}

// front is the focused ordinary window with a process, unless it is a browser's.
func (s *Source) front(ctx context.Context) (computer.DesktopWindow, bool) {
	w, ok := s.focused(ctx)
	return w, ok && !winmatch.IsBrowser([]string{w.App})
}

// focused is the focused ordinary window with a process, whatever app it is.
func (s *Source) focused(ctx context.Context) (computer.DesktopWindow, bool) {
	ws, err := s.Windows.List(ctx)
	if err != nil {
		return computer.DesktopWindow{}, false
	}
	for _, w := range ws {
		if w.Focused && w.Layer == 0 && w.PID > 0 {
			return w, true
		}
	}
	return computer.DesktopWindow{}, false
}

// parse is the control a click names, and whether "click" was said (literal) or misheard.
func parse(said string) (target string, literal bool, ok bool) {
	if m := reVerb.FindStringSubmatch(said); m != nil {
		t := strings.TrimSpace(m[2])
		if strings.EqualFold(m[1], "press") && reKey.MatchString(t) {
			return "", false, false
		}
		return t, true, true
	}
	if m := reHeard.FindStringSubmatch(said); m != nil {
		return strings.TrimSpace(m[2]), false, true
	}
	return "", false, false
}

func appName(w computer.DesktopWindow) string {
	if w.Title != "" {
		return w.Title
	}
	return w.App
}

func fail(code, err, shown string) map[string]any {
	return map[string]any{"success": false, "error_code": code, "error": err,
		node.IslandKey: dictate.Preview(shown, 80), node.IslandOnlyKey: true, node.ChoiceKey: true}
}

// Available and Call: no model tools; clicks are stage 1 only (computer tasks have their own).
func (s *Source) Available(context.Context) []node.ToolDef { return nil }

func (s *Source) Call(context.Context, string, map[string]any, node.Request) (map[string]any, bool) {
	return nil, false
}

var (
	_ node.Toolset        = (*Source)(nil)
	_ node.Director       = (*Source)(nil)
	_ node.ChoiceAnswerer = (*Source)(nil)
	_ node.TurnWatcher    = (*Source)(nil)
)
