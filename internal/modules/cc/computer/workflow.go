package computer

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Taught workflows as the replay sees them (plan P3 pieces 26 and 31). cc stores them
// (cc_taught_workflows, cc/workflows.go, whose WStep, StepWindow, RecordedElement and Param are these types)
// and the replay (replay.go) runs them.

const (
	// RecordedRef is the ref of a learnt press, select or set_value: there is no snapshot at
	// learning time, so the replay finds the element again from the step's Element.
	RecordedRef = "recorded"
	// MaskedValue stands for a value the recorder masked (a password, a card number): the step
	// types it as this, and the replay asks the user to enter the real one.
	MaskedValue = "•••"
)

// Workflow is a taught workflow: its steps, the values it asks for and how to check it worked.
type Workflow struct {
	ID       string
	Name     string
	Slug     string
	Triggers []string
	Params   []WorkflowParam
	Steps    []WorkflowStep
	Success  SuccessCheck
	Version  int
}

// WorkflowParam is a value the workflow asks for at replay ("which month?").
type WorkflowParam struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // date | month | text | number | file
	Example     string `json:"example,omitempty"`
	Description string `json:"description,omitempty"`
}

// WorkflowStep is one step of a workflow: the action as recorded, and the element it landed on
// so a replay can find it again when the page has moved.
type WorkflowStep struct {
	Intent  string             `json:"intent"`
	Target  string             `json:"target"` // browser | desktop
	Site    string             `json:"site,omitempty"`
	App     string             `json:"app,omitempty"`
	Window  *StepWindow        `json:"window,omitempty"` // desktop: the recorded window, when known
	Action  computeruse.Action `json:"action"`
	Element *RecordedElement   `json:"element,omitempty"`
	Risk    string             `json:"risk,omitempty"` // a computeruse risk category; "" is none
}

// StepWindow is the window a desktop step was recorded in: its class (the app id on Wayland)
// and title.
type StepWindow struct {
	Class string `json:"class,omitempty"`
	Title string `json:"title,omitempty"`
}

// RecordedElement is what the recorder saw of the element a step acted on.
type RecordedElement struct {
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	Selector string `json:"selector,omitempty"`
	Frame    string `json:"frame,omitempty"`
	AXPath   string `json:"ax_path,omitempty"`
}

// Heal is a step whose element moved: the element the replay acted on in its place.
type Heal struct {
	Step    int // index in Workflow.Steps
	Was     RecordedElement
	Element RecordedElement
}

// WorkflowStore keeps what a replay learnt: the new element of every healed step.
type WorkflowStore interface {
	// Heal stores heals in workflow id, as long as it is still at version (an edit since wins),
	// and bumps its version.
	Heal(ctx context.Context, id string, version int, heals []Heal) error
}

// target is the step's computer target.
func (s WorkflowStep) target() computeruse.Target {
	kind := s.Target
	if kind != "desktop" {
		kind = "browser"
	}
	return computeruse.Target{Kind: kind, Site: s.Site, App: s.App}
}

var placeholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Missing are the workflow's params without a value in params, in the workflow's order.
func (w *Workflow) Missing(params map[string]string) []string {
	var out []string
	for _, p := range w.Params {
		if strings.TrimSpace(params[p.Name]) == "" {
			out = append(out, p.Name)
		}
	}
	return out
}

// Bind is the workflow's steps with every {param} replaced by its value: in the intents, the
// typed and selected text and the addresses (escaped for where it stands in the URL). A param
// the workflow declares without a value is an error.
func (w *Workflow) Bind(params map[string]string) ([]WorkflowStep, error) {
	if miss := w.Missing(params); len(miss) > 0 {
		return nil, fmt.Errorf("I need the %s to run %q", strings.Join(miss, " and "), w.Name)
	}
	declared := map[string]string{}
	for _, p := range w.Params {
		declared[p.Name] = strings.TrimSpace(params[p.Name])
	}
	sub := func(s string, esc func(v string, at int) string) string {
		var b strings.Builder
		last := 0
		for _, m := range placeholderRe.FindAllStringSubmatchIndex(s, -1) {
			v, ok := declared[s[m[2]:m[3]]]
			if !ok {
				continue
			}
			if esc != nil {
				v = esc(v, m[0])
			}
			b.WriteString(s[last:m[0]])
			b.WriteString(v)
			last = m[1]
		}
		return b.String() + s[last:]
	}
	steps := make([]WorkflowStep, len(w.Steps))
	for i, st := range w.Steps {
		st.Intent = sub(st.Intent, nil)
		a := st.Action
		a.Intent = sub(firstNonEmpty(a.Intent, st.Intent), nil)
		a.Text = sub(a.Text, nil)
		a.URL = sub(a.URL, func(v string, at int) string {
			if q := strings.Index(a.URL, "?"); q >= 0 && at > q {
				return url.QueryEscape(v)
			}
			return url.PathEscape(v)
		})
		a.Keys = slices.Clone(a.Keys)
		if st.Element != nil {
			el := *st.Element
			st.Element = &el
		}
		st.Action = a
		steps[i] = st
	}
	return steps, nil
}

// MatchWorkflows finds the workflows q names: by name, slug or trigger phrase, compared
// without case, punctuation or a leading "the"/"my" and a trailing "workflow". Exact matches win;
// otherwise a name or phrase that q contains, or that contains q. More than one is ambiguous:
// the caller asks which.
func MatchWorkflows(ws []Workflow, q string) []Workflow {
	key := workflowKey(q)
	if key == "" {
		return nil
	}
	var exact, near []Workflow
	for _, w := range ws {
		names := append([]string{w.Name, w.Slug}, w.Triggers...)
		hit, close := false, false
		for _, n := range names {
			k := workflowKey(n)
			switch {
			case k == "":
			case k == key:
				hit = true
			case len(k) >= 4 && len(key) >= 4 && (strings.Contains(key, k) || strings.Contains(k, key)):
				close = true
			}
		}
		switch {
		case hit:
			exact = append(exact, w)
		case close:
			near = append(near, w)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return near
}

// workflowKey is a name as MatchWorkflows compares it: "Run the 'Bank statement' workflow" →
// "bankstatement".
func workflowKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Trim(s, "\"'`“”‘’ .!?")
	for changed := true; changed; {
		changed = false
		for _, p := range []string{"run ", "the ", "my ", "our ", "this ", "that "} {
			if strings.HasPrefix(s, p) {
				s, changed = strings.TrimSpace(s[len(p):]), true
			}
		}
		for _, x := range []string{" workflow", " routine", " macro"} {
			if strings.HasSuffix(s, x) && len(s) > len(x) {
				s, changed = strings.TrimSpace(s[:len(s)-len(x)]), true
			}
		}
		if t := strings.Trim(s, "\"'`“”‘’ .!?"); t != s {
			s, changed = t, true
		}
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
