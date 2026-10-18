package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand"
	"os"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// renderCase builds a case's mic audio at the TTS's sample rate (the first spoken part sets
// it; 16 kHz when nothing is spoken). speak returns a WAV.
func renderCase(ctx context.Context, speak func(context.Context, string) ([]byte, error), parts []AudioPart, seed int64) ([]float32, int, error) {
	spoken := make([][]float32, len(parts))
	rate := 0
	for i, p := range parts {
		if p.Say == "" {
			continue
		}
		wav, err := speak(ctx, p.Say)
		if err != nil {
			return nil, 0, fmt.Errorf("tts %q: %w", p.Say, err)
		}
		s, r, err := audio.ReadWAV(bytes.NewReader(wav))
		if err != nil {
			return nil, 0, fmt.Errorf("tts %q: %w", p.Say, err)
		}
		if rate == 0 {
			rate = r
		} else if r != rate {
			return nil, 0, fmt.Errorf("tts %q: %d Hz after %d Hz", p.Say, r, rate)
		}
		spoken[i] = s
	}
	if rate == 0 {
		rate = 16000
	}
	rng := rand.New(rand.NewSource(seed))
	var out []float32
	for i, p := range parts {
		var s []float32
		switch {
		case p.Say != "":
			s = spoken[i]
		case p.Silence > 0:
			s = make([]float32, int(p.Silence*float64(rate)))
		case p.Noise == "pink":
			s = pinkNoise(rng, int(p.Secs*float64(rate)), gainOf(p))
		case p.Noise == "music":
			s = music(int(p.Secs*float64(rate)), rate, gainOf(p))
		}
		out = mix(out, s, int(p.Over*float64(rate)))
	}
	return out, rate, nil
}

// mix adds s to out starting over samples before out's end (0: appended), clipping at ±1.
func mix(out, s []float32, over int) []float32 {
	start := max(0, len(out)-over)
	if end := start + len(s); end > len(out) {
		out = append(out, make([]float32, end-len(out))...)
	}
	for i, v := range s {
		out[start+i] = max(-1, min(1, out[start+i]+v))
	}
	return out
}

func gainOf(p AudioPart) float32 {
	if p.Gain > 0 {
		return float32(min(p.Gain, 1))
	}
	return 0.3
}

// pinkNoise is Paul Kellet's economy filter over white noise, scaled to peak at gain.
func pinkNoise(rng *rand.Rand, n int, gain float32) []float32 {
	out := make([]float32, n)
	var b0, b1, b2, peak float64
	for i := range out {
		w := rng.Float64()*2 - 1
		b0 = 0.99765*b0 + w*0.0990460
		b1 = 0.96300*b1 + w*0.2965164
		b2 = 0.57000*b2 + w*1.0526913
		v := b0 + b1 + b2 + w*0.1848
		out[i] = float32(v)
		peak = max(peak, math.Abs(v))
	}
	if peak > 0 {
		for i := range out {
			out[i] = out[i] / float32(peak) * gain
		}
	}
	return out
}

// music is a I-V-vi-IV chord loop (half a second a chord) over a kick on every chord: the
// shape of music, made here so no recording's licence is involved.
func music(n, rate int, gain float32) []float32 {
	chords := [][]float64{{261.63, 329.63, 392.00}, {196.00, 246.94, 293.66}, {220.00, 261.63, 329.63}, {174.61, 220.00, 261.63}}
	out := make([]float32, n)
	beat := max(1, rate/2)
	for i := range out {
		t := float64(i) / float64(rate)
		v := 0.0
		for _, f := range chords[(i/beat)%len(chords)] {
			v += math.Sin(2*math.Pi*f*t) / 3
		}
		k := float64(i%beat) / float64(rate) // a 60 Hz thump decaying from each beat
		v = 0.6*v + 0.4*math.Sin(2*math.Pi*60*k)*math.Exp(-k/0.04)
		out[i] = float32(v) * gain
	}
	return out
}

// silenceFloor is the level below which a recording counts as silent (the virtual speaker
// carries no noise of its own; a WAV speaker none at all).
const silenceFloor = 0.02

// voiced is the seconds of audio above the silence floor (in 10 ms frames) and where the
// first such frame starts (0 when there is none).
func voiced(s []float32, rate int) (secs, first float64) {
	frame := rate / 100
	if frame <= 0 {
		return 0, 0
	}
	first = -1
	n := 0
	for i := 0; i+frame <= len(s); i += frame {
		for _, v := range s[i : i+frame] {
			if v > silenceFloor || v < -silenceFloor {
				if first < 0 {
					first = float64(i) / float64(rate)
				}
				n++
				break
			}
		}
	}
	return float64(n) / 100, max(first, 0)
}

func readWAVFile(path string) ([]float32, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	return audio.ReadWAV(f)
}

func writeWAVFile(path string, s []float32, rate int) error {
	var b bytes.Buffer
	if err := audio.WriteWAV(&b, s, rate); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
