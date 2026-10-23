package admin

import (
	"context"
	_ "embed"
	"errors"
	"net/http"
	"time"

	sttmod "github.com/alexberardi/jarvis-server/internal/modules/stt"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/voice/speechpresets"
)

// The Models page's Speech section (docs/llm/07-remote-speech.md §4, decision
// 2026-10-23-admin-speech-section.md): the static provider presets, and a test that transcribes
// a bundled clip through whatever the stt label is set to, so the operator hears back what the
// house would hear. Superuser, like every BFF route.

// Transcriber transcribes a clip in process (the stt module): the Speech section's STT test.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte, opts sttmod.TranscribeOptions) (sttmod.Result, error)
}

// speechTestClip is the STT test's bundled clip: a TTS-rendered "tell me a joke" (16 kHz mono,
// ~4 s), the same one the wake-word tests use. No operator audio is involved.
//
//go:embed speech_test_clip.wav
var speechTestClip []byte

// speechTestClipSays is what the clip says, so the UI can show the expected text next to what
// the engine heard.
const speechTestClipSays = "tell me a joke"

// sttTestTimeout bounds one test transcription, including a local whisper-server's cold start.
const sttTestTimeout = 60 * time.Second

// handleSpeechPresets is GET /api/speech/presets: the static remote speech provider catalog.
// Picking a preset in the admin fills a label's remote fields, which stay editable; only the
// fields are stored, never the preset.
func (m *Module) handleSpeechPresets(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string][]speechpresets.Preset{
		"stt": speechpresets.STT,
		"tts": speechpresets.TTS,
	})
}

// handleSTTTest is POST /api/speech/stt-test: transcribe the bundled clip through the active
// stt engine and report the text, the latency and which engine answered (remote, local, or
// local-cpu when the fallback served).
func (m *Module) handleSTTTest(w http.ResponseWriter, r *http.Request) {
	if m.STT == nil {
		unavailable(w, "stt")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sttTestTimeout)
	defer cancel()
	start := time.Now()
	res, err := m.STT.Transcribe(ctx, speechTestClip, sttmod.TranscribeOptions{})
	latency := time.Since(start)
	if err != nil {
		switch {
		case errors.Is(err, sttmod.ErrEngineUnavailable):
			httpx.Error(w, http.StatusConflict, "The speech-to-text engine is not ready yet: "+err.Error())
		case r.Context().Err() != nil:
			// the operator left; nobody reads the answer
		default:
			m.deps.Log.Error("admin: stt test failed", "err", err)
			httpx.Error(w, http.StatusBadGateway, "Transcription failed: "+err.Error())
		}
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"text":       res.Text,
		"engine":     res.Engine,
		"latency_ms": latency.Milliseconds(),
		"clip_says":  speechTestClipSays,
	})
}
