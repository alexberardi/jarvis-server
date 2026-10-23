package oww

import (
	"math"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/voice/ort"
)

// synthMusic is a deterministic stand-in for the node's own music: a four-chord loop (each
// chord's notes with their first harmonics and a pluck envelope, half a second per chord) over a
// bass note and a kick on every beat. Unit RMS, so the caller sets its level.
func synthMusic(n int) []float64 {
	chords := [][]float64{
		{220.00, 261.63, 329.63}, // Am
		{174.61, 220.00, 261.63}, // F
		{261.63, 329.63, 392.00}, // C
		{196.00, 246.94, 293.66}, // G
	}
	const beat = SampleRate / 2
	out := make([]float64, n)
	var sum float64
	for i := range out {
		t := float64(i) / SampleRate
		c := chords[(i/beat)%len(chords)]
		k := float64(i%beat) / SampleRate // seconds since the beat
		env := math.Exp(-3 * k)
		var v float64
		for _, f := range c {
			for h := 1.0; h <= 3; h++ {
				v += env / h * math.Sin(2*math.Pi*f*h*t)
			}
		}
		v += 1.5 * math.Sin(2*math.Pi*c[0]/2*t)                                  // bass
		v += 3 * math.Exp(-40*k) * math.Sin(2*math.Pi*(60+90*math.Exp(-30*k))*k) // kick
		out[i] = v
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(n))
	for i := range out {
		out[i] /= rms
	}
	return out
}

func rms(x []int16) float64 {
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(x)))
}

// mixMusic lays the music under the speech clip, snrDB below the speech's RMS, from 1.5 s before
// the clip (so the detector has heard only music when the phrase starts) to its end.
func mixMusic(speech []int16, ref, snrDB float64) []int16 {
	lead := SampleRate * 3 / 2
	music := synthMusic(lead + len(speech))
	gain := ref * math.Pow(10, -snrDB/20)
	out := make([]int16, len(music))
	for i, m := range music {
		v := gain * m
		if j := i - lead; j >= 0 {
			v += float64(speech[j])
		}
		out[i] = int16(max(-32768, min(32767, math.Round(v))))
	}
	return out
}

func peak(s []float32) float32 { return s[argmax(s)] }

// TestWakeOverLowMusicClearsTheTentativeFireBar is the offline stand-in for the Pi capture of
// #503 ("hey Jarvis" over the node's own music at low volume, through the echo canceller): the
// wake clip with synthetic music under it, from as loud as the speech to 10 dB under it (the
// node's own music at low volume, after the duck and the canceller), still peaks above the 0.35
// the pipeline fires at inside a tentative wake, and the music alone stays under the 0.20 that
// opens one. Synthetic music masks the phrase far less than the Pi's real capture did (0.33-0.51),
// so this checks the model side only; what the canceller does to the phrase needs the jarvis_ec
// capture.
func TestWakeOverLowMusicClearsTheTentativeFireBar(t *testing.T) {
	// voice.DefaultTentativeConfig()'s FireThreshold and Threshold.
	const fireBar, tentativeBar = 0.35, 0.20
	d := detector(t, ort.Options{})
	speech := readClip(t, "hey_jarvis_emma")
	ref := rms(speech)
	for _, snr := range []float64{10, 5, 0} {
		d.Reset()
		got := peak(scoreClip(t, d, mixMusic(speech, ref, snr)))
		t.Logf("music %2.0f dB under the speech: peak %.4f", snr, got)
		if got <= fireBar {
			t.Errorf("music %.0f dB under the speech: peak %.4f, want above %.2f", snr, got, fireBar)
		}
	}
	d.Reset()
	alone := peak(scoreClip(t, d, mixMusic(make([]int16, len(speech)), ref, 0)))
	t.Logf("music alone: peak %.4f", alone)
	if alone >= tentativeBar {
		t.Errorf("music alone peaks at %.4f, want under %.2f", alone, tentativeBar)
	}
}
