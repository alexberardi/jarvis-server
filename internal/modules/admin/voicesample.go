package admin

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// The wizard's Voice step (AD3b): the active provider's voices to choose from (Kokoro's, or a
// remote provider's) and a short sample of one, rendered in process by the tts module.
// Superuser only, like every BFF route; the sample is a few seconds of audio for the
// operator's own ears, never stored.

// Speech renders a voice sample as WAV (the tts module); ttsmod.ErrNotInstalled while no voice
// model is installed.
type Speech interface {
	Sample(ctx context.Context, voice, text string) ([]byte, error)
}

// VoiceLister lists the active TTS provider's voices (the tts module): Kokoro's, or a remote
// provider's. A Speech without it offers Kokoro's.
type VoiceLister interface {
	VoiceChoices(ctx context.Context) ttsmod.VoiceChoices
}

// SampleText is what the Voice step plays when no text is given.
const SampleText = "Hi, I'm Jarvis. What can I do for you?"

// maxSampleChars caps a sample: it is a preview, not a synthesis endpoint.
const maxSampleChars = 200

// sampleTimeout bounds one sample, including loading the Kokoro model the first time.
const sampleTimeout = 60 * time.Second

// voiceSetting is the tts module's voice setting the Voice step writes.
const voiceSetting = "tts.kokoro_voice"

// handleVoices is GET /api/tts/voices: the active provider's voice names, the one in use, the
// default, the setting a choice goes to, and free_text when the provider has no list.
func (m *Module) handleVoices(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.voiceChoices(r.Context()))
}

func (m *Module) voiceChoices(ctx context.Context) ttsmod.VoiceChoices {
	if l, ok := m.TTS.(VoiceLister); ok {
		return l.VoiceChoices(ctx)
	}
	current := ttsmod.DefaultVoice
	if src := m.settingsSource("tts"); src != nil {
		if v := src.Settings().String(ctx, voiceSetting, settings.Scope{}); v != "" {
			current = v
		}
	}
	return ttsmod.VoiceChoices{Provider: ttsmod.ProviderName, Voices: ttsmod.Voices(), Current: current,
		Default: ttsmod.DefaultVoice, Setting: "tts/" + voiceSetting}
}

// FallbackReporter reports the tts module's remote fallback state; ok=false while Kokoro
// speaks everything. The Speech section's TTS test reads which engine answered from it.
type FallbackReporter interface {
	FallbackStatus() (fallback.Status, bool)
}

// sampleEngine is which engine rendered the sample just played, for the X-Speech-Engine
// header: local (Kokoro), remote, or local-cpu (the remote failed and the fallback answered).
// Read from the fallback gate right after the render; the Sample call is synchronous, so the
// gate's last served path is the sample's own (a concurrent voice turn can race it, which for
// a test button is accepted).
func (m *Module) sampleEngine() string {
	if f, ok := m.TTS.(FallbackReporter); ok {
		if st, ok := f.FallbackStatus(); ok {
			if st.LastPath != "" {
				return st.LastPath
			}
			return fallback.PathRemote
		}
	}
	return "local"
}

// sampleVoice is the provider's spelling of the voice asked for; ok=false when it has no such
// voice. Free text takes any name.
func sampleVoice(c ttsmod.VoiceChoices, asked string) (string, bool) {
	asked = strings.TrimSpace(asked)
	if asked == "" {
		return "", false
	}
	if i := slices.IndexFunc(c.Voices, func(v string) bool { return strings.EqualFold(v, asked) }); i >= 0 {
		return c.Voices[i], true
	}
	return asked, c.FreeText && len(asked) <= maxVoiceChars
}

// maxVoiceChars caps a free-text voice name.
const maxVoiceChars = 100

// handleVoiceSample is POST /api/tts/sample with {"voice": name, "text"?: string}: a WAV of
// the voice saying text (SampleText when empty). 409 while no voice model is installed.
func (m *Module) handleVoiceSample(w http.ResponseWriter, r *http.Request) {
	if m.TTS == nil {
		unavailable(w, "tts")
		return
	}
	var body struct {
		Voice string `json:"voice"`
		Text  string `json:"text"`
	}
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	voice, known := sampleVoice(m.voiceChoices(r.Context()), body.Voice)
	if !known {
		httpx.ValidationError(w, httpx.FieldError{Type: "value_error", Loc: []any{"body", "voice"},
			Msg: "Unknown voice", Input: body.Voice})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		text = SampleText
	}
	if len([]rune(text)) > maxSampleChars {
		httpx.ValidationError(w, httpx.FieldError{Type: "string_too_long", Loc: []any{"body", "text"},
			Msg: "Text should have at most " + strconv.Itoa(maxSampleChars) + " characters", Input: body.Text})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sampleTimeout)
	defer cancel()
	wav, err := m.TTS.Sample(ctx, voice, text)
	if err == nil {
		w.Header().Set("X-Speech-Engine", m.sampleEngine())
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Content-Length", strconv.Itoa(len(wav)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wav)
		return
	}
	switch {
	case errors.Is(err, ttsmod.ErrNotInstalled):
		httpx.Error(w, http.StatusConflict, "The voice model is not installed yet")
	case r.Context().Err() != nil:
		// the operator left; nobody reads the answer
	default:
		m.deps.Log.Error("admin: voice sample failed", "voice", voice, "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Could not render the sample: "+err.Error())
	}
}
