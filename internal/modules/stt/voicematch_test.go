package stt

import (
	"context"
	"slices"
	"testing"
)

// The follow-up gate's same-voice comparison (voicematch.go).

func TestSpeechOnlyDropsTheSilenceAround(t *testing.T) {
	quiet := make([]float32, SampleRate) // 1 s of digital silence
	speech := sine(SampleRate, 1.5, 220)
	got := SpeechOnly(slices.Concat(quiet, speech, quiet))
	secs := float64(len(got)) / SampleRate
	// The speech plus at most one 20 ms frame of context on each side.
	if secs < 1.48 || secs > 1.54 {
		t.Fatalf("kept %.3f s, want ~1.5 s", secs)
	}
	if SpeechOnly(nil) != nil {
		t.Fatal("empty in, empty out")
	}
}

func TestVoiceSimilarity(t *testing.T) {
	sm := toneEmbedder{}
	pad := make([]float32, SampleRate/2)
	low, err := EmbedVoice(sm, slices.Concat(pad, sine(SampleRate, 2, 220), pad))
	if err != nil {
		t.Fatal(err)
	}
	low2, _ := EmbedVoice(sm, slices.Concat(pad, sine(SampleRate, 1.5, 220), pad))
	high, _ := EmbedVoice(sm, slices.Concat(pad, sine(SampleRate, 2, 880), pad))
	if !low.Usable() || !high.Usable() {
		t.Fatalf("usable: %+v %+v", low, high)
	}
	if s, ok := low.Similarity(low2); !ok || s < 0.99 {
		t.Fatalf("same voice: %.3f %v", s, ok)
	}
	if s, ok := low.Similarity(high); !ok || s > 0.2 {
		t.Fatalf("different voices: %.3f %v", s, ok)
	}

	short, _ := EmbedVoice(sm, slices.Concat(pad, sine(SampleRate, 0.5, 220), pad))
	if short.Usable() || short.SpeechSecs > 0.6 {
		t.Fatalf("0.5 s of speech is too little to compare: %+v", short)
	}
	if _, ok := low.Similarity(short); ok {
		t.Fatal("an unusable voice has no similarity")
	}
	other, _ := EmbedVoice(toneEmbedder{id: "tone-v2"}, slices.Concat(pad, sine(SampleRate, 2, 220), pad))
	if _, ok := low.Similarity(other); ok {
		t.Fatal("voices from different models are never compared")
	}
}

func TestVoiceOfDecodesTheUpload(t *testing.T) {
	m := &Module{Speaker: toneEmbedder{}}
	pad := make([]float32, SampleRate/2)
	v, err := m.VoiceOf(context.Background(), EncodeWAV(slices.Concat(pad, sine(SampleRate, 2, 330), pad)))
	if err != nil || !v.Usable() || v.ModelID != "tone-v1" {
		t.Fatalf("voice %+v, err %v", v, err)
	}
	if _, err := m.VoiceOf(context.Background(), []byte("not a wav")); err == nil {
		t.Fatal("bad audio is an error")
	}
}
