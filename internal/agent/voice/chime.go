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
