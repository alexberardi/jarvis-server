package voice

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// heardChime is the wake chime as the microphone hears it (48 kHz, at amp), padded with the
// room to whole chunks.
func heardChime(amp float64) [][]int16 {
	var pcm []int16
	for _, t := range []tone{{660, 90}, {880, 120}} {
		for i := range CaptureRate * t.ms / 1000 {
			pcm = append(pcm, int16(amp*math.Sin(2*math.Pi*t.hz*float64(i)/CaptureRate)))
		}
	}
	for len(pcm)%ChunkSamples != 0 {
		pcm = append(pcm, noise[len(pcm)%ChunkSamples])
	}
	var out [][]int16
	for c := range slices.Chunk(pcm, ChunkSamples) {
		out = append(out, c)
	}
	return out
}

// voiced is a vowel-like 220 Hz voice: ten harmonics, the third right on the chime's 660 Hz.
func voiced(n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		v := 0.0
		for k := 1; k <= 10; k++ {
			v += 3000 / float64(k) * math.Sin(2*math.Pi*220*float64(k)*float64(i)/CaptureRate)
		}
		out[i] = int16(v)
	}
	return out
}

// maxChimeShare is the largest share of the chime's tones in any window of pcm.
func maxChimeShare(pcm []int16) float64 {
	worst := 0.0
	for w := range slices.Chunk(pcm, chimeWindow) {
		if len(w) == chimeWindow {
			worst = max(worst, chimeShare(w))
		}
	}
	return worst
}

func TestMuteChimeSilencesTheChimeAndKeepsSpeech(t *testing.T) {
	chime := slices.Concat(heardChime(8000)...)
	if s := maxChimeShare(chime); s < 0.9 {
		t.Fatalf("the chime's own share is %.2f; the detector does not see it", s)
	}
	voice := voiced(CaptureRate / 2)
	take := slices.Concat(rep(noise, 2)[0], chime, voice)
	muteChime(take)
	if s := maxChimeShare(take); s > 0.2 {
		t.Errorf("chime share %.2f left in the take", s)
	}
	if !slices.Equal(take[len(take)-len(voice):], voice) {
		t.Error("the speech after the chime was changed")
	}
	if !slices.Equal(take[:ChunkSamples], noise) {
		t.Error("the room before the chime was changed")
	}
}

// The take of #358: the chime heard while it played sits in the pre-roll (#229) and whisper
// wrote it as "(beep)". The upload must hold the command without the chime, and the command
// must reach the server without the annotation.
func TestWakeChimeIsNotInTheCommand(t *testing.T) {
	// 3.2 s of room, "hey jarvis", the chime as the mic heard it, then 13 chunks of command:
	// the first 5 while the speaker drains.
	chime := heardChime(8000)
	mic := script(rep(noise, 40), rep(wake, 3), chime, rep(speech, 13))
	mic.pace = 5 * time.Millisecond
	srv := newServer()
	srv.text = "(beep)  Open Google Chrome."
	r := newRig(t, mic, srv)
	r.p.lockstep = false
	r.p.Cfg.Endpointing.PreRollSecs = 1
	open, first := r.p.Playback, true
	r.p.Playback = func(rate, ch int) (device.Playback, error) {
		pb, err := open(rate, ch)
		if first {
			first = false
			pb = slowDrain{pb, func() {
				for mic.readCount() < 43+len(chime)+5 {
					time.Sleep(time.Millisecond)
				}
			}}
		}
		return pb, err
	}
	stop := r.run(t)
	tr := r.turn(t)
	_ = stop()
	cmd, _ := decodeWAV(t, r.srv.cmdWAV)
	if s := maxChimeShare(cmd); s > 0.2 {
		t.Errorf("the command upload holds the chime (share %.2f, pre-roll %.2f s)", s, tr.PreRollSecs)
	}
	loud := 0
	for i := 0; i+ChunkSamples <= len(cmd); i += ChunkSamples {
		if RMS(cmd[i:i+ChunkSamples]) > 1000 {
			loud++
		}
	}
	if loud < 13 || loud > 16 {
		t.Errorf("%d chunks of speech in the command, want the 13 of the command (+ the wake word's tail)", loud)
	}
	if got := r.srv.streamIn.VoiceCommand; got != "Open Google Chrome." {
		t.Errorf("server got %q, want the command without the (beep)", got)
	}
}
