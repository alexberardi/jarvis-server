package audio

import "math"

// Decimator is a streaming integer decimator matching scipy.signal.resample_poly(x, 1, down)'s
// filter (firwin(2*10*down+1, 1/down, window=('kaiser', 5.0)), unity DC gain), but stateful
// across chunks, so there are no per-chunk edge artifacts (the Python node resamples each
// 80 ms chunk independently with zero padding).
type Decimator struct {
	down  int
	taps  []float32
	hist  []float32 // last len(taps)-1 input samples
	phase int
}

// NewDecimator returns a decimator by the integer factor down (3 for 48 kHz to 16 kHz).
func NewDecimator(down int) *Decimator {
	half := 10 * down
	n := 2*half + 1
	taps := make([]float64, n)
	sum := 0.0
	beta := 5.0
	for i := range taps {
		t := float64(i - half)
		s := 1.0
		if t != 0 {
			x := math.Pi * t / float64(down)
			s = math.Sin(x) / x
		}
		r := 2*float64(i)/float64(n-1) - 1
		w := bessI0(beta*math.Sqrt(1-r*r)) / bessI0(beta)
		taps[i] = s * w
		sum += taps[i]
	}
	d := &Decimator{down: down, taps: make([]float32, n), hist: make([]float32, n-1)}
	for i := range taps {
		d.taps[i] = float32(taps[i] / sum)
	}
	return d
}

func bessI0(x float64) float64 {
	s, t := 1.0, 1.0
	for k := 1; k < 50; k++ {
		t *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		s += t
	}
	return s
}

// Process consumes in and appends the decimated output to out.
func (d *Decimator) Process(in []int16, out []int16) []int16 {
	n := len(d.taps)
	buf := make([]float32, len(d.hist)+len(in))
	copy(buf, d.hist)
	for i, s := range in {
		buf[len(d.hist)+i] = float32(s)
	}
	for i := d.phase; i+n <= len(buf); i += d.down {
		var acc float32
		w := buf[i : i+n]
		for k, t := range d.taps {
			acc += w[k] * t
		}
		acc = float32(math.Round(float64(acc)))
		out = append(out, int16(max(-32768, min(32767, acc))))
		d.phase = i + d.down
	}
	d.phase -= len(buf) - len(d.hist)
	copy(d.hist, buf[len(buf)-len(d.hist):])
	return out
}
