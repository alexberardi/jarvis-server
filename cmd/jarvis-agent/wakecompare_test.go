package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio"
)

// scriptDet scores stream n's chunk i as scores[n][i] (0 past the script). Reset starts the
// next stream, as wakeCompare must between sources.
type scriptDet struct {
	scores [][]float32
	stream int
	chunk  int
}

func (d *scriptDet) Reset() { d.stream++; d.chunk = 0 }

func (d *scriptDet) Process(chunk []int16) (float32, error) {
	if len(chunk) != compareChunk {
		return 0, fmt.Errorf("chunk of %d samples", len(chunk))
	}
	if d.stream == 0 {
		return 0, errors.New("Process before the first Reset")
	}
	d.chunk++
	s := d.scores[d.stream-1]
	if d.chunk > len(s) {
		return 0, nil
	}
	return s[d.chunk-1], nil
}

func TestWakeCompareScoresEachSourceFresh(t *testing.T) {
	det := &scriptDet{scores: [][]float32{{0, 0.5, 0.9, 0.2}, {0, 0.1, 0.4, 0}}}
	var out strings.Builder
	c := &wakeCompare{det: det, out: &out, floor: 0.3,
		record: func(_ context.Context, source string, n int) ([]int16, error) {
			if n != 4*compareChunk {
				t.Fatalf("%s: asked for %d samples", source, n)
			}
			// A trailing part-chunk: dropped, not scored.
			return make([]int16, n+100), nil
		}}
	if err := c.run(context.Background(), []string{"jarvis_mic_raw", "jarvis_ec_source"}, 4*0.08); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"jarvis_mic_raw", "max score 0.900 at 0.24 s", "frames >= 0.30: 0.16s 0.500  0.24s 0.900",
		"jarvis_ec_source", "max score 0.400 at 0.24 s", "frames >= 0.30: 0.24s 0.400",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if det.stream != 2 {
		t.Fatalf("detector reset %d times; want one fresh start per source", det.stream)
	}
}

func TestWakeCompareReportsARecordFailure(t *testing.T) {
	det := &scriptDet{scores: [][]float32{{0.6}}}
	var out strings.Builder
	c := &wakeCompare{det: det, out: &out, floor: 0.3,
		record: func(_ context.Context, source string, n int) ([]int16, error) {
			if source == "jarvis_ec_source" {
				return nil, errors.New("parec: No such entity")
			}
			return make([]int16, compareChunk), nil
		}}
	err := c.run(context.Background(), []string{"jarvis_mic_raw", "jarvis_ec_source"}, 0.08)
	if err == nil || !strings.Contains(err.Error(), "jarvis_ec_source not recorded") {
		t.Fatalf("err %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "jarvis_ec_source: parec: No such entity") ||
		!strings.Contains(got, "jarvis_mic_raw") || !strings.Contains(got, "max score 0.600") {
		t.Fatalf("output:\n%s", got)
	}
}

// The scoring path end to end on the real models: the fixture clip scores as a wake on both
// "sources" (the recording side stays faked; the real one needs a Pi, go-pi-node.md §3.16).
func TestWakeCompareScoresTheFixtureClip(t *testing.T) {
	if !voice.Supported {
		t.Skipf("voice not supported on this OS")
	}
	root := os.Getenv("JARVIS_SHERPA_MODELS")
	if root == "" {
		t.Skip("JARVIS_SHERPA_MODELS not set")
	}
	models := filepath.Join(root, "openwakeword")
	if err := voice.CheckWakeModels(models); err != nil {
		t.Skipf("no openWakeWord models: %v (run scripts/fetch-test-models.sh)", err)
	}
	f, err := os.Open(filepath.Join("..", "..", "internal", "voice", "oww", "testdata", "hey_jarvis_emma.wav"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fs, rate, err := audio.ReadWAV(f)
	if err != nil || rate != compareRate {
		t.Fatalf("fixture: rate %d, %v", rate, err)
	}
	clip := make([]int16, len(fs))
	for i, v := range fs {
		clip[i] = int16(v * 32768)
	}
	// Not t.TempDir(): Windows keeps loaded DLLs locked (as in the oww tests).
	det, closeDet, err := voice.LoadDetector(filepath.Join(os.TempDir(), "jarvis-sherpa-test"), models)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDet()
	var out strings.Builder
	c := &wakeCompare{det: det, out: &out, floor: 0.5,
		record: func(_ context.Context, source string, n int) ([]int16, error) { return clip, nil }}
	if err := c.run(context.Background(), []string{"jarvis_mic_raw", "jarvis_ec_source"}, 1); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "frames >= 0.50") != 2 {
		t.Fatalf("the clip doesn't score above 0.5 on both streams:\n%s", got)
	}
}
