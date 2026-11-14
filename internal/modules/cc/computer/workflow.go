package computer

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
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
	Skill    Skill
	Version  int
	// OwnerID is who taught it; Proven whether a run of it has verified (an unproven one only
	// its owner starts, themselves).
	OwnerID int64
	Proven  bool
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
	// Goal is the point of the step, and Expect what can be observed once it worked (the
	// workflow's skill, skill.go).
	Goal   string        `json:"goal,omitempty"`
	Expect *SuccessCheck `json:"expect,omitempty"`
}

// StepWindow is the window a desktop step was recorded in: its class (the app id on Wayland)
// and title, and where in it a click was.
type StepWindow struct {
	Class string       `json:"class,omitempty"`
	Title string       `json:"title,omitempty"`
	Point *WindowPoint `json:"point,omitempty"`
}

// WindowPoint is a click's point from its window's top-left, with the window's size W×H then.
type WindowPoint struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// RecordedElement is what the recorder saw of the element a step acted on.
type RecordedElement struct {
	Role     string `json:"role,omitempty"`
	Name     string `json:"name,omitempty"`
	Selector string `json:"selector,omitempty"`
	Frame    string `json:"frame,omitempty"`
	AXPath   string `json:"ax_path,omitempty"`
	// Source is where the element came from: "ax" for the desktop accessibility tree (AX on
	// macOS, AT-SPI on Linux), "vision" for one the vision model named from the click's
	// keyframe (no AX path: a replay finds it by sight); empty for a browser element.
	Source string `json:"source,omitempty"`
	// Confidence is the vision model's, 0 to 1 (source "vision" only).
	Confidence float64 `json:"confidence,omitempty"`
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
	// Proved records that a run of workflow id at version finished with its result verified.
	Proved(ctx context.Context, id string, version int) error
	// Record stores workflow id's run as it stands (record.go), over the one before.
	Record(ctx context.Context, id string, rec RunRecord) error
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
// typed and selected text, the addresses (escaped for where it stands in the URL) and the
// recorded elements' names and selectors (the "Statement {month}" link). A month is written as
// the workflow's example writes it (ParamValue). A param the workflow declares without a value
// is an error.
func (w *Workflow) Bind(params map[string]string) ([]WorkflowStep, error) {
	return w.bind(params, time.Now())
}

func (w *Workflow) bind(params map[string]string, now time.Time) ([]WorkflowStep, error) {
	if miss := w.Missing(params); len(miss) > 0 {
		return nil, fmt.Errorf("I need the %s to run %q", strings.Join(miss, " and "), w.Name)
	}
	declared := map[string]string{}
	for _, p := range w.Params {
		declared[p.Name] = ParamValue(p, params[p.Name], now)
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
			el.Name, el.Selector = sub(el.Name, nil), sub(el.Selector, nil)
			st.Element = &el
		}
		st.Action = a
		st.Goal = sub(st.Goal, nil)
		if c := st.Expect; c != nil {
			// What the step should cause, for this run's values: recorded for September, the
			// page it opens in August is August's.
			b := c.with(func(s string) string { return sub(s, nil) })
			st.Expect = &b
		}
		steps[i] = st
	}
	return steps, nil
}

// BindText is s with each declared {param} replaced by its value for this run (a goal, a check).
func (w *Workflow) BindText(s string, params map[string]string) string {
	now := time.Now()
	return placeholderRe.ReplaceAllStringFunc(s, func(ph string) string {
		name := ph[1 : len(ph)-1]
		for _, p := range w.Params {
			if p.Name == name {
				return ParamValue(p, params[name], now)
			}
		}
		return ph
	})
}

// with is c with f applied to every text a param can stand in.
// shortcut: a value goes into an address as given; escape it when a param holds a space or a slash.
func (c SuccessCheck) with(f func(string) string) SuccessCheck {
	b := c
	b.URLContains, b.Screen = f(c.URLContains), f(c.Screen)
	b.TextPresent = slices.Clone(c.TextPresent)
	for j := range b.TextPresent {
		b.TextPresent[j] = f(b.TextPresent[j])
	}
	if c.Download != nil {
		d := *c.Download
		d.NameLike = f(d.NameLike)
		b.Download = &d
	}
	if c.File != nil {
		x := *c.File
		x.Path = f(x.Path)
		b.File = &x
	}
	return b
}

var (
	isoMonthRe = regexp.MustCompile(`^\d{4}-\d{2}$`)
	// monthNameRe and monthNumRe are a month as people say it: "August", "aug 2026", "August of 2026",
	// "2026-8", "08/2026".
	monthNameRe = regexp.MustCompile(`^([a-z]{3,9})\.?(?:,?\s+(?:of\s+)?(\d{4}))?$`)
	monthNumRe  = regexp.MustCompile(`^(?:(\d{4})-(\d{1,2})|(\d{1,2})/(\d{4}))$`)
)

// ParamValue is v, the value given for p, as the workflow uses it: trimmed, and a month as its
// example writes it (YYYY-MM) when the example is one: "August" or "August 2026" is 2026-08. A
// month without a year is its latest one up to now's month. Anything else is left as given.
func ParamValue(p WorkflowParam, v string, now time.Time) string {
	v = strings.TrimSpace(v)
	if p.Kind != "month" || !isoMonthRe.MatchString(p.Example) {
		return v
	}
	low := strings.ToLower(v)
	year, month := 0, 0
	if m := monthNumRe.FindStringSubmatch(low); m != nil {
		if m[1] != "" {
			year, month = atoi(m[1]), atoi(m[2])
		} else {
			year, month = atoi(m[4]), atoi(m[3])
		}
	} else if m := monthNameRe.FindStringSubmatch(low); m != nil {
		for i := time.January; i <= time.December; i++ {
			if name := strings.ToLower(i.String()); name == m[1] || (len(m[1]) >= 3 && strings.HasPrefix(name, m[1])) {
				month = int(i)
			}
		}
		if m[2] != "" {
			year = atoi(m[2])
		} else if month != 0 {
			year = now.Year()
			if month > int(now.Month()) {
				year--
			}
		}
	}
	if month < 1 || month > 12 || year < 1 {
		return v
	}
	return fmt.Sprintf("%04d-%02d", year, month)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
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
