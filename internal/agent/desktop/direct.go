package desktop

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Funnel stage 1 (VOICE-FLOWS.md Q27): a clear one-step command, said whole, is carried out here
// against what is open now, with no model. Plain code over a few dozen names, so it runs on any
// PC. Sure: act. Several fit: ask on the island. Anything else goes on to command-center.
//
// v1 is switching: "switch to / go to / show me X", where X is a tab or sheet (views) or a
// window. "X tab" or "in Firefox" means a view; "X window" or "X app" a window.

var reSwitch = regexp.MustCompile(`(?i)^\s*(?:(?:hey\s+)?jarvis[,.]?\s+)?(?:please\s+|can you\s+|could you\s+)?` +
	`(?:switch|go|jump|flip|change|move)(?:\s+(?:over|back))?\s+to\s+(?:the\s+|my\s+)?(.+?)[\s.!?]*$`)

var (
	reInApp    = regexp.MustCompile(`(?i)^(.+?)\s+(?:in|on)\s+(?:the\s+|my\s+)?(.+)$`)
	reViewWord = regexp.MustCompile(`(?i)\s+(?:tab|sheet|worksheet)$`)
	reWinWord  = regexp.MustCompile(`(?i)\s+(?:window|app|application)$`)
	// reCompound: "switch to X and type Y" is more than one step: the model's.
	reCompound = regexp.MustCompile(`(?i)\b(?:and|then)\b`)
)

// switchRequest is a parsed "switch to X".
type switchRequest struct {
	target  string
	app     string // "in Firefox"
	views   bool   // a view may be meant
	windows bool   // a window may be meant
}

func parseSwitch(said string) (switchRequest, bool) {
	m := reSwitch.FindStringSubmatch(said)
	if m == nil || reCompound.MatchString(m[1]) {
		return switchRequest{}, false
	}
	r := switchRequest{target: strings.TrimSpace(m[1]), views: true, windows: true}
	if im := reInApp.FindStringSubmatch(r.target); im != nil {
		r.target, r.app, r.windows = strings.TrimSpace(im[1]), strings.TrimSpace(im[2]), false
	}
	switch {
	case reViewWord.MatchString(r.target):
		r.target, r.windows = reViewWord.ReplaceAllString(r.target, ""), false
	case reWinWord.MatchString(r.target):
		r.target, r.views = reWinWord.ReplaceAllString(r.target, ""), false
	}
	ref := strings.TrimSuffix(strings.ToLower(r.target), " one")
	if r.target == "" || slices.Contains(itWords, ref) || slices.Contains(otherWords, ref) {
		return switchRequest{}, false // "switch to it", "the other one": the model has the context
	}
	return r, true
}

// Direct carries out a clear one-step command (funnel stage 1); ok is false to pass it on.
func (t *Tools) Direct(ctx context.Context, said string) (map[string]any, bool) {
	r, ok := parseSwitch(said)
	if !ok {
		return nil, false
	}
	ctx = context.WithValue(ctx, saidKey{}, said)
	var view *View
	var viewCands []View
	var appName string
	r.views = r.views && node.Enabled(ctx, ToolView)
	r.windows = r.windows && node.Enabled(ctx, ToolWindow)
	if r.views && t.Views != nil {
		var views []View
		var err error
		if appName, views, err = t.Views.Views(ctx, r.app); err == nil {
			view, viewCands = matchView(views, r.target)
		}
	}
	var win *host.Window
	if r.windows && t.Host != nil {
		if w, found, err := t.resolveWindow(ctx, r.target); err == nil && found {
			win = &w
		}
	}
	if win != nil && view != nil && strings.EqualFold(win.App, appName) {
		win = nil // the browser's window is titled by the tab it shows: that's the tab
	}
	switch {
	case win != nil && view != nil:
		// "Discord" is an app's window and a tab: ask which (Q24).
		v, w := view, *win
		out := node.Fail("%q is both a window and a tab", r.target)
		out["error_code"] = "ambiguous"
		t.directChoice(out, []string{windowName(w) + " window", shortView(v.Name) + " tab in " + appName},
			[]func(context.Context) (map[string]any, error){
				func(ctx context.Context) (map[string]any, error) { return t.focusWindow(ctx, w) },
				func(ctx context.Context) (map[string]any, error) { return t.doSwitchView(ctx, r.app, appName, *v) },
			})
		return out, true
	case view != nil:
		out, err := t.doSwitchView(ctx, r.app, appName, *view)
		return t.directResult(out, err), true
	case len(viewCands) > 0 && win == nil:
		out := node.Fail("no tab or sheet in %s is clearly %q", appName, r.target)
		out["error_code"] = "ambiguous_view"
		t.viewChoice(out, r.app, viewCands)
		return out, true
	case win != nil:
		out, err := t.focusWindow(ctx, *win)
		return t.directResult(out, err), true
	}
	return nil, false
}

func (t *Tools) focusWindow(ctx context.Context, w host.Window) (map[string]any, error) {
	out, err := t.window(ctx, map[string]any{"action": string(host.ActionFocus), "window": w.ID})
	if err == nil {
		out["success"] = true // the voice loop chimes an error on a result without it
	}
	return out, err
}

func (t *Tools) directResult(out map[string]any, err error) map[string]any {
	if err == nil {
		return out
	}
	o := node.Fail("%v", err)
	o["error_code"] = code(err)
	t.island(o, "Couldn't switch: "+err.Error(), true)
	return o
}

// directChoice numbers mixed choices (a window or a tab) on the island; the answer runs one.
func (t *Tools) directChoice(out map[string]any, labels []string, acts []func(context.Context) (map[string]any, error)) {
	var b strings.Builder
	b.WriteString("Which one?")
	for i, l := range labels {
		fmt.Fprintf(&b, "  %d. %s", i+1, l)
	}
	out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = b.String(), true, true
	t.mu.Lock()
	t.mixedChoices, t.choicesAt = acts, t.now()
	t.choices, t.choiceTool, t.choiceArgs, t.viewChoices = nil, "", nil, nil
	t.mu.Unlock()
	if t.Show != nil {
		t.Show(b.String(), choiceFor)
	}
}

// answerMixed is AnswerChoice for a mixed list: the place said.
func (t *Tools) answerMixed(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	acts, at := t.mixedChoices, t.choicesAt
	t.mu.Unlock()
	if len(acts) == 0 || t.now().Sub(at) > choiceValidFor {
		return nil, false
	}
	n := answerOrdinal(said)
	if n == -1 {
		n = len(acts)
	}
	if n < 1 || n > len(acts) {
		return nil, false
	}
	t.mu.Lock()
	t.mixedChoices = nil
	t.mu.Unlock()
	out, err := acts[n-1](ctx)
	return t.directResult(out, err), true
}
