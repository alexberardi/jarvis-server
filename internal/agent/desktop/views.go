package desktop

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// Views inside an app window: tabs, worksheets, an editor's or terminal's tabs (VOICE-FLOWS.md
// Q26). One way for all of them: the names come from the OS accessibility tree, our code
// matches what was heard (spelling and sound), and an unclear name is a numbered choice on the
// island, never a guess.

const (
	ToolViews = "desktop_views"
	ToolView  = "desktop_view"
)

// View is one named view in an app's windows (the front one's first), in the order the app
// shows them.
type View struct {
	Index    int
	Name     string
	Selected bool
	Window   string // the title of the window it is in, when the app has several
}

// ViewSource lists and switches the views of an app's front window. app is the app as said
// ("firefox"), "" for the focused window; Views answers with the app's name.
type ViewSource interface {
	Views(ctx context.Context, app string) (string, []View, error)
	Switch(ctx context.Context, app string, v View) error
}

// viewScore is how well heard names title: the best Similarity over the title's parts ("Web
// analytics | … | Cloudflare") and over runs of its words as long as what was heard, so a
// short name finds itself in a long title.
func viewScore(heard, title string) float64 {
	hw := viewWords(heard)
	if len(hw) == 0 {
		return 0
	}
	h := strings.Join(hw, " ")
	best := winmatch.Similarity(h, title)
	for _, part := range strings.FieldsFunc(title, func(r rune) bool { return strings.ContainsRune("|·—–:", r) }) {
		best = max(best, winmatch.Similarity(h, part))
	}
	tw := viewWords(title)
	for n := max(1, len(hw)-1); n <= len(hw)+1; n++ {
		for i := 0; i+n <= len(tw); i++ {
			best = max(best, winmatch.Similarity(h, strings.Join(tw[i:i+n], " ")))
		}
	}
	return best
}

// viewWords are s's words without what a request wraps a view's name in ("the Claude tab").
func viewWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !viewFiller[w] {
			out = append(out, w)
		}
	}
	return out
}

var viewFiller = map[string]bool{"the": true, "my": true, "tab": true, "tabs": true, "sheet": true,
	"worksheet": true, "page": true, "view": true, "one": true}

const (
	viewSure   = 0.8  // a match this good, clearly ahead of the next, is switched to
	viewLikely = 0.65 // a match this good with nothing else near is switched to too
	viewNear   = 0.15 // "clearly ahead": no other view within this
	viewMaybe  = 0.55 // below this nothing came close: every view is offered
	viewChoice = 5    // at most this many choices
)

// matchView picks the view heard names: one (switch), or candidates to choose from, best
// first: those near the best, or none when nothing comes close. Scores from real use
// (2026-10-25): "Cloud Fair" is 0.78 for Cloudflare and 0.51 for Claude (switch); "Claude pad"
// is 0.66 for Claude and 0.56 for Cloudflare (ask which).
func matchView(views []View, heard string) (*View, []View) {
	type scored struct {
		v View
		s float64
	}
	var all []scored
	for _, v := range views {
		all = append(all, scored{v, viewScore(heard, v.Name)})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s > all[j].s })
	if len(all) == 0 || all[0].s < viewMaybe {
		return nil, nil
	}
	best := all[0].s
	alone := len(all) == 1 || best-all[1].s >= viewNear
	if alone && best >= viewLikely {
		return &all[0].v, nil
	}
	var cands []View
	for _, a := range all {
		if a.s >= best-viewNear && len(cands) < viewChoice {
			cands = append(cands, a.v)
		}
	}
	return nil, cands
}

func viewsSpecs() []node.Spec {
	app := node.Param{Name: "app", Type: "string", Description: "The app as the user said it ('firefox', 'excel'). Leave out for the window in front"}
	return []node.Spec{
		{Name: ToolViews, Description: "List the tabs or sheets inside an app's window (browser tabs, spreadsheet sheets, editor and terminal tabs): answers 'what tabs are open'.",
			Params:   []node.Param{app},
			Keywords: []string{"tabs", "tab", "sheets", "sheet", "worksheet", "open"}},
		{Name: ToolView, Description: "Switch to a tab or sheet inside an app's window by its name, as the user said it ('switch to the Claude tab', 'go to the budget sheet'). Pass the name as heard: it is matched by sound, and the user is asked when it's unclear.",
			Params:   []node.Param{{Name: "name", Type: "string", Required: true, Description: "The tab or sheet as the user said it"}, app},
			Keywords: []string{"switch", "tab", "sheet", "worksheet", "go to"},
			Examples: []node.Example{
				{VoiceCommand: "switch to the Claude tab", Params: map[string]any{"name": "Claude"}, Primary: true},
				{VoiceCommand: "go to the budget sheet in excel", Params: map[string]any{"name": "budget", "app": "excel"}},
			}},
	}
}

func (t *Tools) listViews(ctx context.Context, args map[string]any) (map[string]any, error) {
	app, _ := args["app"].(string)
	name, views, err := t.Views.Views(ctx, strings.TrimSpace(app))
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, len(views))
	for i, v := range views {
		list[i] = map[string]any{"name": v.Name, "selected": v.Selected}
	}
	return map[string]any{"success": true, "app": name, "views": list, "count": len(views)}, nil
}

func (t *Tools) switchView(ctx context.Context, args map[string]any) (map[string]any, error) {
	heard, _ := args["name"].(string)
	app, _ := args["app"].(string)
	app = strings.TrimSpace(app)
	appName, views, err := t.Views.Views(ctx, app)
	if err != nil {
		return nil, err
	}
	if len(views) == 0 {
		out := node.Fail("%s shows no tabs or sheets that can be switched by name", appName)
		out["error_code"] = "no_views"
		return out, nil
	}
	if v, ok := t.pickViewChoice(ctx, heard); ok {
		return t.doSwitchView(ctx, app, appName, v)
	}
	v, cands := matchView(views, heard)
	if v != nil {
		return t.doSwitchView(ctx, app, appName, *v)
	}
	out := node.Fail("no tab or sheet in %s is clearly %q", appName, heard)
	out["error_code"] = "ambiguous_view"
	if len(cands) == 0 {
		cands = views // nothing close: offer them all (the name may be misheard)
		if len(cands) > viewChoice {
			cands = cands[:viewChoice]
		}
		out["error_code"] = "view_not_found"
	}
	t.viewChoice(out, app, cands)
	return out, nil
}

func (t *Tools) doSwitchView(ctx context.Context, app, appName string, v View) (map[string]any, error) {
	if err := t.Views.Switch(ctx, app, v); err != nil {
		return nil, err
	}
	out := map[string]any{"success": true, "app": appName, "switched_to": v.Name}
	t.island(out, "Switched to "+shortView(v.Name), false)
	return out, nil
}

// viewChoice numbers the candidates on the island; the answer ("the second one", or a name)
// comes in the choice window (AnswerChoice) or as the next call.
func (t *Tools) viewChoice(out map[string]any, app string, cands []View) {
	var b strings.Builder
	b.WriteString("Which one?")
	numbered := make([]map[string]any, len(cands))
	for i, v := range cands {
		fmt.Fprintf(&b, "  %d. %s", i+1, shortView(v.Name))
		numbered[i] = map[string]any{"n": i + 1, "name": v.Name}
	}
	out[node.IslandKey], out[node.IslandOnlyKey], out[node.ChoiceKey] = b.String(), true, true
	out["numbered"] = numbered
	t.mu.Lock()
	t.viewChoices, t.viewChoiceApp, t.choicesAt = cands, app, t.now()
	t.choices, t.choiceTool, t.choiceArgs = nil, "", nil // one list at a time
	t.mu.Unlock()
	if t.Show != nil {
		t.Show(b.String(), choiceFor)
	}
}

// pickViewChoice: the view at the place an ordinal names ("the second one"), against a fresh list.
func (t *Tools) pickViewChoice(ctx context.Context, heard string) (View, bool) {
	t.mu.Lock()
	list, at := t.viewChoices, t.choicesAt
	t.mu.Unlock()
	if len(list) == 0 || t.now().Sub(at) > choiceValidFor {
		return View{}, false
	}
	n := ordinal(heard)
	if n == 0 {
		said, _ := ctx.Value(saidKey{}).(string)
		n = ordinal(said)
	}
	if n == -1 {
		n = len(list)
	}
	if n < 1 || n > len(list) {
		return View{}, false
	}
	return list[n-1], true
}

// answerView is AnswerChoice for a view list: the place said, else a name among the choices.
func (t *Tools) answerView(ctx context.Context, said string) (map[string]any, bool) {
	t.mu.Lock()
	list, app, at := t.viewChoices, t.viewChoiceApp, t.choicesAt
	t.mu.Unlock()
	if len(list) == 0 || t.now().Sub(at) > choiceValidFor {
		return nil, false
	}
	var pick *View
	if n := answerOrdinal(said); n != 0 {
		if n == -1 {
			n = len(list)
		}
		if n < 1 || n > len(list) {
			return nil, false
		}
		pick = &list[n-1]
	} else if v, _ := matchView(list, said); v != nil {
		pick = v
	}
	if pick == nil {
		return nil, false
	}
	t.mu.Lock()
	t.viewChoices = nil
	t.mu.Unlock()
	appName := app
	out, err := t.doSwitchView(ctx, app, appName, *pick)
	if err != nil {
		o := node.Fail("%v", err)
		o["error_code"] = code(err)
		return o, true
	}
	return out, true
}

// shortView is a view's name for the island: long page titles cut at a word.
func shortView(s string) string {
	const max = 48
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndex(cut, " "); i > max/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// ViewVocabulary is the front window's view names for speech-to-text, cut at their separators
// ("New session - Claude" → "New session", "Claude"), so "switch to the Claude tab" is heard as
// Claude. Nothing when there's no view source or it can't read the window.
func (t *Tools) ViewVocabulary(ctx context.Context) []string {
	if t.Views == nil {
		return nil
	}
	_, views, err := t.Views.Views(ctx, "")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range views {
		for _, part := range strings.FieldsFunc(v.Name, func(r rune) bool { return strings.ContainsRune("|·—–:-", r) }) {
			part = strings.TrimSpace(part)
			if part == "" || len([]rune(part)) > 30 || seen[strings.ToLower(part)] {
				continue
			}
			seen[strings.ToLower(part)] = true
			out = append(out, part)
		}
	}
	return out
}
