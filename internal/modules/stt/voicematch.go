package stt

import (
	"context"
	"slices"
)

// Same-voice comparison for the follow-up gate (command-center's "is this speech for me?"
// check, docs/agent/go-pi-node.md §3.10). It answers "is this the voice that woke the node?"
// by comparing two clips of one conversation with the speaker model, and identifies nobody:
// no voiceprint is read or written, the household's voice.recognition_enabled is not
// consulted, and the vectors live only in command-center's per-conversation memory.

// minVoiceSpeechSecs is the least speech a clip needs before its embedding means anything.
// ERes2Net is reliable from about 1 s (the voice spike measured 1.5 s clips at 1.46% EER);
// below that a score is noise, so it is reported as unknown.
const minVoiceSpeechSecs = 0.8

// Voice is one clip's unit speaker embedding and how much speech it was computed from.
type Voice struct {
	Vec        []float32
	SpeechSecs float64
	ModelID    string
}

// Usable reports whether the clip had enough speech to be compared.
func (v Voice) Usable() bool { return len(v.Vec) > 0 && v.SpeechSecs >= minVoiceSpeechSecs }

// Similarity is the cosine of two voices (both unit vectors); ok=false when either is unusable
// or they came from different models.
func (v Voice) Similarity(o Voice) (float64, bool) {
	if !v.Usable() || !o.Usable() || v.ModelID != o.ModelID || len(v.Vec) != len(o.Vec) {
		return 0, false
	}
	return dot(v.Vec, o.Vec), true
}

// VoiceOf embeds the speech in a WAV (any rate) for a same-voice comparison: only the frames
// the energy VAD calls speech are embedded, so the silence and room tone around an answer
// don't dilute it. A clip without enough speech returns a Voice that is not Usable, not an
// error; errors are bad audio or a missing speaker model.
func (m *Module) VoiceOf(ctx context.Context, wav []byte) (Voice, error) {
	samples, err := DecodeWAV(wav)
	if err != nil {
		return Voice{}, err
	}
	sm, err := m.speakerModel(ctx)
	if err != nil {
		return Voice{}, err
	}
	return EmbedVoice(sm, samples)
}

// EmbedVoice is VoiceOf on 16 kHz samples with a given model.
func EmbedVoice(sm SpeakerModel, samples []float32) (Voice, error) {
	speech := SpeechOnly(samples)
	v := Voice{SpeechSecs: float64(len(speech)) / SampleRate, ModelID: sm.ID()}
	if v.SpeechSecs < minVoiceSpeechSecs {
		return v, nil
	}
	vec, err := embedClip(sm, speech)
	if err != nil {
		// Too short for the model's own window: unknown, not a failure.
		return v, nil
	}
	v.Vec = vec
	return v, nil
}

// SpeechOnly keeps the 20 ms frames SpeechSeconds counts as speech, each with one frame of
// context on either side (so word onsets and tails stay), in order.
func SpeechOnly(s []float32) []float32 {
	frame := SampleRate * vadFrameMs / 1000
	n := len(s) / frame
	if n == 0 {
		return nil
	}
	levels := make([]float64, n)
	for i := range levels {
		levels[i] = dbfs(rms(s[i*frame : (i+1)*frame]))
	}
	sorted := slices.Clone(levels)
	slices.Sort(sorted)
	th := max(vadFloorDB, min(vadCeilingDB, sorted[n/10]+vadAboveNoise))
	keep := make([]bool, n)
	for i, l := range levels {
		if l > th {
			for j := max(0, i-1); j <= min(n-1, i+1); j++ {
				keep[j] = true
			}
		}
	}
	var out []float32
	for i, k := range keep {
		if k {
			out = append(out, s[i*frame:(i+1)*frame]...)
		}
	}
	return out
}
