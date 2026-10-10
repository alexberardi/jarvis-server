package oww

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio"
	"github.com/alexberardi/jarvis-server/internal/voice/ort"
)

// The golden test runs the real models on the embedded onnxruntime. It needs the openWakeWord
// models, which scripts/fetch-test-models.sh puts in $JARVIS_SHERPA_MODELS/openwakeword.
func detector(t *testing.T, o ort.Options) *Detector {
	t.Helper()
	root := os.Getenv("JARVIS_SHERPA_MODELS")
	if root == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	dir := filepath.Join(root, "openwakeword")
	if _, err := os.Stat(filepath.Join(dir, HeyJarvisModel)); err != nil {
		t.Skipf("no openWakeWord models in %s (run scripts/fetch-test-models.sh)", dir)
	}
	// Not t.TempDir(): Windows keeps loaded DLLs locked (as in the sherpa tests).
	rt, err := ort.LoadEmbedded(filepath.Join(os.TempDir(), "jarvis-sherpa-test"))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	d, err := New(rt, dir, HeyJarvisModel, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func readClip(t *testing.T, name string) []int16 {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, rate, err := audio.ReadWAV(f)
	if err != nil || rate != SampleRate {
		t.Fatalf("%s: rate %d, %v", name, rate, err)
	}
	out := make([]int16, len(s))
	for i, v := range s {
		out[i] = int16(v * 32768) // ReadWAV divides by 32768, so this is exact
	}
	return out
}

func scoreClip(t *testing.T, d *Detector, x []int16) []float32 {
	t.Helper()
	var scores []float32
	for i := 0; i+Chunk <= len(x); i += Chunk {
		s, err := d.Process(x[i : i+Chunk])
		if err != nil {
			t.Fatal(err)
		}
		scores = append(scores, s)
	}
	return scores
}

func argmax(s []float32) int {
	best := 0
	for i, v := range s {
		if v > s[best] {
			best = i
		}
	}
	return best
}

// Python oww seeds its feature buffer with embeddings of random noise, so its first frames are
// noise; compare from here on (go-pi-node.md §3.1).
const firstComparable = 21

// TestSmokeOWWMatchesPython: frame-for-frame parity with openWakeWord 0.6.0 on Kokoro clips,
// through the embedded onnxruntime on every CI OS. The spike measured 3e-8 on a Pi; the
// tolerance allows for MLAS kernel differences between CPUs and ORT builds.
func TestSmokeOWWMatchesPython(t *testing.T) {
	var golden struct {
		Clips map[string][]float32 `json:"clips"`
	}
	b, err := os.ReadFile("testdata/python_scores.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	d := detector(t, ort.Options{})
	lean := detector(t, ort.Options{NoArena: true})
	for _, c := range []struct {
		name string
		wake bool
	}{{"hey_jarvis_emma", true}, {"hey_jarvis_george", true}, {"tell_me_a_joke_george", false}} {
		t.Run(c.name, func(t *testing.T) {
			want := golden.Clips[c.name]
			d.Reset()
			got := scoreClip(t, d, readClip(t, c.name))
			if len(got) != len(want) {
				t.Fatalf("%d frames, Python has %d", len(got), len(want))
			}
			// 16 embeddings fill the window at frame 15, then 5 warmup predictions are zeroed.
			for i := range featWin - 1 + warmup {
				if got[i] != 0 {
					t.Errorf("frame %d = %v before the feature window filled, want 0", i, got[i])
				}
			}
			var worst float64
			for i := firstComparable; i < len(got); i++ {
				worst = max(worst, math.Abs(float64(got[i]-want[i])))
			}
			peak := argmax(got)
			t.Logf("peak %.4f @%d (Python %.4f @%d), max |diff| %.2g", got[peak], peak, want[argmax(want)], argmax(want), worst)
			if worst > 1e-4 {
				t.Errorf("max |diff| from Python = %g", worst)
			}
			if c.wake && (got[peak] < 0.9 || peak != argmax(want)) {
				t.Errorf("wake clip: peak %.4f @%d", got[peak], peak)
			}
			if !c.wake && got[peak] > 0.1 {
				t.Errorf("non-wake clip peaks at %.4f", got[peak])
			}

			// Reset really resets, and the arena setting doesn't change results.
			d.Reset()
			again := scoreClip(t, d, readClip(t, c.name))
			lean.Reset()
			noArena := scoreClip(t, lean, readClip(t, c.name))
			for i := range got {
				if again[i] != got[i] || math.Abs(float64(noArena[i]-got[i])) > 1e-6 {
					t.Fatalf("frame %d: %v, after Reset %v, no arena %v", i, got[i], again[i], noArena[i])
				}
			}
		})
	}
}

func TestProcessRejectsWrongChunkSize(t *testing.T) {
	d := &Detector{}
	if _, err := d.Process(make([]int16, Chunk-1)); err == nil {
		t.Fatal("a short chunk was accepted")
	}
}

// Regression: ort passed out-parameter addresses (&vals[i], &outs[0], &info) as uintptr
// through its own variadic helper, so the compiler could keep them on the goroutine stack,
// and a stack growth inside the call moved them: C wrote the OrtValue* into the old stack and
// Run failed with "NULL input supplied" (seen live, intermittently, on Linux and macOS).
// Fresh goroutines at every stack depth make the growth land inside the call.
func TestProcessAtAnyStackDepth(t *testing.T) {
	d := detector(t, ort.Options{})
	chunk := make([]int16, Chunk)
	for depth := range 300 {
		done := make(chan error, 1)
		go func() {
			done <- atDepth(depth, func() error { _, err := d.Process(chunk); return err })
		}()
		if err := <-done; err != nil {
			t.Fatalf("stack depth %d: %v", depth, err)
		}
	}
}

//go:noinline
func atDepth(n int, f func() error) error {
	var pad [48]byte
	if n == 0 {
		return f()
	}
	pad[n%len(pad)] = byte(n)
	err := atDepth(n-1, f)
	if pad[0] == 255 && err == nil { // keeps pad live across the call
		return nil
	}
	return err
}
