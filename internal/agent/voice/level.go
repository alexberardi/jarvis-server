package voice

import (
	"math"
	"slices"
)

// Speech level, the node's half of command-center's follow-up gate (go-pi-node.md §3.10).
// Uploads are normalised to -18 dBFS, so only the node knows how loud an answer really was. It
// reports each follow-up's speech level relative to the woken command's; command-center
// weighs a much quieter answer (across the room, a TV) together with the voice match and the
// transcript.

// minLevelChunks is the least speech (80 ms chunks at or above the silence threshold) a
// recording needs for a level: fewer and the level is reported as unknown.
const minLevelChunks = 3

// levelPercentile picks the speech level among the loud chunks: high enough to ignore word
// tails and the dips between words, low enough that one plosive or a door slam can't set it.
const levelPercentile = 0.75

// SpeechLevel is a recording's speech level in dBFS: the 75th-percentile RMS of its 80 ms
// chunks that are at or above threshold (the silence threshold the recording was endpointed
// with). ok is false when fewer than minLevelChunks chunks qualify.
func SpeechLevel(pcm []int16, threshold float64) (db float64, ok bool) {
	var loud []float64
	for i := 0; i+ChunkSamples <= len(pcm); i += ChunkSamples {
		if r := RMS(pcm[i : i+ChunkSamples]); r >= threshold && r > 0 {
			loud = append(loud, r)
		}
	}
	if len(loud) < minLevelChunks {
		return 0, false
	}
	slices.Sort(loud)
	r := loud[min(len(loud)-1, int(levelPercentile*float64(len(loud))))]
	return 20 * math.Log10(r/32768), true
}

// levelVsWake is a follow-up's speech level relative to the woken command's, in dB (negative:
// quieter), rounded to 0.1; nil when either level is unknown.
func levelVsWake(s *session, pcm []int16) *float64 {
	if s.wakeLevel == nil {
		return nil
	}
	db, ok := SpeechLevel(pcm, s.threshold)
	if !ok {
		return nil
	}
	d := math.Round((db-*s.wakeLevel)*10) / 10
	return &d
}
