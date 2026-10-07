package prompts

// PersonaPreset is one editable starter voice offered by the mobile Household Settings
// screen (served unchanged by the persona presets route).
type PersonaPreset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Text  string `json:"text"`
}
