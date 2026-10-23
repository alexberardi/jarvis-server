package stt

import (
	"context"
	"errors"
	"net/http"

	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// inferRemote transcribes the clip on the remote provider. With fallback=cpu
// (docs/llm/07-remote-speech.md §2), a failure that falls back (fallback.FallsBackOn: transport,
// timeout, 401/403, 429, 5xx) sends the same clip to a local whisper-server on CPU, and for a
// short backoff after one the clip goes there first, without asking the provider. With no
// local model the error is the remote one, saying why there was no fallback.
func (m *Module) inferRemote(ctx context.Context, ep Endpoint, samples []float32, p inferenceParams) (string, []Segment, string, error) {
	mode := ep.Fallback
	if mode == "" {
		mode = fallback.ModeOff
	}
	m.fallbackMode.Store(mode)
	if mode != fallback.ModeCPU || m.FallbackEngine == nil {
		text, segs, err := m.inferProvider(ctx, ep, samples, p)
		if err != nil {
			m.fallback.Errored(err)
		} else {
			m.fallback.Served(fallback.PathRemote)
		}
		return text, segs, fallback.PathRemote, err
	}
	if !m.fallback.TryRemote() {
		text, segs, err := m.inferCPU(ctx, samples, p)
		if err == nil {
			m.fallback.Served(fallback.PathLocalCPU)
			return text, segs, fallback.PathLocalCPU, nil
		}
		// No fallback to go to: the provider may be back.
		m.deps.Log.Warn("stt: local CPU fallback failed inside the remote's backoff; asking the remote", "err", err)
	}
	text, segs, err := m.inferProvider(ctx, ep, samples, p)
	if err == nil {
		m.fallback.Succeeded()
		m.fallback.Served(fallback.PathRemote)
		return text, segs, fallback.PathRemote, nil
	}
	var ee *EngineError
	if !errors.As(err, &ee) || !ee.Remote || !fallback.FallsBackOn(ee.Status) || ctx.Err() != nil {
		m.fallback.Errored(err)
		return "", nil, fallback.PathRemote, err
	}
	m.fallback.Failed(err)
	if ee.Status == http.StatusUnauthorized || ee.Status == http.StatusForbidden {
		// An expired key shouldn't silence the house, but someone has to fix it.
		m.deps.Log.Error("stt: remote provider refused the API key; falling back to local CPU", "err", err)
	} else {
		m.deps.Log.Warn("stt: remote provider failed; falling back to local CPU", "err", err)
	}
	text, segs, cerr := m.inferCPU(ctx, samples, p)
	if cerr != nil {
		m.deps.Log.Error("stt: local CPU fallback unavailable", "err", cerr)
		return "", nil, fallback.PathRemote, &EngineError{Msg: ee.Msg + " (local CPU fallback unavailable: " + cerr.Error() + ")",
			Stderr: ee.Stderr, Remote: true, Status: ee.Status}
	}
	m.fallback.Served(fallback.PathLocalCPU)
	return text, segs, fallback.PathLocalCPU, nil
}

// inferCPU transcribes the clip on the label's local whisper-server forced to CPU.
func (m *Module) inferCPU(ctx context.Context, samples []float32, p inferenceParams) (string, []Segment, error) {
	ep, err := m.FallbackEngine.Resolve(ctx, "stt")
	if err != nil {
		return "", nil, err
	}
	return m.inferLocal(ctx, ep.BaseURL, samples, p)
}

// FallbackStatus reports the remote stt label's fallback: the setting seen by the latest
// request, the fallback count, the last remote error and the backoff (the labels API).
func (m *Module) FallbackStatus() fallback.Status {
	st := m.fallback.Status()
	st.Mode, _ = m.fallbackMode.Load().(string)
	return st
}
