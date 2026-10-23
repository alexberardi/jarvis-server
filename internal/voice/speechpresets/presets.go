// Package speechpresets is the static catalog of remote speech providers
// (docs/llm/07-remote-speech.md §1). A preset fills a label's remote fields (URL, dialect,
// model), which stay editable; only those fields are stored, never the preset.
package speechpresets

// Dialects a remote speech endpoint speaks (stt.remote_api, tts.remote_api).
const (
	APIOpenAI     = "openai"
	APIElevenLabs = "elevenlabs"
)

// Preset is one provider's defaults for one kind (STT or TTS). An empty model means the preset
// offers no default (Other: the user types it).
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// URL is the base the dialect's paths are appended to ("" for Other).
	URL string `json:"url"`
	API string `json:"api"`
	// STTModel is the model sent for speech-to-text.
	STTModel string `json:"stt_model,omitempty"`
	// TTSModel and TTSVoice are text-to-speech's model and voice ("" = picked from the
	// provider's list).
	TTSModel string `json:"tts_model,omitempty"`
	TTSVoice string `json:"tts_voice,omitempty"`
	// Note is something to know before picking it (cost, data handling).
	Note string `json:"note,omitempty"`
}

// STT lists the speech-to-text presets in display order.
var STT = []Preset{
	{ID: "openai", Name: "OpenAI", URL: "https://api.openai.com/v1", API: APIOpenAI, STTModel: "gpt-transcribe"},
	{ID: "groq", Name: "Groq", URL: "https://api.groq.com/openai/v1", API: APIOpenAI, STTModel: "whisper-large-v3-turbo",
		Note: "Groq bills each request as at least 10 seconds, so short voice turns cost more than they look."},
	{ID: "elevenlabs", Name: "ElevenLabs", URL: elevenLabsURL, API: APIElevenLabs, STTModel: "scribe_v2", Note: elevenLabsNote},
	{ID: "other", Name: "Other", API: APIOpenAI,
		Note: "Any server with an OpenAI-compatible /audio/transcriptions: speaches, LocalAI, a GPU box on your network."},
}

// TTS lists the text-to-speech presets in display order. Groq has none yet (its TTS models are
// in flux, docs/llm/07-remote-speech.md §1).
var TTS = []Preset{
	{ID: "openai", Name: "OpenAI", URL: "https://api.openai.com/v1", API: APIOpenAI, TTSModel: "gpt-4o-mini-tts", TTSVoice: "marin"},
	{ID: "elevenlabs", Name: "ElevenLabs", URL: elevenLabsURL, API: APIElevenLabs, TTSModel: "eleven_flash_v2_5", Note: elevenLabsNote},
	{ID: "other", Name: "Other", API: APIOpenAI,
		Note: "Any server with an OpenAI-compatible /audio/speech: speaches, Kokoro-FastAPI, LocalAI, a GPU box on your network."},
}

// elevenLabsURL is ElevenLabs' API root: its paths carry their own version (/v1/speech-to-text,
// /v2/voices), so unlike OpenAI's the URL stops before it
// (docs/decisions/2026-10-22-remote-speech-elevenlabs.md).
const elevenLabsURL = "https://api.elevenlabs.io"

const elevenLabsNote = "Requests ask ElevenLabs not to keep the audio or text (enable_logging=false), which some plans don't allow."

// Find returns the preset with this id.
func Find(presets []Preset, id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
