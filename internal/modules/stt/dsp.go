package stt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// SampleRate is what both whisper-server and the speaker model get: every upload is resampled
// to 16 kHz mono first, whatever rate it arrived at (legacy _load_for_whisper and
// _load_wav_mono_16k did the same with scipy's resample_poly).
const SampleRate = 16000

// ErrBadAudio wraps any audio that cannot be decoded.
var ErrBadAudio = errors.New("stt: invalid audio")

// DecodeWAV reads a 16-bit PCM WAV (any rate, any channel count) as 16 kHz mono samples.
func DecodeWAV(wav []byte) ([]float32, error) {
	s, rate, err := audio.ReadWAV(bytes.NewReader(wav))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadAudio, err)
	}
	if rate <= 0 {
		return nil, fmt.Errorf("%w: sample rate %d", ErrBadAudio, rate)
	}
	return Resample(s, rate, SampleRate), nil
}

// EncodeWAV writes 16 kHz mono samples as a 16-bit PCM WAV (what whisper-server gets).
func EncodeWAV(samples []float32) []byte {
	var b bytes.Buffer
	_ = audio.WriteWAV(&b, samples, SampleRate) // a bytes.Buffer never fails
	return b.Bytes()
}

// Resample converts mono samples between rates with a Hann-windowed sinc (band-limited
// interpolation). When downsampling, the kernel's cutoff drops to the output Nyquist, so it
// doubles as the anti-alias filter resample_poly applied.
func Resample(in []float32, from, to int) []float32 {
	if from == to || len(in) == 0 {
		return in
	}
	const zeroCrossings = 16
	ratio := float64(from) / float64(to)
	cutoff := min(1.0, 1/ratio) // fraction of the input Nyquist that survives
	half := float64(zeroCrossings) / cutoff
	n := int(math.Ceil(float64(len(in)) / ratio))
	out := make([]float32, n)
	for i := range out {
		t := float64(i) * ratio // position in input samples
		lo := max(0, int(math.Ceil(t-half)))
		hi := min(len(in)-1, int(math.Floor(t+half)))
		var acc, wsum float64
		for j := lo; j <= hi; j++ {
			x := (t - float64(j)) * cutoff
			w := 0.5 + 0.5*math.Cos(math.Pi*(t-float64(j))/half)
			k := w * cutoff * sinc(x)
			acc += float64(in[j]) * k
			wsum += k
		}
		if wsum != 0 {
			acc /= wsum // unity DC gain at the edges too
		}
		out[i] = float32(acc)
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

// Preprocess is the legacy ?preprocess=true pipeline (app/audio.py preprocess_audio): RMS
// normalise to -20 dBFS, then trim leading and trailing silence below -40 dBFS.
func Preprocess(s []float32) []float32 {
	return trimSilence(normalizeRMS(s, -20), SampleRate, -40, 100)
}

func normalizeRMS(s []float32, targetDB float64) []float32 {
	r := rms(s)
	out := slices.Clone(s)
	if r < 1e-10 {
		return out
	}
	gain := math.Pow(10, targetDB/20) / r
	for i, v := range out {
		out[i] = float32(max(-1, min(1, float64(v)*gain)))
	}
	return out
}

// trimSilence ports app/audio.py trim_silence: frames of minSilenceMs, one frame of padding
// before the first and two after the last loud frame; an all-silent clip keeps one frame.
func trimSilence(s []float32, rate int, thresholdDB float64, minSilenceMs int) []float32 {
	th := math.Pow(10, thresholdDB/20)
	frame := max(1, rate*minSilenceMs/1000)
	start := -1
	for i := 0; i < len(s)-frame; i += frame {
		if rms(s[i:i+frame]) > th {
			start = max(0, i-frame)
			break
		}
	}
	if start < 0 {
		return s[:min(len(s), max(1, frame))]
	}
	end := len(s)
	for i := len(s) - frame; i > start; i -= frame {
		if rms(s[i:i+frame]) > th {
			end = min(len(s), i+2*frame)
			break
		}
	}
	return s[start:end]
}

func rms(s []float32) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}

// VAD frame length and thresholds. A frame is speech when its level is above an adaptive
// threshold: the clip's noise floor (10th-percentile frame level) + 6 dB, clamped to
// [-45, -30] dBFS. The clamp keeps a dead-quiet room from counting breaths (floor) and a
// steady loud signal from being measured against itself (ceiling).
const (
	vadFrameMs    = 20
	vadFloorDB    = -45.0
	vadCeilingDB  = -30.0
	vadAboveNoise = 6.0
)

// SpeechSeconds is a simple energy VAD (D37): how many seconds of the 16 kHz clip are speech.
func SpeechSeconds(s []float32) float64 {
	frame := SampleRate * vadFrameMs / 1000
	n := len(s) / frame
	if n == 0 {
		return 0
	}
	levels := make([]float64, n)
	for i := range levels {
		levels[i] = dbfs(rms(s[i*frame : (i+1)*frame]))
	}
	sorted := slices.Clone(levels)
	slices.Sort(sorted)
	noise := sorted[n/10]
	th := max(vadFloorDB, min(vadCeilingDB, noise+vadAboveNoise))
	speech := 0
	for _, l := range levels {
		if l > th {
			speech++
		}
	}
	return float64(speech*vadFrameMs) / 1000
}

func dbfs(r float64) float64 {
	if r < 1e-10 {
		return -200
	}
	return 20 * math.Log10(r)
}

// --- embeddings ---

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n) + 1e-9
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range min(len(a), len(b)) {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// centroid is the L2-normalised mean of unit vectors (legacy _load_member_embedding).
func centroid(vs [][]float32) []float32 {
	if len(vs) == 0 {
		return nil
	}
	sum := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i := range min(len(sum), len(v)) {
			sum[i] += v[i]
		}
	}
	return normalize(sum)
}

func encodeEmbedding(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeEmbedding(b []byte, dim int) ([]float32, error) {
	if dim <= 0 || len(b) != 4*dim {
		return nil, fmt.Errorf("stt: voiceprint blob is %d bytes, want %d", len(b), 4*dim)
	}
	v := make([]float32, dim)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
