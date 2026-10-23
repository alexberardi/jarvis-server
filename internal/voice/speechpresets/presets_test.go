package speechpresets

import (
	"net/url"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

func TestSTTPresets(t *testing.T) {
	var ids []string
	for _, p := range STT {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"openai", "groq", "other"}) {
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
	if _, ok := Find(STT, "elevenlabs"); ok {
		t.Error("elevenlabs needs its own dialect first")
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
