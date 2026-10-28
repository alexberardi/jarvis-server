package voice

import (
	"fmt"
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
	// PreRollSecs keeps up to this much of the audio heard between the wake fire and the end
	// of the wake chime, and puts it in front of the recording: a command said right after
	// the wake word starts under the chime (#229). Endpointing still starts after the chime,
	// and the wake word itself is not kept (it ended before the fire). 0 (the default) is the
	// Python node's history_secs=0: recording starts after the chime.
	PreRollSecs float64
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

// PreWakeVAD classifies the pre-wake window's chunks as speech for pre_wake_speech_seconds
// (N:core/vad_thresholds.py pre_wake_vad_threshold, auto_pre_wake_vad_threshold): command-center's
// "room was quiet" direction hint.
type PreWakeVAD struct {
	Static         float64 // pre_wake_vad_rms_threshold, 2500
	AutoMultiplier float64 // pre_wake_vad_auto_multiplier, 3 (0 disables auto)
	AutoMin        float64 // pre_wake_vad_auto_min, 300
	AutoMax        float64 // pre_wake_vad_auto_max, 2500
}

// DefaultPreWakeVAD is the Python node's configuration out of the box.
func DefaultPreWakeVAD() PreWakeVAD {
	return PreWakeVAD{Static: 2500, AutoMultiplier: 3, AutoMin: 300, AutoMax: 2500}
}

// SpeechSecs is pre_wake_speech_seconds: the window's chunks above the threshold × 80 ms,
// rounded to 0.01 s, with the threshold used and its source. The threshold is clamp(median ×
// multiplier, min, max) off the window's own floor ("auto"); the static one without a floor
// ("static") or during the node's own music, whose bleed would be taken for the floor
// ("static_self_playback").
func (v PreWakeVAD) SpeechSecs(preWakeRMS []float64, selfPlaying bool) (secs, threshold float64, source string) {
	threshold, source = v.Static, "static"
	switch {
	case selfPlaying:
		source = "static_self_playback"
	case v.AutoMultiplier > 0 && len(preWakeRMS) > 0:
		s := slices.Clone(preWakeRMS)
		slices.Sort(s)
		if median := math.Round(s[len(s)/2]*10) / 10; median > 0 {
			threshold, source = max(v.AutoMin, min(v.AutoMax, median*v.AutoMultiplier)), "auto"
		}
	}
	n := 0
	for _, r := range preWakeRMS {
		if r > threshold {
			n++
		}
	}
	return math.Round(float64(n)*chunkSecs*100) / 100, threshold, source
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

// levelSummary describes a recording against its silence threshold, for the recording's log
// line: the 80 ms chunk RMS median, 90th percentile and peak, and how long it was at or above
// the threshold. A recording cut off mid-sentence shows speech mostly under the threshold.
func levelSummary(pcm []int16, threshold float64) []any {
	var rms []float64
	for i := 0; i+ChunkSamples <= len(pcm); i += ChunkSamples {
		rms = append(rms, RMS(pcm[i:i+ChunkSamples]))
	}
	if len(rms) == 0 {
		return []any{"threshold", fmt.Sprintf("%.0f", threshold)}
	}
	loud := 0
	for _, r := range rms {
		if r >= threshold {
			loud++
		}
	}
	slices.Sort(rms)
	at := func(q float64) string { return fmt.Sprintf("%.0f", rms[int(q*float64(len(rms)-1))]) }
	return []any{"threshold", fmt.Sprintf("%.0f", threshold), "loud_s", fmt.Sprintf("%.2f", float64(loud)*chunkSecs),
		"rms_p50", at(0.5), "rms_p90", at(0.9), "rms_max", at(1)}
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

// leadingAnnotations are the bracketed annotations at the start of a transcript.
var leadingAnnotations = regexp.MustCompile(`^\s*(?:[\*\[\(\<][^\*\]\)\>]+[\*\]\)\>]\s*[\.,!?\-]?\s*)+`)

// StripNonSpeech drops the bracketed annotations whisper puts in front of words ("(beep)
// Open Google Chrome.": the wake chime, #358), which can steer the model to the wrong tool.
// A transcript of annotations only is kept as is, for IsNonSpeech and the silence path.
func StripNonSpeech(text string) string {
	text = strings.TrimSpace(text)
	rest := strings.TrimSpace(leadingAnnotations.ReplaceAllString(text, ""))
	if rest == "" || IsNonSpeech(rest) {
		return text
	}
	return rest
}

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
