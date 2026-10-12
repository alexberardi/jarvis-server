package voice

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

// Capture format, as the Python node: 48 kHz S16 mono in 80 ms chunks (go-pi-node.md §1.1
// row 3). Wake detection decimates to 16 kHz; STT gets the 48 kHz audio (§8 decision 1).
const (
	CaptureRate  = 48000
	ChunkSamples = 3840 // 80 ms at 48 kHz
	chunkSecs    = float64(ChunkSamples) / CaptureRate
	decimation   = CaptureRate / 16000

	// preWakeChunks is the pre-wake RMS window: int(5.0 / 0.08) chunks (N:core/wake_loop.py
	// PRE_WAKE_VAD_FRAMES).
	preWakeChunks = 62
	// wakeClipChunks is the wake clip prepended to speaker_audio: round(2.0 / 0.08) chunks
	// (WAKE_CLIP_SECONDS). jarvisd's wake verification slices its leading 2.2 s.
	wakeClipChunks = 25
)

// Endpointing is the Python node's RMS endpointing (N:scripts/speech_to_text.py listen(),
// N:core/vad_thresholds.py adaptive_silence_threshold), with the same defaults.
type Endpointing struct {
	SilenceThreshold float64 // static fallback, 300
	SilenceSecs      float64 // listen_silence_duration_secs, 0.8
	MinSecs          float64 // min_record_seconds, 2
	MaxSecs          float64 // max_record_seconds, 7
	AutoMultiplier   float64 // silence_threshold_auto_multiplier, 2 (0 disables adaptive)
	AutoFloor        float64 // 200
	AutoCeiling      float64 // 1000
	// OnsetWaitSecs holds off the silence stop until speech is heard (a chunk at or above the
	// threshold) or this long has gone by, so a pause after the wake word longer than
	// SilenceSecs doesn't cut the speaker off. 0 (the default) is the Python node's listen():
	// silence counts from the first chunk. MaxSecs still applies.
	OnsetWaitSecs float64
}

// DefaultEndpointing is the Python node's configuration out of the box.
func DefaultEndpointing() Endpointing {
	return Endpointing{SilenceThreshold: 300, SilenceSecs: 0.8, MinSecs: 2, MaxSecs: 7,
		AutoMultiplier: 2, AutoFloor: 200, AutoCeiling: 1000}
}

// RMS of 16-bit samples (calculate_rms).
func RMS(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		f := float64(v)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(s)))
}

// Threshold is the silence threshold for one recording: clamp(median pre-wake RMS ×
// multiplier, floor, ceiling) when there is a pre-wake window, else the static value.
// The median is Python's rms_sorted[n // 2] rounded to 0.1, and the product is truncated.
func (e Endpointing) Threshold(preWakeRMS []float64) float64 {
	if e.AutoMultiplier <= 0 || len(preWakeRMS) == 0 {
		return e.SilenceThreshold
	}
	s := slices.Clone(preWakeRMS)
	slices.Sort(s)
	median := math.Round(s[len(s)/2]*10) / 10
	if median <= 0 {
		return e.SilenceThreshold
	}
	return max(e.AutoFloor, min(e.AutoCeiling, math.Trunc(median*e.AutoMultiplier)))
}

// frames converts seconds to whole chunks the way listen() does: int(secs / chunk_secs).
func frames(secs float64) int { return int(secs / chunkSecs) }

// recorder is listen()'s main loop: append chunks until there have been SilenceSecs of
// consecutive chunks below the threshold and at least MinSecs have gone by, or MaxSecs.
// With an onset wait the silence stop also needs speech heard or the wait over.
type recorder struct {
	threshold                float64
	silenceFrames, minFrames int
	maxFrames, onsetFrames   int

	pcm    []int16
	n      int // chunks so far
	silent int
	heard  bool // a chunk at or above the threshold
	done   bool
	hitMax bool
}

func (e Endpointing) newRecorder(threshold float64) *recorder {
	minF := max(1, frames(e.MinSecs))
	return &recorder{threshold: threshold, silenceFrames: max(1, frames(e.SilenceSecs)),
		minFrames: minF, maxFrames: max(minF, frames(e.MaxSecs)), onsetFrames: max(0, frames(e.OnsetWaitSecs))}
}

// add takes one chunk and reports whether the recording is complete.
func (r *recorder) add(chunk []int16) bool {
	if r.done {
		return true
	}
	frameCount := r.n
	r.n++
	r.pcm = append(r.pcm, chunk...)
	if RMS(chunk) < r.threshold {
		r.silent++
	} else {
		r.silent, r.heard = 0, true
	}
	onset := r.heard || frameCount >= r.onsetFrames
	if onset && r.silent >= r.silenceFrames && frameCount >= r.minFrames {
		r.done = true
	} else if r.n >= r.maxFrames {
		r.done, r.hitMax = true, true
	}
	return r.done
}

// Normalize is _normalize_frames_to_target_rms with the node's defaults (-18 dBFS target,
// at most +18 dB, skipped under +0.5 dB): remove DC, then one static gain. Like numpy's
// astype(int16) after clip, values truncate toward zero.
func Normalize(pcm []int16) []int16 {
	const targetDB, maxGainDB, minGainDB = -18.0, 18.0, 0.5
	if len(pcm) == 0 {
		return pcm
	}
	var mean float64
	for _, v := range pcm {
		mean += float64(v)
	}
	mean /= float64(len(pcm))
	var sq float64
	for _, v := range pcm {
		d := float64(v) - mean
		sq += d * d
	}
	rms := math.Sqrt(sq / float64(len(pcm)))
	if rms < 1 {
		return pcm
	}
	current := 20 * math.Log10(rms/32768)
	gain := 1.0
	if needed := targetDB - current; needed >= minGainDB {
		gain = math.Pow(10, min(needed, maxGainDB)/20)
	}
	out := make([]int16, len(pcm))
	for i, v := range pcm {
		out[i] = int16(math.Trunc(max(-32768, min(32767, (float64(v)-mean)*gain))))
	}
	return out
}

// bracketed is N:core/voice_filters.py _BRACKETED_ANNOTATION_RE.
var bracketed = regexp.MustCompile(`^\s*(?:[\*\[\(\<][^\*\]\)\>]+[\*\]\)\>]\s*[\.,!?\-]?\s*)+\.?\s*$`)

// IsNonSpeech is voice_filters.is_non_speech: empty, whitespace, "..." or only bracketed
// annotations ([BLANK_AUDIO], (wind blowing), *sniff*). Anything that looks like words goes to
// command-center, whose not_for_me classifier decides.
func IsNonSpeech(text string) bool {
	stripped := strings.TrimSpace(text)
	if stripped == "" {
		return true
	}
	if body := strings.TrimRight(stripped, ".,!?"); body != "" && bracketed.MatchString(body) {
		return true
	}
	return strings.Trim(stripped, ".") == ""
}
