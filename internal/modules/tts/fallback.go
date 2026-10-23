package tts

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// Remote TTS fallback (tts.fallback=cpu, docs/llm/07-remote-speech.md §3, decision
// 2026-10-22-remote-speech-fallback.md): when the provider fails in a way that would end the
// reply (transport, timeout, 401/403, 429, 5xx), the failed sentence and the rest of the reply
// are rendered by local Kokoro (CPU) into the same stream. Kokoro renders 24 kHz s16le, the
// remote stream's format, so nothing changes on the wire. For a short backoff after a failure,
// replies go straight to Kokoro. With no Kokoro model the reply ends with the remote error, as
// with fallback=off, and says why there was no fallback.

func (m *Module) fallbackMode(ctx context.Context) string {
	if m.settings == nil {
		return fallback.ModeOff
	}
	if m.settings.String(ctx, "tts.fallback", settings.Scope{}) == fallback.ModeCPU {
		return fallback.ModeCPU
	}
	return fallback.ModeOff
}

// fallBack renders sentences[i:] on Kokoro after the provider failed on sentence i with err.
func (m *Module) fallBack(ctx context.Context, st *Stream, err error, i int, sentences []string, opts SpeakOptions, emit func([]byte) error) {
	m.fallback.Failed(err)
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusUnauthorized || se.code == http.StatusForbidden) {
		// An expired key shouldn't silence the house, but someone has to fix it.
		m.log().Error("tts: remote provider refused the API key; the rest of the reply is local Kokoro", "index", i, "err", err)
	} else {
		m.log().Warn("tts: remote provider failed; the rest of the reply is local Kokoro", "index", i, "err", err)
	}
	lerr := m.speakLocal(ctx, sentences[i:], opts, emit)
	if lerr == nil || ctx.Err() != nil {
		m.fallback.Served(fallback.PathLocalCPU)
		return
	}
	m.log().Error("tts: local fallback unavailable", "err", lerr)
	st.err = fmt.Errorf("%w (local fallback unavailable: %v)", err, lerr)
}

// speakLocal renders sentences on local Kokoro into emit, for the remote's fallback: the
// Kokoro voice, speed and gain settings (a caller's voice only if it is a Kokoro voice). The
// caller holds the slot in m.sem. It fails if no sentence rendered.
func (m *Module) speakLocal(ctx context.Context, sentences []string, opts SpeakOptions, emit func([]byte) error) error {
	o := SpeakOptions{Speed: opts.Speed, Gain: opts.Gain}
	if _, ok := voiceByName(opts.Voice); ok {
		o.Voice = opts.Voice
	}
	cfg := m.resolve(ctx, o)
	dir, installed := m.modelDir(ctx)
	if !installed {
		return ErrNotInstalled
	}
	ref, err := m.eng.acquire(keyFor(dir, cfg.voice), true)
	if err != nil {
		return err
	}
	defer m.eng.release(ref)
	if r := ref.s.SampleRate(); r != remoteSampleRate {
		return fmt.Errorf("tts: the Kokoro model renders %d Hz, not the remote stream's %d", r, remoteSampleRate)
	}
	sid := cfg.voice.SID
	if sid >= ref.s.NumSpeakers() {
		sid = 0
	}
	var last error
	rendered := false
	for i, sentence := range sentences {
		if err := ctx.Err(); err != nil {
			return err
		}
		samples, err := ref.s.Generate(sentence, sid, cfg.speed)
		if err != nil {
			m.log().Warn("tts: sentence failed", "index", i, "chars", len(sentence), "err", err, "engine", fallback.PathLocalCPU)
			last = err
			continue
		}
		if err := emit(pcm16(samples, cfg.gain)); err != nil {
			return err
		}
		rendered = true
	}
	if !rendered && last != nil {
		return last
	}
	return nil
}

// FallbackStatus reports the remote voice's fallback for the labels API: the setting, the
// path that served lately, the fallback count, the last remote error and a warning when
// fallback=cpu has no Kokoro to go to. false when tts speaks through Kokoro anyway.
func (m *Module) FallbackStatus() (fallback.Status, bool) {
	ctx := context.Background()
	if m.provider(ctx) != RemoteProvider {
		return fallback.Status{}, false
	}
	st := m.fallback.Status()
	st.Mode = m.fallbackMode(ctx)
	if st.Mode == fallback.ModeCPU {
		if _, ok := m.modelDir(ctx); !ok {
			st.Warning = "no Kokoro model for the local fallback; install one on the Models page"
		}
	}
	return st, true
}
