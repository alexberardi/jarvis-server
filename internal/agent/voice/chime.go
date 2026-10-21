package voice

import "math"

// Chimes are synthesised, so no audio assets ship with the agent. The wake chime stands in
// for the Python node's greeting (go-pi-node.md §1.1 row 6: "a bundled WAV is enough"); the
// error chime for its spoken error phrases (row 15: "chime only").

const chimeRate = 24000

type tone struct {
	hz float64
	ms int
}

func synth(tones ...tone) []int16 {
	const amp = 0.25 * 32767
	const fadeMS = 8
	var out []int16
	for _, t := range tones {
		n := chimeRate * t.ms / 1000
		fade := chimeRate * fadeMS / 1000
		for i := range n {
			g := 1.0
			if i < fade {
				g = float64(i) / float64(fade)
			} else if n-i < fade {
				g = float64(n-i) / float64(fade)
			}
			out = append(out, int16(amp*g*math.Sin(2*math.Pi*t.hz*float64(i)/chimeRate)))
		}
	}
	return out
}

var (
	wakeChime  = synth(tone{660, 90}, tone{880, 120})
	errorChime = synth(tone{440, 140}, tone{330, 220})
)

// chimeWindow is muteChime's resolution: 20 ms at 48 kHz, fine enough to cut the chime and
// little else, wide enough (50 Hz bins) to tell its tones from a voice's harmonics.
const chimeWindow = CaptureRate / 50

// chimeShareMax is the share of a window's energy in the wake chime's tones above which the
// window is the chime. A pure tone reads 1; a voice, whose energy spreads over its
// harmonics, well under 0.2 even with one of them on 660 or 880 Hz.
const chimeShareMax = 0.4

// chimeShare is the share of pcm's energy at the wake chime's two tones (Goertzel).
func chimeShare(pcm []int16) float64 {
	energy := 0.0
	for _, v := range pcm {
		energy += float64(v) * float64(v)
	}
	if energy == 0 {
		return 0
	}
	n := float64(len(pcm))
	tones := 0.0
	for _, hz := range []float64{660, 880} {
		c := 2 * math.Cos(2*math.Pi*hz/CaptureRate)
		var s1, s2 float64
		for _, v := range pcm {
			s1, s2 = float64(v)+c*s1-s2, s1
		}
		tones += s1*s1 + s2*s2 - c*s1*s2
	}
	return 2 * tones / (n * energy)
}

// muteChime zeroes the windows of pcm (48 kHz) where the wake chime is what the microphone
// heard: whisper writes it as "(beep)" in front of the command (#358). Speech under the chime
// is lost with it, as it would be anyway; speech around it is kept.
func muteChime(pcm []int16) {
	for i := 0; i < len(pcm); i += chimeWindow {
		w := pcm[i:min(i+chimeWindow, len(pcm))]
		if chimeShare(w) > chimeShareMax {
			clear(w)
		}
	}
}
