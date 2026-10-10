package audio

import (
	"math"
	"testing"
)

// Chunked processing must equal one-shot processing (no per-chunk edge effects), and the
// filter must pass DC at unity and reject a tone above the new Nyquist.
func TestDecimatorStreamingAndResponse(t *testing.T) {
	in := make([]int16, 48000)
	for i := range in {
		in[i] = int16(8000*math.Sin(2*math.Pi*440*float64(i)/48000) + 6000*math.Sin(2*math.Pi*12000*float64(i)/48000) + 1000)
	}
	whole := NewDecimator(3).Process(in, nil)
	d := NewDecimator(3)
	var parts []int16
	for i := 0; i < len(in); i += 3840 + 7 { // odd chunk sizes on purpose
		parts = d.Process(in[i:min(len(in), i+3840+7)], parts)
	}
	if len(whole) != len(parts) {
		t.Fatalf("len %d vs %d", len(whole), len(parts))
	}
	for i := range whole {
		if whole[i] != parts[i] {
			t.Fatalf("sample %d differs: %d vs %d", i, whole[i], parts[i])
		}
	}
	// Output is 16 kHz: expect the 440 Hz tone + DC, with the 12 kHz tone gone.
	var mean, worst float64
	out := whole[100:]
	for _, s := range out {
		mean += float64(s)
	}
	mean /= float64(len(out))
	for i, s := range out {
		want := 8000*math.Sin(2*math.Pi*440*float64(i+100)*3/48000-2*math.Pi*440*30/48000) + 1000
		worst = max(worst, math.Abs(float64(s)-want))
	}
	if math.Abs(mean-1000) > 20 {
		t.Errorf("DC gain off: mean %.1f", mean)
	}
	if worst > 150 {
		t.Errorf("12 kHz not rejected / 440 Hz distorted: worst error %.1f", worst)
	}
}
