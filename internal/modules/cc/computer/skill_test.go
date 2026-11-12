package computer

import (
	"strings"
	"testing"
)

func TestSkillValidate(t *testing.T) {
	params := []WorkflowParam{{Name: "file", Kind: "file"}}
	ok := Skill{
		Goal:       Goal{Reason: " keep the budget current ", EndState: "the summary sheet shows this month"},
		WhenToUse:  "the user asks for the monthly budget summary",
		Guardrails: GuardCreative,
		Prerequisites: []Prerequisite{
			{Kind: PrereqAppOpen, App: "Calc"},
			{Kind: PrereqFileOpen, File: "{file}", Text: "{file} is open"},
			{Kind: PrereqPageOpen, URL: "https://bank.example/statements"},
			{Kind: PrereqSignedIn, Site: "bank.example"},
			{Kind: PrereqOther, Text: "the printer is on"},
		},
		Notes: []Note{{Text: "the totals row is the last one", App: "Calc"}},
	}
	if err := ok.Validate(params); err != nil {
		t.Fatal(err)
	}
	if ok.Goal.Reason != "keep the budget current" {
		t.Errorf("reason not trimmed: %q", ok.Goal.Reason)
	}
	if err := (&Skill{}).Validate(nil); err != nil {
		t.Errorf("an empty skill (a workflow from before skills): %v", err)
	}
	for _, c := range []struct {
		name string
		s    Skill
		want string
	}{
		{"guardrails", Skill{Guardrails: "yolo"}, "skill.guardrails must be strict or creative"},
		{"kind", Skill{Prerequisites: []Prerequisite{{Kind: "moon"}}}, "skill.prerequisites[0]: kind must be one of"},
		{"subject", Skill{Prerequisites: []Prerequisite{{Kind: PrereqFileOpen, Text: "a file"}}}, "skill.prerequisites[0]: file_open needs what it is about"},
		{"param", Skill{Prerequisites: []Prerequisite{{Kind: PrereqFileOpen, File: "{month}.xlsx"}}}, "skill.prerequisites[0]: {month} isn't one of the workflow's params"},
		{"long", Skill{Goal: Goal{EndState: strings.Repeat("x", maxSkillText+1)}}, "skill.goal.end_state is longer than 500 characters"},
		{"note", Skill{Notes: []Note{{Text: " "}}}, "skill.notes[0]: empty"},
		{"many", Skill{Prerequisites: make([]Prerequisite, maxPrerequisites+1)}, "skill.prerequisites: at most 20"},
	} {
		if err := c.s.Validate(params); err == nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	st := WorkflowStep{Goal: strings.Repeat("x", maxStepGoal+1)}
	if err := st.ValidateSkill(); err == nil {
		t.Error("a step goal past the limit was accepted")
	}
}
