// Package speechpresets is the static catalog of remote speech providers
// (docs/llm/07-remote-speech.md §1). A preset fills a label's remote fields (URL, dialect,
// model), which stay editable; only those fields are stored, never the preset.
package speechpresets

// Dialects a remote speech endpoint speaks (stt.remote_api).
const APIOpenAI = "openai"

// Preset is one provider's defaults. An empty STTModel means the preset offers no
// speech-to-text default (Other: the user types it).
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the base the dialect's paths are appended to ("" for Other).
	URL string `json:"url"`
	API string `json:"api"`
	// STTModel is the model sent for speech-to-text.
	STTModel string `json:"stt_model"`
	// Note is something to know before picking it (cost, data handling).
	Note string `json:"note,omitempty"`
}

// STT lists the speech-to-text presets in display order.
var STT = []Preset{
	{ID: "openai", Name: "OpenAI", URL: "https://api.openai.com/v1", API: APIOpenAI, STTModel: "gpt-transcribe"},
	{ID: "groq", Name: "Groq", URL: "https://api.groq.com/openai/v1", API: APIOpenAI, STTModel: "whisper-large-v3-turbo",
		Note: "Groq bills each request as at least 10 seconds, so short voice turns cost more than they look."},
	{ID: "other", Name: "Other", API: APIOpenAI,
		Note: "Any server with an OpenAI-compatible /audio/transcriptions: speaches, LocalAI, a GPU box on your network."},
}

// Find returns the preset with this id.
func Find(presets []Preset, id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
