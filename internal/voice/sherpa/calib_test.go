//go:build calib

package sherpa

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestCalibrateSpeakerThreshold scores a real household speaker's commands against their own
// enrollment and against impostors, to pick voice.similarity_threshold for ERes2Net (D33).
//
//	JARVIS_SHERPA_MODELS=…  JARVIS_CALIB_DIR=…/calib  go test -tags calib -run Calibrate -v ./internal/voice/sherpa
//
// JARVIS_CALIB_DIR holds <speaker>/enroll/*.wav plus <speaker>/near and <speaker>/far test
// clips recorded on a node mic; impostors come from $JARVIS_SHERPA_MODELS/speakers (LibriSpeech)
// and from Kokoro voices saying the same commands.
func TestCalibrateSpeakerThreshold(t *testing.T) {
	dir := modelsDir(t)
	calib := os.Getenv("JARVIS_CALIB_DIR")
	if calib == "" {
		t.Skip("JARVIS_CALIB_DIR not set")
	}
	ex, err := NewSpeakerExtractor(SpeakerConfig{Model: filepath.Join(dir, "3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"), NumThreads: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	embedFile := func(p string) []float32 {
		s, rate := readWAV(t, p)
		v, err := ex.Embed(s, rate)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return unit(v)
	}
	glob := func(pattern string) []string {
		m, _ := filepath.Glob(pattern)
		sort.Strings(m)
		return m
	}

	speakers := glob(filepath.Join(calib, "*"))
	for _, spk := range speakers {
		name := filepath.Base(spk)
		var enroll [][]float32
		for _, f := range glob(filepath.Join(spk, "enroll", "*.wav")) {
			enroll = append(enroll, embedFile(f))
		}
		if len(enroll) == 0 {
			continue
		}
		c := centroid(enroll)
		var genuine []float64
		for _, sub := range []string{"near", "far"} {
			for _, f := range glob(filepath.Join(spk, sub, "*.wav")) {
				sc := dot(c, embedFile(f))
				genuine = append(genuine, sc)
				t.Logf("%s %s %s: %.3f", name, sub, filepath.Base(f), sc)
			}
		}

		// Impostors: every LibriSpeech clip, and Kokoro voices saying commands.
		var impostor []float64
		for _, f := range glob(filepath.Join(dir, "speakers", "*", "*.wav")) {
			impostor = append(impostor, dot(c, embedFile(f)))
		}
		tts, err := NewKokoro(KokoroConfig{Dir: filepath.Join(dir, "kokoro-multi-lang-v1_0"), Lexicon: "lexicon-gb-en.txt", Lang: "en", NumThreads: 4})
		if err != nil {
			t.Fatal(err)
		}
		defer tts.Close()
		var ttsImp []float64
		for sid := 0; sid < 28; sid++ {
			for _, txt := range []string{"What's the weather tomorrow?", "Turn off the kitchen lights.", "Set a timer for ten minutes."} {
				a, err := tts.Generate(txt, sid, 1.0)
				if err != nil {
					t.Fatal(err)
				}
				v, err := ex.Embed(a, tts.SampleRate())
				if err != nil {
					t.Fatal(err)
				}
				ttsImp = append(ttsImp, dot(c, unit(v)))
			}
		}
		impostor = append(impostor, ttsImp...)

		sort.Float64s(genuine)
		sort.Float64s(impostor)
		maxImp := impostor[len(impostor)-1]
		p999 := impostor[int(float64(len(impostor))*0.999)]
		t.Logf("%s: genuine n=%d min=%.3f median=%.3f | impostor n=%d max=%.3f p99.9=%.3f (tts max %.3f)",
			name, len(genuine), genuine[0], genuine[len(genuine)/2], len(impostor), maxImp, p999, maxF(ttsImp))
		t.Logf("%s: separation gap = %.3f; midpoint threshold = %.3f", name, genuine[0]-maxImp, (genuine[0]+maxImp)/2)
		for _, thr := range []float64{0.30, 0.35, 0.40, 0.45, 0.50, 0.55} {
			fr, fa := 0, 0
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
			t.Logf("  threshold %.2f: false rejects %d/%d, false accepts %d/%d", thr, fr, len(genuine), fa, len(impostor))
		}
	}
}

func maxF(xs []float64) float64 {
	m := -1.0
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}
