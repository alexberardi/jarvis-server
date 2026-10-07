package stt

import (
	"math"
	"testing"
)

// zeroCrossingHz estimates a tone's frequency.
func zeroCrossingHz(s []float32, rate int) float64 {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return float64(n) / 2 / (float64(len(s)) / float64(rate))
}

func TestResamplePreservesToneAndLevel(t *testing.T) {
	for _, from := range []int{8000, 22050, 44100, 48000} {
		out := Resample(sine(from, 1, 440), from, 16000)
		if d := len(out) - 16000; d < -1 || d > 1 {
			t.Errorf("%d Hz: %d samples", from, len(out))
		}
		mid := out[1000 : len(out)-1000]
		if hz := zeroCrossingHz(mid, 16000); math.Abs(hz-440) > 3 {
			t.Errorf("%d Hz: tone at %.1f Hz", from, hz)
		}
		if r := rms(mid); math.Abs(r-0.3/math.Sqrt2) > 0.01 {
			t.Errorf("%d Hz: rms %.4f", from, r)
		}
	}
	// Downsampling removes what the output can't carry (anti-alias): 7 kHz at 48 kHz → 16 kHz.
	if r := rms(Resample(sine(48000, 1, 7000), 48000, 16000)[1000:15000]); r < 0.15 {
		t.Errorf("in-band 7 kHz lost: %.3f", r)
	}
	if r := rms(Resample(sine(48000, 1, 12000), 48000, 16000)[1000:15000]); r > 0.02 {
		t.Errorf("12 kHz aliased into the output: %.3f", r)
	}
	if got := Resample(sine(16000, 0.1, 440), 16000, 16000); len(got) != 1600 {
		t.Error("same-rate resample changed length")
	}
}

func TestSpeechSeconds(t *testing.T) {
	cases := []struct {
		name   string
		s      []float32
		lo, hi float64
	}{
		{"silence", make([]float32, 16000*5), 0, 0},
		{"steady tone", sine(16000, 3, 220), 2.98, 3.0},
		{"3 s voice in 8 s", append(sine(16000, 3, 220), make([]float32, 16000*5)...), 2.9, 3.05},
		{"voice over room noise", append(sine(16000, 4, 220), noise(16000*4, 0.003)...), 3.9, 4.05},
		{"quiet room noise only", noise(16000*8, 0.003), 0, 0.1},
		{"too short", sine(16000, 0.01, 220), 0, 0},
	}
	for _, c := range cases {
		if got := SpeechSeconds(c.s); got < c.lo || got > c.hi {
			t.Errorf("%s: %.2f s, want [%.2f, %.2f]", c.name, got, c.lo, c.hi)
		}
	}
}

func TestPreprocessNormalisesAndTrims(t *testing.T) {
	s := append(append(make([]float32, 16000), sine(16000, 1, 440)...), make([]float32, 16000)...)
	for i := range s {
		s[i] *= 0.1
	}
	out := Preprocess(s)
	if len(out) > 16000+5*1600 || len(out) < 16000 {
		t.Errorf("trim kept %d samples", len(out))
	}
	// Legacy order: normalise the whole clip to -20 dBFS, then trim, so the kept tone is louder.
	if db := dbfs(rms(out)); db < -18 || db > -14 {
		t.Errorf("level %.1f dBFS", db)
	}
	if got := Preprocess(make([]float32, 16000)); len(got) != 1600 {
		t.Errorf("all-silent clip keeps one frame, got %d", len(got))
	}
}

func TestEmbeddingCodec(t *testing.T) {
	v := normalize([]float32{3, 4, 0})
	got, err := decodeEmbedding(encodeEmbedding(v), 3)
	if err != nil || got[0] != v[0] || got[1] != v[1] || math.Abs(float64(got[0])-0.6) > 1e-6 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := decodeEmbedding([]byte{1, 2, 3}, 3); err == nil {
		t.Error("short blob accepted")
	}
	if c := centroid([][]float32{{1, 0}, {0, 1}}); math.Abs(dot(c, c)-1) > 1e-6 {
		t.Errorf("centroid not unit: %v", c)
	}
}

func TestDecodeWAVRejectsGarbage(t *testing.T) {
	if _, err := DecodeWAV([]byte("RIFFxxxxWAVE")); err == nil {
		t.Error("garbage accepted")
	}
	s, err := DecodeWAV(sineWAV(8000, 1, 300))
	if err != nil || len(s) != 16000 {
		t.Errorf("8 kHz → 16 kHz: %d %v", len(s), err)
	}
}

func TestCentiMs(t *testing.T) {
	for in, want := range map[float64]int{0: 0, 0.84: 840, 1.835: 1840, 2.2600000000000002: 2260} {
		if got := centiMs(in); got != want {
			t.Errorf("centiMs(%v) = %d, want %d", in, got, want)
		}
	}
}
