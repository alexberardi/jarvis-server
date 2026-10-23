package audio

// Resampler converts a mono s16 stream from one rate to another, chunk by chunk, with no
// per-chunk edges. An integer step down (48 kHz to 24 kHz) uses the Decimator's anti-alias
// filter; any other ratio (22050 or 16000 up to 24000, 44100 down) interpolates linearly,
// which is enough for speech from a TTS provider that won't send the rate we ask for.
type Resampler struct {
	from, to int
	dec      *Decimator
	pos      float64 // position of the next output sample, in input samples since prev
	prev     int16   // the last input sample of the previous chunk
	started  bool
}

// NewResampler returns a resampler from rate from to rate to (both > 0).
func NewResampler(from, to int) *Resampler {
	r := &Resampler{from: from, to: to}
	if from > to && from%to == 0 {
		r.dec = NewDecimator(from / to)
	}
	return r
}

// Process consumes in and appends the resampled output to out.
func (r *Resampler) Process(in []int16, out []int16) []int16 {
	switch {
	case r.from == r.to:
		return append(out, in...)
	case r.dec != nil:
		return r.dec.Process(in, out)
	}
	if len(in) == 0 {
		return out
	}
	if !r.started {
		r.prev, r.started = in[0], true
	}
	step := float64(r.from) / float64(r.to)
	// Sample k of this chunk sits at position k+1 (prev is at 0).
	at := func(k int) float64 {
		if k < 0 {
			return float64(r.prev)
		}
		return float64(in[k])
	}
	for r.pos <= float64(len(in)) {
		i := int(r.pos) // between sample i-1 and i (prev when i == 0)
		f := r.pos - float64(i)
		v := at(i - 1)
		if f > 0 {
			v += f * (at(i) - v)
		}
		out = append(out, int16(max(-32768, min(32767, v))))
		r.pos += step
	}
	r.pos -= float64(len(in))
	r.prev = in[len(in)-1]
	return out
}
