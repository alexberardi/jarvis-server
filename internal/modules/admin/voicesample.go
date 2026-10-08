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
)

// The wizard's Voice step (AD3b): the Kokoro voices to choose from and a short sample of one,
// rendered in process by the tts module. Superuser only, like every BFF route; the sample is
// a few seconds of audio for the operator's own ears, never stored.

// Speech renders a voice sample as WAV (the tts module); ttsmod.ErrNotInstalled while no voice
// model is installed.
type Speech interface {
	Sample(ctx context.Context, voice, text string) ([]byte, error)
}

// SampleText is what the Voice step plays when no text is given.
const SampleText = "Hi, I'm Jarvis. What can I do for you?"

// maxSampleChars caps a sample: it is a preview, not a synthesis endpoint.
const maxSampleChars = 200

// sampleTimeout bounds one sample, including loading the Kokoro model the first time.
const sampleTimeout = 60 * time.Second

// voiceSetting is the tts module's voice setting the Voice step writes.
const voiceSetting = "tts.kokoro_voice"

// handleVoices is GET /api/tts/voices: the voice names, the one in use and the default.
func (m *Module) handleVoices(w http.ResponseWriter, r *http.Request) {
	current := ttsmod.DefaultVoice
	if src := m.settingsSource("tts"); src != nil {
		if v := src.Settings().String(r.Context(), voiceSetting, settings.Scope{}); v != "" {
			current = v
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"voices": ttsmod.Voices(), "current": current, "default": ttsmod.DefaultVoice, "setting": "tts/" + voiceSetting,
	})
}

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
	voice := strings.ToLower(strings.TrimSpace(body.Voice))
	if !slices.Contains(ttsmod.Voices(), voice) {
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
