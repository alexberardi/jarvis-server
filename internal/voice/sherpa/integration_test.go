package sherpa

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// Integration tests load the real embedded libraries via purego and run real models.
// They need model files, so they skip unless JARVIS_SHERPA_MODELS points at a directory with:
//
//	3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx
//	kokoro-multi-lang-v1_0/
//	speakers/<speaker>/*.wav   (optional; enables the EER regression check)
//
// The voice spike's layout (spikes/voice-onnx) satisfies this with speakers -> spk/data30.
func modelsDir(t *testing.T) string {
	t.Helper()
	d := os.Getenv("JARVIS_SHERPA_MODELS")
	if d == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	// Not t.TempDir(): Windows keeps loaded DLLs locked, so per-test cleanup would fail.
	// Extraction is content-addressed, so a shared directory is reused safely across runs.
	if err := Load(filepath.Join(os.TempDir(), "jarvis-sherpa-test")); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return d
}

func readWAV(t *testing.T, path string) ([]float32, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, rate, err := audio.ReadWAV(f)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return s, rate
}

func TestKokoroGenerates(t *testing.T) {
	dir := modelsDir(t)
	tts, err := NewKokoro(KokoroConfig{Dir: filepath.Join(dir, "kokoro-multi-lang-v1_0"), Lexicon: "lexicon-gb-en.txt", Lang: "en", NumThreads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer tts.Close()
	if tts.SampleRate() != 24000 || tts.NumSpeakers() < 27 {
		t.Fatalf("rate=%d speakers=%d", tts.SampleRate(), tts.NumSpeakers())
	}
	const bmGeorge = 26
	samples, err := tts.Generate("At your service. How may I help?", bmGeorge, 1.25)
	if err != nil {
		t.Fatal(err)
	}
	secs := float64(len(samples)) / 24000
	if secs < 1.5 || secs > 3.5 { // spike measured 2.16s
		t.Fatalf("generated %.2fs of audio, want ~2.2s", secs)
	}
	if out := os.Getenv("JARVIS_SHERPA_TTS_OUT"); out != "" {
		f, _ := os.Create(out)
		defer f.Close()
		audio.WriteWAV(f, samples, 24000)
	}
}

func TestSpeakerEERMatchesSpike(t *testing.T) {
	dir := modelsDir(t)
	data := filepath.Join(dir, "speakers")
	if _, err := os.Stat(data); err != nil {
		t.Skip("no speakers/ dataset")
	}
	ex, err := NewSpeakerExtractor(SpeakerConfig{Model: filepath.Join(dir, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"), NumThreads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()

	const nEnroll = 3
	spks, _ := os.ReadDir(data)
	cents := map[string][]float32{}
	type trial struct {
		spk string
		v   []float32
	}
	var tests []trial
	for _, sd := range spks {
		files, _ := filepath.Glob(filepath.Join(data, sd.Name(), "*.wav"))
		sort.Strings(files)
		var enroll [][]float32
		for i, f := range files {
			s, rate := readWAV(t, f)
			v, err := ex.Embed(s, rate)
			if err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			v = unit(v)
			if i < nEnroll {
				enroll = append(enroll, v)
			} else {
				tests = append(tests, trial{sd.Name(), v})
			}
		}
		cents[sd.Name()] = centroid(enroll)
	}
	var genuine, impostor []float64
	for _, tr := range tests {
		for name, c := range cents {
			if s := dot(c, tr.v); name == tr.spk {
				genuine = append(genuine, s)
			} else {
				impostor = append(impostor, s)
			}
		}
	}
	eer := equalErrorRate(genuine, impostor)
	t.Logf("speakers=%d trials=%d EER=%.2f%%", len(cents), len(tests), eer*100)
	if eer > 0.02 { // spike (cgo build): 1.25% on spk/data30
		t.Fatalf("EER %.2f%% regressed vs spike's 1.25%%", eer*100)
	}
}

func unit(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	n = math.Sqrt(n)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / n)
	}
	return out
}

func centroid(vs [][]float32) []float32 {
	c := make([]float32, len(vs[0]))
	for _, v := range vs {
		for i, x := range v {
			c[i] += x
		}
	}
	return unit(c)
}

func dot(a, b []float32) float64 {
	var d float64
	for i := range a {
		d += float64(a[i]) * float64(b[i])
	}
	return d
}

func equalErrorRate(genuine, impostor []float64) float64 {
	best, gap := 1.0, 2.0
	for thr := -0.2; thr <= 1.0; thr += 0.001 {
		var fr, fa float64
		for _, g := range genuine {
			if g < thr {
				fr++
			}
		}
		for _, x := range impostor {
			if x >= thr {
				fa++
			}
		}
		fr /= float64(len(genuine))
		fa /= float64(len(impostor))
		if d := math.Abs(fr - fa); d < gap {
			best, gap = (fr+fa)/2, d
		}
	}
	return best
}

// TestSmokeTTSToSpeaker is the per-OS CI check: no external audio needed. Kokoro renders two
// phrases in each of two voices; speaker ID must score same-voice pairs above cross-voice pairs.
func TestSmokeTTSToSpeaker(t *testing.T) {
	dir := modelsDir(t)
	tts, err := NewKokoro(KokoroConfig{Dir: filepath.Join(dir, "kokoro-multi-lang-v1_0"), Lexicon: "lexicon-gb-en.txt", Lang: "en", NumThreads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer tts.Close()
	ex, err := NewSpeakerExtractor(SpeakerConfig{Model: filepath.Join(dir, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"), NumThreads: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()

	embed := func(text string, sid int) []float32 {
		s, err := tts.Generate(text, sid, 1.0)
		if err != nil {
			t.Fatal(err)
		}
		v, err := ex.Embed(s, tts.SampleRate()) // 24 kHz in; sherpa resamples to the model's 16 kHz
		if err != nil {
			t.Fatal(err)
		}
		return unit(v)
	}
	const george, emma = 26, 21
	g1, g2 := embed("Turn on the kitchen lights, please.", george), embed("What is the weather like tomorrow?", george)
	e1 := embed("Turn on the kitchen lights, please.", emma)
	same, cross := dot(g1, g2), dot(g1, e1)
	t.Logf("same-voice=%.3f cross-voice=%.3f", same, cross)
	if same <= cross+0.1 {
		t.Fatalf("speaker ID cannot separate voices: same=%.3f cross=%.3f", same, cross)
	}
}
