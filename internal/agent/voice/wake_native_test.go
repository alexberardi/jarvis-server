//go:build linux || darwin

package voice

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// The whole listening path with the real models: a 16 kHz clip played as a 48 kHz microphone
// (WAVCapture's resampling), decimated back, scored by openWakeWord, and a turn run against
// the fake server. Needs the models from scripts/fetch-test-models.sh.
func TestRealWakeWordStartsATurn(t *testing.T) {
	root := os.Getenv("JARVIS_SHERPA_MODELS")
	if root == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	models := filepath.Join(root, "openwakeword")
	if err := CheckWakeModels(models); err != nil {
		t.Skip(err)
	}
	det, closeDet, err := LoadDetector(filepath.Join(os.TempDir(), "jarvis-sherpa-test"), models)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDet()

	clip := func(name string) [][]int16 {
		c, err := device.OpenWAVCapture(filepath.Join("..", "..", "voice", "oww", "testdata", name+".wav"), CaptureRate, 1, false)
		if err != nil {
			t.Fatal(err)
		}
		var out [][]int16
		for !c.Done() {
			b := make([]int16, ChunkSamples)
			_ = c.Read(b)
			out = append(out, b)
		}
		return out
	}
	quiet := make([]int16, ChunkSamples)
	for _, tc := range []struct {
		clip string
		fire bool
	}{{"hey_jarvis_george", true}, {"tell_me_a_joke_george", false}} {
		t.Run(tc.clip, func(t *testing.T) {
			det.Reset()
			// 2 s of silence first: oww needs 16 embeddings before it scores.
			mic := script(rep(quiet, 25), clip(tc.clip), rep(quiet, 5), rep(speech, 6))
			mic.pace = 5 * time.Millisecond
			n := len(mic.chunks)
			r := newRig(t, mic, newServer())
			r.p.Detector = det
			stop := r.run(t)
			if tc.fire {
				tr := r.turn(t)
				if tr.WakeScore <= 0.4 || tr.Outcome != OutcomeSpoken {
					t.Fatalf("turn %+v", tr)
				}
				t.Logf("fired at score %.3f", tr.WakeScore)
			} else {
				r.noTurn(t, time.Duration(n+20)*mic.pace+time.Second)
			}
			if err := stop(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
