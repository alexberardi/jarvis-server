package audio

import (
	"math"
	"testing"
)

func sine(rate, n int, hz float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(10000 * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

// feed runs in through r in uneven chunks, as a network body arrives.
func feed(r *Resampler, in []int16) []int16 {
	var out []int16
	for i, step := 0, 1; i < len(in); step = step%7 + 3 {
		j := min(len(in), i+step*97)
		out = r.Process(in[i:j], out)
		i = j
	}
	return out
}

func TestResamplerPassthrough(t *testing.T) {
	in := sine(24000, 1000, 440)
	out := feed(NewResampler(24000, 24000), in)
	if len(out) != len(in) {
		t.Fatalf("len %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("sample %d changed", i)
		}
	}
}

// TestResamplerRates checks the length (rate ratio) and that a 440 Hz tone is still a 440 Hz
// tone at 24 kHz, for an integer decimation (48 kHz) and fractional rates up and down.
func TestResamplerRates(t *testing.T) {
	for _, from := range []int{48000, 22050, 16000, 44100} {
		in := sine(from, from/2, 440) // 0.5 s
		out := feed(NewResampler(from, 24000), in)
		if want := 12000; math.Abs(float64(len(out)-want)) > 40 {
			t.Fatalf("%d Hz: %d samples, want ~%d", from, len(out), want)
		}
		// Correlate with a 440 Hz reference at 24 kHz, past the filter's warm-up.
		var dot, norm float64
		for i := 500; i < len(out)-500; i++ {
			ph := 2 * math.Pi * 440 * float64(i) / 24000
			dot += float64(out[i]) * math.Sin(ph)
			norm += float64(out[i]) * math.Cos(ph)
		}
		amp := 2 * math.Hypot(dot, norm) / float64(len(out)-1000)
		if amp < 8500 || amp > 11000 {
			t.Fatalf("%d Hz: 440 Hz amplitude %.0f, want ~10000", from, amp)
		}
	}
}
