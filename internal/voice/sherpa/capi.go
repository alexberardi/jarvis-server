// Package sherpa binds the sherpa-onnx C API without cgo.
//
// The shared libraries (sherpa-onnx-c-api + onnxruntime) are embedded in the binary, extracted to
// a versioned directory on first use, and loaded with purego. Struct types below mirror
// third_party/sherpa/include/c-api.h field-for-field; TestStructLayout guards that they match.
package sherpa

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// cstr is a C char* backed by Go memory. The Go GC does not move heap objects, so the address
// stays valid while the backing slice is reachable; callers keep a cstrings value alive across
// the C call.
type cstr = uintptr

type cstrings struct{ bufs [][]byte }

func (c *cstrings) of(s string) cstr {
	if s == "" {
		return 0
	}
	b := append([]byte(s), 0)
	c.bufs = append(c.bufs, b)
	return uintptr(unsafe.Pointer(&b[0]))
}

// --- mirrored C structs (sherpa-onnx v1.13.8) ---

type speakerEmbeddingExtractorConfig struct {
	Model      cstr
	NumThreads int32
	Debug      int32
	Provider   cstr
}

type ttsVitsModelConfig struct {
	Model       cstr
	Lexicon     cstr
	Tokens      cstr
	DataDir     cstr
	NoiseScale  float32
	NoiseScaleW float32
	LengthScale float32
	DictDir     cstr
}

type ttsMatchaModelConfig struct {
	AcousticModel cstr
	Vocoder       cstr
	Lexicon       cstr
	Tokens        cstr
	DataDir       cstr
	NoiseScale    float32
	LengthScale   float32
	DictDir       cstr
}

type ttsKokoroModelConfig struct {
	Model       cstr
	Voices      cstr
	Tokens      cstr
	DataDir     cstr
	LengthScale float32
	DictDir     cstr
	Lexicon     cstr
	Lang        cstr
}

type ttsKittenModelConfig struct {
	Model       cstr
	Voices      cstr
	Tokens      cstr
	DataDir     cstr
	LengthScale float32
}

type ttsZipvoiceModelConfig struct {
	Tokens        cstr
	Encoder       cstr
	Decoder       cstr
	Vocoder       cstr
	DataDir       cstr
	Lexicon       cstr
	FeatScale     float32
	TShift        float32
	TargetRMS     float32
	GuidanceScale float32
}

type ttsPocketModelConfig struct {
	LmFlow                      cstr
	LmMain                      cstr
	Encoder                     cstr
	Decoder                     cstr
	TextConditioner             cstr
	VocabJSON                   cstr
	TokenScoresJSON             cstr
	VoiceEmbeddingCacheCapacity int32
}

type ttsSupertonicModelConfig struct {
	DurationPredictor cstr
	TextEncoder       cstr
	VectorEstimator   cstr
	Vocoder           cstr
	TTSJSON           cstr
	UnicodeIndexer    cstr
	VoiceStyle        cstr
}

type ttsModelConfig struct {
	Vits       ttsVitsModelConfig
	NumThreads int32
	Debug      int32
	Provider   cstr
	Matcha     ttsMatchaModelConfig
	Kokoro     ttsKokoroModelConfig
	Kitten     ttsKittenModelConfig
	Zipvoice   ttsZipvoiceModelConfig
	Pocket     ttsPocketModelConfig
	Supertonic ttsSupertonicModelConfig
}

type ttsConfig struct {
	Model           ttsModelConfig
	RuleFsts        cstr
	MaxNumSentences int32
	RuleFars        cstr
	SilenceScale    float32
}

type generationConfig struct {
	SilenceScale        float32
	Speed               float32
	Sid                 int32
	ReferenceAudio      uintptr
	ReferenceAudioLen   int32
	ReferenceSampleRate int32
	ReferenceText       cstr
	NumSteps            int32
	Extra               cstr
}

type generatedAudio struct {
	Samples    unsafe.Pointer // C-owned; freed by destroyAudio
	N          int32
	SampleRate int32
}

// --- bound functions ---
//
// No function takes a by-value float: purego on Windows routes calls through syscall.SyscallN,
// which cannot place floats in XMM registers. Speed etc. travel inside generationConfig.

var capi struct {
	createSpeakerExtractor  func(cfg *speakerEmbeddingExtractorConfig) uintptr
	destroySpeakerExtractor func(p uintptr)
	speakerDim              func(p uintptr) int32
	speakerCreateStream     func(p uintptr) uintptr
	speakerIsReady          func(p, s uintptr) int32
	speakerCompute          func(p, s uintptr) unsafe.Pointer
	speakerDestroyEmbedding func(v unsafe.Pointer)

	streamAcceptWaveform func(s uintptr, sampleRate int32, samples *float32, n int32)
	streamInputFinished  func(s uintptr)
	destroyStream        func(s uintptr)

	createTTS          func(cfg *ttsConfig) uintptr
	destroyTTS         func(p uintptr)
	ttsSampleRate      func(p uintptr) int32
	ttsNumSpeakers     func(p uintptr) int32
	ttsGenerateWithCfg func(p uintptr, text string, cfg *generationConfig, cb, arg uintptr) *generatedAudio
	destroyAudio       func(a *generatedAudio)
}

var (
	loadOnce sync.Once
	loadErr  error
)

// ErrUnsupported is returned on platforms jarvisd ships no native voice libraries for.
var ErrUnsupported = errors.New("sherpa-onnx: no embedded libraries for " + runtime.GOOS + "/" + runtime.GOARCH)

// Load extracts the embedded libraries under baseDir (once per version) and binds the C API.
// It is safe to call repeatedly; only the first call does work.
func Load(baseDir string) error {
	loadOnce.Do(func() { loadErr = load(baseDir) })
	return loadErr
}

func load(baseDir string) error {
	dir, err := extract(baseDir)
	if err != nil {
		return err
	}
	// onnxruntime first so the C-API library's dependency is already resident on every OS
	// (Linux also has RPATH=$ORIGIN; Windows matches loaded modules by name).
	if _, err := openLibrary(dir, libOnnxRuntime); err != nil {
		return err
	}
	h, err := openLibrary(dir, libSherpaCAPI)
	if err != nil {
		return err
	}
	bind := func(fptr any, name string) {
		if err != nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("sherpa-onnx: binding %s: %v", name, r)
			}
		}()
		purego.RegisterLibFunc(fptr, h, name)
	}
	bind(&capi.createSpeakerExtractor, "SherpaOnnxCreateSpeakerEmbeddingExtractor")
	bind(&capi.destroySpeakerExtractor, "SherpaOnnxDestroySpeakerEmbeddingExtractor")
	bind(&capi.speakerDim, "SherpaOnnxSpeakerEmbeddingExtractorDim")
	bind(&capi.speakerCreateStream, "SherpaOnnxSpeakerEmbeddingExtractorCreateStream")
	bind(&capi.speakerIsReady, "SherpaOnnxSpeakerEmbeddingExtractorIsReady")
	bind(&capi.speakerCompute, "SherpaOnnxSpeakerEmbeddingExtractorComputeEmbedding")
	bind(&capi.speakerDestroyEmbedding, "SherpaOnnxSpeakerEmbeddingExtractorDestroyEmbedding")
	bind(&capi.streamAcceptWaveform, "SherpaOnnxOnlineStreamAcceptWaveform")
	bind(&capi.streamInputFinished, "SherpaOnnxOnlineStreamInputFinished")
	bind(&capi.destroyStream, "SherpaOnnxDestroyOnlineStream")
	bind(&capi.createTTS, "SherpaOnnxCreateOfflineTts")
	bind(&capi.destroyTTS, "SherpaOnnxDestroyOfflineTts")
	bind(&capi.ttsSampleRate, "SherpaOnnxOfflineTtsSampleRate")
	bind(&capi.ttsNumSpeakers, "SherpaOnnxOfflineTtsNumSpeakers")
	bind(&capi.ttsGenerateWithCfg, "SherpaOnnxOfflineTtsGenerateWithConfig")
	bind(&capi.destroyAudio, "SherpaOnnxDestroyOfflineTtsGeneratedAudio")
	return err
}
