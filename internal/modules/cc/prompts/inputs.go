package prompts

import "strings"

// ToolCalling is how a prompt's tools reach the model: as native API tools (the prompt carries
// no signatures) or as text in the system prompt.
type ToolCalling string

const (
	// ToolsNative sends the tools in the request's tools payload; {{tools}} renders "".
	ToolsNative ToolCalling = "native"
	// ToolsText writes the <tools> signatures and the call format into the prompt.
	ToolsText ToolCalling = "text"
)

// Inputs is everything a text prompt's variables are filled from.
type Inputs struct {
	Context     Context
	Tools       []Tool
	Flags       []CommandFlag
	ToolCalling ToolCalling
}

// notForMeInstruction is how the model stays silent: the not_for_me tool with native tool
// calling, the <not_for_me/> sentinel with text tool calling (decision 2026-10-20-prompt-diet).
func notForMeInstruction(tc ToolCalling) string {
	if tc == ToolsText {
		return "reply with <not_for_me/> alone."
	}
	return "call not_for_me and nothing else."
}

// para reduces a legacy "\n"-framed section to its paragraph.
func para(s string) string { return strings.TrimSuffix(strings.TrimPrefix(s, "\n"), "\n") }

// Values is the variable table Render fills a prompt from, built from the same section
// functions the providers use.
func (in Inputs) Values() map[string]string {
	c := in.Context
	tools := func(desc bool) string {
		if in.ToolCalling != ToolsText {
			return ""
		}
		return "You are provided with function signatures within <tools></tools> XML tags:\n" +
			toolsBlock(in.Tools, buildOptions{paramDescriptions: desc, excludeRefinable: true}) + "\n\n" + qwenToolFormat
	}
	return map[string]string{
		"room":             c.Room,
		"voice_mode":       c.VoiceMode,
		"persona":          PersonalityBlock(c.HouseholdPersona),
		"persona_reminder": PersonalityReminder(c.HouseholdPersona),
		"tools":            tools(true),
		"tools_compact":    tools(false),
		"date_keys":        para(dtKeysLine(c.DateKeys)),
		"tool_guidance":    para(toolGuidanceSection(in.Tools)),
		"direct_answers":   para(directAnswerSection(in.Flags, in.Tools)),
		"not_for_me":       notForMeInstruction(in.ToolCalling),
		"agents":           para(agentContextSummary(c)),
		"rooms":            para(roomHierarchySection(c.RoomHierarchy)),
	}
}
