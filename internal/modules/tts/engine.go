package tts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/voice/sherpa"
)

// ErrNotInstalled means no Kokoro model is installed (or configured) and none is loaded.
var ErrNotInstalled = errors.New("tts: no Kokoro model installed")

// synth is what the module needs from a loaded engine: *sherpa.TTS, or a fake in tests.
type synth interface {
	Generate(text string, sid int, speed float32) ([]float32, error)
	SampleRate() int
	NumSpeakers() int
	Close()
}

// engineKey identifies a loaded engine: the model directory and its front end. A voice change
// within the same language reuses the engine (the sid is per call); a change of language
// (lexicon) or model reloads it.
type engineKey struct {
	Dir, Lexicon, Lang string
}

func (k engineKey) String() string { return fmt.Sprintf("%s [%s %s]", k.Dir, k.Lexicon, k.Lang) }

// engineRef is a loaded engine with a reference count: a replaced engine is closed once its
// last stream finishes.
type engineRef struct {
	s       synth
	key     engineKey
	refs    int
	retired bool
}

// engines holds the current engine and swaps it when the key changes. The legacy provider
// manager's rule carries over: a failed reload keeps the previous engine (the service never
// goes silent); a failed load is retried at most every retryAfter.
type engines struct {
	load       func(engineKey) (synth, error)
	log        *slog.Logger
	retryAfter time.Duration
	now        func() time.Time

	loading sync.Mutex // one load at a time
	mu      sync.Mutex // guards the fields below
	cur     *engineRef
	failKey engineKey
	failAt  time.Time
	failErr error
	closed  bool
}

// current returns the loaded engine's sample rate, or 0 when nothing is loaded.
func (e *engines) sampleRate() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur == nil {
		return 0
	}
	return e.cur.s.SampleRate()
}

// take returns the current engine if its key matches (or any engine when match is false).
func (e *engines) take(key engineKey, match bool) *engineRef {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cur == nil || (match && e.cur.key != key) {
		return nil
	}
	e.cur.refs++
	return e.cur
}

// acquire returns an engine for key, loading it if needed. installed=false means the model
// manager has nothing: the current engine (if any) keeps serving. Release the result.
func (e *engines) acquire(key engineKey, installed bool) (*engineRef, error) {
	if !installed {
		if r := e.take(key, false); r != nil {
			return r, nil
		}
		return nil, ErrNotInstalled
	}
	if r := e.take(key, true); r != nil {
		return r, nil
	}
	e.loading.Lock()
	defer e.loading.Unlock()
	if r := e.take(key, true); r != nil { // loaded while we waited
		return r, nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, errors.New("tts: shutting down")
	}
	recentFail := e.failErr != nil && e.failKey == key && e.now().Sub(e.failAt) < e.retryAfter
	failErr := e.failErr
	e.mu.Unlock()
	if recentFail {
		if r := e.take(key, false); r != nil {
			return r, nil
		}
		return nil, failErr
	}

	start := e.now()
	s, err := e.load(key)
	if err != nil {
		e.mu.Lock()
		e.failKey, e.failAt, e.failErr = key, e.now(), err
		e.mu.Unlock()
		if r := e.take(key, false); r != nil {
			e.log.Warn("tts: engine reload failed; keeping the previous engine", "want", key.String(), "keep", r.key.String(), "err", err)
			return r, nil
		}
		e.log.Error("tts: engine load failed", "model", key.String(), "err", err)
		return nil, err
	}
	e.log.Info("tts: engine loaded", "model", key.String(), "sample_rate", s.SampleRate(),
		"speakers", s.NumSpeakers(), "elapsed", e.now().Sub(start).Round(time.Millisecond))
	r := &engineRef{s: s, key: key, refs: 1}
	e.mu.Lock()
	old := e.cur
	e.cur = r
	e.failErr = nil
	closeOld := old != nil && old.refs == 0
	if old != nil {
		old.retired = true
	}
	e.mu.Unlock()
	if closeOld {
		old.s.Close()
	}
	return r, nil
}

func (e *engines) release(r *engineRef) {
	e.mu.Lock()
	r.refs--
	closeIt := r.retired && r.refs == 0
	e.mu.Unlock()
	if closeIt {
		r.s.Close()
	}
}

// shutdown retires the current engine (closed now if idle, else when its last stream ends)
// and refuses new loads.
func (e *engines) shutdown() {
	e.loading.Lock()
	defer e.loading.Unlock()
	e.mu.Lock()
	e.closed = true
	r := e.cur
	e.cur = nil
	closeIt := false
	if r != nil {
		r.retired = true
		closeIt = r.refs == 0
	}
	e.mu.Unlock()
	if closeIt {
		r.s.Close()
	}
}

// kokoroLoader loads real engines through sherpa-onnx, binding the native libraries (once)
// under libDir first.
func kokoroLoader(libDir string, threads int) func(engineKey) (synth, error) {
	return func(k engineKey) (synth, error) {
		if err := sherpa.Load(libDir); err != nil {
			return nil, err
		}
		t, err := sherpa.NewKokoro(sherpa.KokoroConfig{Dir: k.Dir, Lexicon: k.Lexicon, Lang: k.Lang, NumThreads: threads})
		if err != nil {
			return nil, err
		}
		return t, nil
	}
}

// keyFor builds the engine key for a model directory and voice. Lexicon files missing from
// the directory are dropped (another Kokoro pack may not ship them; espeak-ng covers it).
func keyFor(dir string, v voice) engineKey {
	var lex []string
	for _, f := range strings.Split(v.Lexicon, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			lex = append(lex, f)
		}
	}
	return engineKey{Dir: dir, Lexicon: strings.Join(lex, ","), Lang: v.Lang}
}

// modelDir resolves the Kokoro directory: the override, else the model manager.
func (m *Module) modelDir(ctx context.Context) (string, bool) {
	if m.ModelDir != "" {
		return m.ModelDir, true
	}
	if m.Models == nil {
		return "", false
	}
	return m.Models.ModelPath(ctx, "tts")
}
