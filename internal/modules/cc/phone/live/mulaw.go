// Package live holds the pure, I/O-free building blocks of the absorbed phone gateway
// (docs/cc/11-phone.md, D16): the G.711 codec, endpointing, the text-token tool protocol, the
// think stripper, the spoken-output guard, the call-brain prompt, the Twilio wire format and
// signature checks, the recorder and the escalation window. Ported from jarvis-phone-gateway;
// the prompt and guard strings are byte-for-byte (they were tuned on live calls).
package live

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/bits"
)

const (
	mulawBias = 0x84
	mulawClip = 32635
)

// MulawDecode decodes G.711 mu-law bytes to int16 PCM (audio/mulaw.py ulaw_decode).
func MulawDecode(b []byte) []int16 {
	out := make([]int16, len(b))
	for i, v := range b {
		u := ^v
		sign := u & 0x80
		exponent := (u >> 4) & 0x07
		mantissa := int32(u & 0x0F)
		mag := (((mantissa << 3) + mulawBias) << exponent) - mulawBias
		if sign != 0 {
			mag = -mag
		}
		out[i] = int16(mag)
	}
	return out
}

// MulawEncode encodes int16 PCM to G.711 mu-law bytes (audio/mulaw.py ulaw_encode).
func MulawEncode(pcm []int16) []byte {
	out := make([]byte, len(pcm))
	for i, s := range pcm {
		x := int32(s)
		var sign int32
		if x < 0 {
			sign = 0x80
			x = -x
		}
		if x > mulawClip {
			x = mulawClip
		}
		x += mulawBias
		exponent := int32(bits.Len32(uint32(x))-1) - 7 // floor(log2(x)) - 7
		if exponent < 0 {
			exponent = 0
		}
		if exponent > 7 {
			exponent = 7
		}
		mantissa := (x >> (exponent + 3)) & 0x0F
		out[i] = byte(^(sign | (exponent << 4) | mantissa) & 0xFF)
	}
	return out
}

// RMS is the root-mean-square level of a frame (0 for an empty one).
func RMS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, s := range pcm {
		f := float64(s)
		sum += f * f
	}
	return math.Sqrt(sum / float64(len(pcm)))
}

// resampleHalfWidth is the windowed-sinc kernel's half width in output-band zero crossings.
const resampleHalfWidth = 16

// Resample converts int16 PCM between sample rates with a Hann-windowed sinc low-pass
// (cutoff at the lower Nyquist), clip-safe. The output length matches scipy's resample_poly
// (ceil(len*dst/src) after reducing the ratio); exact sample parity with scipy is not a goal.
// The same rate returns pcm unchanged.
func Resample(pcm []int16, src, dst int) []int16 {
	if src == dst || src <= 0 || dst <= 0 {
		return pcm
	}
	g := gcd(src, dst)
	up, down := dst/g, src/g
	n := (len(pcm)*up + down - 1) / down
	out := make([]int16, n)
	fc := 1.0
	if dst < src {
		fc = float64(dst) / float64(src) // cutoff relative to the input Nyquist
	}
	half := float64(resampleHalfWidth) / fc // kernel half width in input samples
	step := float64(src) / float64(dst)
	for k := range out {
		t := float64(k) * step
		lo := int(math.Ceil(t - half))
		hi := int(math.Floor(t + half))
		if lo < 0 {
			lo = 0
		}
		if hi > len(pcm)-1 {
			hi = len(pcm) - 1
		}
		var acc, wsum float64
		for i := lo; i <= hi; i++ {
			d := t - float64(i)
			w := 0.5 + 0.5*math.Cos(math.Pi*d/half) // Hann window
			h := fc * sinc(fc*d) * w
			acc += float64(pcm[i]) * h
			wsum += h
		}
		// Normalise by the kernel's DC gain so edges (truncated kernels) keep their level.
		if wsum != 0 {
			acc /= wsum
		}
		out[k] = clip16(math.Round(acc))
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func clip16(v float64) int16 {
	if v > math.MaxInt16 {
		return math.MaxInt16
	}
	if v < math.MinInt16 {
		return math.MinInt16
	}
	return int16(v)
}

// WAV wraps mono int16 PCM in a RIFF/WAVE container (16-bit linear, never mu-law: whisper
// rejects a mu-law WAV).
func WAV(pcm []int16, rate int) []byte {
	data := len(pcm) * 2
	var b bytes.Buffer
	b.Grow(44 + data)
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + data))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(rate))
	w(uint32(rate * 2))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(data))
	w(pcm)
	return b.Bytes()
}

// PCMBytesToInt16 reads little-endian int16 samples; a trailing odd byte is ignored.
func PCMBytesToInt16(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return out
}
