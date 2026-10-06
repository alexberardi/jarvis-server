package sherpa

import (
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"
)

// KokoroConfig points at an unpacked sherpa-onnx Kokoro model directory
// (e.g. kokoro-multi-lang-v1_0) and selects lexicon/language.
type KokoroConfig struct {
	Dir        string
	Lexicon    string // file name inside Dir, e.g. "lexicon-gb-en.txt"
	Lang       string // espeak-ng language for OOV fallback, e.g. "en"
	NumThreads int
}

// TTS synthesizes speech. It is safe for concurrent use; calls are serialized internally.
type TTS struct {
	mu         sync.Mutex
	ptr        uintptr
	sampleRate int
	speakers   int
}

// NewKokoro loads a Kokoro model. Load must have succeeded first.
func NewKokoro(cfg KokoroConfig) (*TTS, error) {
	if capi.createTTS == nil {
		return nil, errors.New("sherpa-onnx: Load has not been called")
	}
	model, err := firstMatch(filepath.Join(cfg.Dir, "model*.onnx"))
	if err != nil {
		return nil, err
	}
	var cs cstrings
	var c ttsConfig
	c.Model.NumThreads = int32(max(cfg.NumThreads, 1))
	c.Model.Provider = cs.of("cpu")
	c.Model.Kokoro = ttsKokoroModelConfig{
		Model:       cs.of(model),
		Voices:      cs.of(filepath.Join(cfg.Dir, "voices.bin")),
		Tokens:      cs.of(filepath.Join(cfg.Dir, "tokens.txt")),
		DataDir:     cs.of(filepath.Join(cfg.Dir, "espeak-ng-data")),
		LengthScale: 1.0,
		Lang:        cs.of(cfg.Lang),
	}
	if cfg.Lexicon != "" {
		c.Model.Kokoro.Lexicon = cs.of(filepath.Join(cfg.Dir, cfg.Lexicon))
	}
	c.MaxNumSentences = 1
	p := capi.createTTS(&c)
	runtime.KeepAlive(&cs)
	if p == 0 {
		return nil, errors.New("sherpa-onnx: failed to create Kokoro TTS from " + cfg.Dir)
	}
	return &TTS{ptr: p, sampleRate: int(capi.ttsSampleRate(p)), speakers: int(capi.ttsNumSpeakers(p))}, nil
}

// SampleRate of generated audio.
func (t *TTS) SampleRate() int { return t.sampleRate }

// NumSpeakers available in the voice pack.
func (t *TTS) NumSpeakers() int { return t.speakers }

// Generate renders text with the given voice id and speed (1.0 = normal) as mono [-1, 1] samples.
func (t *TTS) Generate(text string, sid int, speed float32) ([]float32, error) {
	if sid < 0 || sid >= t.speakers {
		return nil, errors.New("sherpa-onnx: speaker id out of range")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	cfg := generationConfig{Speed: speed, Sid: int32(sid), SilenceScale: 0.2}
	ga := capi.ttsGenerateWithCfg(t.ptr, text, &cfg, 0, 0)
	if ga == nil {
		return nil, errors.New("sherpa-onnx: generation failed")
	}
	defer capi.destroyAudio(ga)
	if ga.N <= 0 || ga.Samples == nil {
		return nil, errors.New("sherpa-onnx: generation produced no audio")
	}
	out := make([]float32, ga.N)
	copy(out, unsafe.Slice((*float32)(ga.Samples), int(ga.N)))
	return out, nil
}

// Close releases the native TTS engine.
func (t *TTS) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ptr != 0 {
		capi.destroyTTS(t.ptr)
		t.ptr = 0
	}
}

func firstMatch(pattern string) (string, error) {
	m, _ := filepath.Glob(pattern)
	if len(m) == 0 {
		return "", errors.New("sherpa-onnx: no file matches " + pattern)
	}
	return m[0], nil
}
