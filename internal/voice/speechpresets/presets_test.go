package speechpresets

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/modules/tts"
)

func TestSTTPresets(t *testing.T) {
	var ids []string
	for _, p := range STT {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"openai", "groq", "elevenlabs", "other"}) {
		t.Fatalf("presets %v", ids)
	}
	if p, _ := Find(STT, "openai"); p.URL != "https://api.openai.com/v1" || p.STTModel != "gpt-transcribe" {
		t.Errorf("openai %+v", p)
	}
	if p, _ := Find(STT, "groq"); p.URL != "https://api.groq.com/openai/v1" || p.STTModel != "whisper-large-v3-turbo" {
		t.Errorf("groq %+v", p)
	}
	if p, ok := Find(STT, "other"); !ok || p.URL != "" || p.STTModel != "" {
		t.Errorf("other %+v", p)
	}
	// ElevenLabs' paths carry their own version (/v1/speech-to-text, /v2/voices): its URL is the root.
	if p, _ := Find(STT, "elevenlabs"); p.URL != "https://api.elevenlabs.io" || p.API != APIElevenLabs || p.STTModel != "scribe_v2" {
		t.Errorf("elevenlabs %+v", p)
	}

	// Every preset must be storable as is: its dialect is one stt.remote_api accepts, and its
	// URL is already in the resolver's normal form.
	var opts []any
	for _, d := range engine.SettingDefinitions() {
		if d.Key == "stt.remote_api" {
			opts = d.Options
		}
	}
	for _, p := range STT {
		if !slices.Contains(opts, any(p.API)) {
			t.Errorf("%s: api %q not in stt.remote_api %v", p.ID, p.API, opts)
		}
		if p.URL == "" {
			continue
		}
		if u, err := url.Parse(p.URL); err != nil || u.Scheme != "https" || engine.NormalizeBaseURL(p.URL) != p.URL {
			t.Errorf("%s: url %q", p.ID, p.URL)
		}
	}
}

// TTS presets: OpenAI, ElevenLabs and Other (Groq's TTS models are in flux; design §1).
func TestTTSPresets(t *testing.T) {
	var ids []string
	for _, p := range TTS {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"openai", "elevenlabs", "other"}) {
		t.Fatalf("presets %v", ids)
	}
	if p, _ := Find(TTS, "openai"); p.URL != "https://api.openai.com/v1" || p.TTSModel != "gpt-4o-mini-tts" || p.TTSVoice != "marin" || p.STTModel != "" {
		t.Errorf("openai %+v", p)
	}
	// ElevenLabs' voice is picked from its list (/v2/voices): no default.
	if p, _ := Find(TTS, "elevenlabs"); p.URL != "https://api.elevenlabs.io" || p.API != APIElevenLabs || p.TTSModel != "eleven_flash_v2_5" || p.TTSVoice != "" {
		t.Errorf("elevenlabs %+v", p)
	}
	if p, ok := Find(TTS, "other"); !ok || p.URL != "" || p.TTSModel != "" || p.API != APIOpenAI {
		t.Errorf("other %+v", p)
	}
	var opts []any
	for _, d := range tts.Definitions {
		if d.Key == "tts.remote_api" {
			opts = d.Options
		}
	}
	for _, p := range TTS {
		if !slices.Contains(opts, any(p.API)) {
			t.Errorf("%s: api %q not in tts.remote_api %v", p.ID, p.API, opts)
		}
		if p.URL != "" && strings.TrimRight(p.URL, "/") != p.URL {
			t.Errorf("%s: url %q", p.ID, p.URL)
		}
	}
}
