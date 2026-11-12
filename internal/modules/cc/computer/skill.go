package computer

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// A workflow's skill (docs/agent/WORKFLOW-SKILLS.md): what it knows about its task beyond its
// steps, as separate fields (never a document). It is learnt from the recording and edited by
// the owner. Runs don't read it yet (module verified-replay).

// Guardrails: how far a run may stray from the recording.
const (
	GuardStrict   = "strict"   // only the apps and sites recorded in, any route inside them
	GuardCreative = "creative" // any means
)

// Prerequisite kinds.
const (
	PrereqAppOpen  = "app_open"  // App is running with a window
	PrereqFileOpen = "file_open" // File is open (in App, when set)
	PrereqPageOpen = "page_open" // URL is showing in the browser
	PrereqSignedIn = "signed_in" // the user is signed in to Site
	PrereqOther    = "other"     // Text says it; only the model can check it
)

var prereqKinds = []string{PrereqAppOpen, PrereqFileOpen, PrereqPageOpen, PrereqSignedIn, PrereqOther}

// Limits on a skill: it is rendered into a local model's prompt.
const (
	MaxSkillText     = 500
	maxPrerequisites = 20
	maxNotes         = 50
	MaxStepGoal      = 300
)

// Skill is the workflow-level part; each step's goal and expectation are on the step.
type Skill struct {
	Goal          Goal           `json:"goal,omitzero"`
	WhenToUse     string         `json:"when_to_use,omitempty"`
	Prerequisites []Prerequisite `json:"prerequisites,omitempty"`
	Guardrails    string         `json:"guardrails,omitempty"` // "" is GuardStrict
	Notes         []Note         `json:"notes,omitempty"`
}

// Goal is why the task is done and what is true once it worked: a finished run is checked
// against EndState.
type Goal struct {
	Reason   string `json:"reason,omitempty"`
	EndState string `json:"end_state,omitempty"`
}

// Prerequisite is something that must be true before step 1; a run tries to achieve an unmet
// one. Text, File and URL may hold {param}.
type Prerequisite struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"` // as the owner reads it
	App  string `json:"app,omitempty"`
	Site string `json:"site,omitempty"`
	File string `json:"file,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Note is something to watch for, about this workflow or (App set) about an app.
type Note struct {
	Text string `json:"text"`
	App  string `json:"app,omitempty"`
}

// Validate checks the skill of a workflow with these params, trimming its texts. Errors name
// the field ("skill.prerequisites[1]: ...").
func (s *Skill) Validate(params []WorkflowParam) error {
	trim := func(field string, p *string) error {
		if *p = strings.TrimSpace(*p); utf8.RuneCountInString(*p) > MaxSkillText {
			return fmt.Errorf("skill.%s is longer than %d characters", field, MaxSkillText)
		}
		return nil
	}
	if err := trim("goal.reason", &s.Goal.Reason); err != nil {
		return err
	}
	if err := trim("goal.end_state", &s.Goal.EndState); err != nil {
		return err
	}
	if err := trim("when_to_use", &s.WhenToUse); err != nil {
		return err
	}
	switch {
	case s.Guardrails != "" && s.Guardrails != GuardStrict && s.Guardrails != GuardCreative:
		return fmt.Errorf("skill.guardrails must be %s or %s", GuardStrict, GuardCreative)
	case len(s.Prerequisites) > maxPrerequisites:
		return fmt.Errorf("skill.prerequisites: at most %d", maxPrerequisites)
	case len(s.Notes) > maxNotes:
		return fmt.Errorf("skill.notes: at most %d", maxNotes)
	}
	for i := range s.Prerequisites {
		p := &s.Prerequisites[i]
		at := fmt.Sprintf("prerequisites[%d]", i)
		need := map[string]*string{PrereqAppOpen: &p.App, PrereqFileOpen: &p.File, PrereqPageOpen: &p.URL,
			PrereqSignedIn: &p.Site, PrereqOther: &p.Text}[p.Kind]
		if need == nil {
			return fmt.Errorf("skill.%s: kind must be one of %s", at, strings.Join(prereqKinds, ", "))
		}
		for _, f := range []*string{&p.Text, &p.App, &p.Site, &p.File, &p.URL} {
			if err := trim(at, f); err != nil {
				return err
			}
		}
		if *need == "" {
			return fmt.Errorf("skill.%s: %s needs what it is about", at, p.Kind)
		}
		for _, f := range []string{p.Text, p.File, p.URL} {
			if name, ok := undeclared(f, params); !ok {
				return fmt.Errorf("skill.%s: {%s} isn't one of the workflow's params", at, name)
			}
		}
	}
	for i := range s.Notes {
		if err := trim(fmt.Sprintf("notes[%d]", i), &s.Notes[i].Text); err != nil {
			return err
		}
		if s.Notes[i].Text == "" {
			return fmt.Errorf("skill.notes[%d]: empty", i)
		}
		s.Notes[i].App = strings.TrimSpace(s.Notes[i].App)
	}
	return nil
}

// ValidateSkill checks a step's goal (trimmed).
func (s *WorkflowStep) ValidateSkill() error {
	if s.Goal = strings.TrimSpace(s.Goal); utf8.RuneCountInString(s.Goal) > MaxStepGoal {
		return fmt.Errorf("goal is longer than %d characters", MaxStepGoal)
	}
	return nil
}

// undeclared is the first {name} in s that isn't a param (ok when there is none).
func undeclared(s string, params []WorkflowParam) (string, bool) {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if !slices.ContainsFunc(params, func(p WorkflowParam) bool { return p.Name == m[1] }) {
			return m[1], false
		}
	}
	return "", true
}
