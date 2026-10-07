package live

import "sync"

// RecordingRate is the recording's sample rate (the phone leg's).
const RecordingRate = 8000

// Recorder mixes both directions of a call into one 8 kHz track (services/recording.py). The
// inbound stream is the wall clock (Twilio sends continuous frames, silence included), so
// agent audio is mixed in at the inbound position where playback started. Safe for
// concurrent use.
type Recorder struct {
	mu       sync.Mutex
	inbound  []int16
	outbound []outBurst
}

type outBurst struct {
	start int
	pcm   []int16
}

// AddInbound appends callee audio.
func (r *Recorder) AddInbound(pcm []int16) {
	r.mu.Lock()
	r.inbound = append(r.inbound, pcm...)
	r.mu.Unlock()
}

// AddOutbound anchors agent audio at the current inbound position.
func (r *Recorder) AddOutbound(pcm []int16) {
	r.mu.Lock()
	r.outbound = append(r.outbound, outBurst{start: len(r.inbound), pcm: append([]int16(nil), pcm...)})
	r.mu.Unlock()
}

// Mix sums both directions with an int32 accumulator and clips.
func (r *Recorder) Mix() []int16 {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := len(r.inbound)
	for _, b := range r.outbound {
		total = max(total, b.start+len(b.pcm))
	}
	acc := make([]int32, total)
	for i, s := range r.inbound {
		acc[i] += int32(s)
	}
	for _, b := range r.outbound {
		for i, s := range b.pcm {
			acc[b.start+i] += int32(s)
		}
	}
	out := make([]int16, total)
	for i, v := range acc {
		out[i] = clip16(float64(v))
	}
	return out
}

// WAV is the mix as an 8 kHz mono PCM16 WAV.
func (r *Recorder) WAV() []byte { return WAV(r.Mix(), RecordingRate) }
