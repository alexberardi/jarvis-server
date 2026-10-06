package sherpa

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"
)

// SpeakerConfig configures a speaker-embedding extractor.
type SpeakerConfig struct {
	Model      string // path to the .onnx model (e.g. 3D-Speaker ERes2Net, NeMo TitaNet)
	NumThreads int
}

// SpeakerExtractor turns audio into a fixed-size speaker embedding.
// It is safe for concurrent use; calls are serialized internally.
type SpeakerExtractor struct {
	mu  sync.Mutex
	ptr uintptr
	dim int
}

// NewSpeakerExtractor loads a speaker-embedding model. Load must have succeeded first.
func NewSpeakerExtractor(cfg SpeakerConfig) (*SpeakerExtractor, error) {
	if capi.createSpeakerExtractor == nil {
		return nil, errors.New("sherpa-onnx: Load has not been called")
	}
	var cs cstrings
	c := speakerEmbeddingExtractorConfig{
		Model:      cs.of(cfg.Model),
		NumThreads: int32(max(cfg.NumThreads, 1)),
		Provider:   cs.of("cpu"),
	}
	p := capi.createSpeakerExtractor(&c)
	runtime.KeepAlive(&cs)
	if p == 0 {
		return nil, errors.New("sherpa-onnx: failed to create speaker extractor for " + cfg.Model)
	}
	return &SpeakerExtractor{ptr: p, dim: int(capi.speakerDim(p))}, nil
}

// Dim is the embedding length.
func (e *SpeakerExtractor) Dim() int { return e.dim }

// ErrTooShort means the clip did not contain enough audio to compute an embedding.
var ErrTooShort = errors.New("sherpa-onnx: audio too short for a speaker embedding")

// Embed computes the raw (un-normalized) embedding for mono samples in [-1, 1].
func (e *SpeakerExtractor) Embed(samples []float32, sampleRate int) ([]float32, error) {
	if len(samples) == 0 {
		return nil, ErrTooShort
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	s := capi.speakerCreateStream(e.ptr)
	defer capi.destroyStream(s)
	capi.streamAcceptWaveform(s, int32(sampleRate), &samples[0], int32(len(samples)))
	capi.streamInputFinished(s)
	if capi.speakerIsReady(e.ptr, s) == 0 {
		return nil, ErrTooShort
	}
	v := capi.speakerCompute(e.ptr, s)
	if v == nil {
		return nil, errors.New("sherpa-onnx: embedding computation failed")
	}
	defer capi.speakerDestroyEmbedding(v)
	out := make([]float32, e.dim)
	copy(out, unsafe.Slice((*float32)(v), e.dim))
	return out, nil
}

// Close releases the native extractor.
func (e *SpeakerExtractor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ptr != 0 {
		capi.destroySpeakerExtractor(e.ptr)
		e.ptr = 0
	}
}
