package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/music"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// wake-compare (#514): does the echo canceller attenuate the wake word? While the canceller is
// engaged (the node's music playing), record the raw Pulse source and the cancelled one at the
// same time, score both with the pipeline's own openWakeWord detector, and print each stream's
// peak. Run next to the running agent on the Pi; go-pi-node.md §3.16 has the procedure.

// The detector's input format (oww.SampleRate, oww.Chunk; not imported here so this file builds
// where onnxruntime isn't embedded, as the pipeline's wakeChunk).
const (
	compareRate  = 16000
	compareChunk = 1280
)

func wakeCompareCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("wake-compare", flag.ExitOnError)
	seconds := fs.Float64("seconds", 8, `record this long; say "hey Jarvis" once while it runs`)
	sources := fs.String("sources", music.MicSource+","+music.ECSource,
		"comma-separated Pulse sources to record side by side")
	models := fs.String("wake-models", "", "openWakeWord models directory (default: the agent's cache)")
	floor := fs.Float64("floor", 0.1, "print every frame scoring at least this")
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	if *seconds <= 0 {
		return fmt.Errorf("--seconds %v: must be positive", *seconds)
	}
	vs, err := loadVoice(ctx, &voiceFlags{models: *models}, logger())
	if err != nil {
		return fmt.Errorf("voice: %w", err)
	}
	defer vs.close()
	c := &wakeCompare{det: vs.det, record: parecRecord, out: os.Stdout, floor: *floor}
	fmt.Fprintf(c.out, "recording %s for %.1f s; say \"hey Jarvis\" now\n", *sources, *seconds)
	return c.run(ctx, strings.Split(*sources, ","), *seconds)
}

// wakeCompare records every source at once, then scores each recording with the one detector
// (reset between streams, so each scores as a fresh pipeline would).
type wakeCompare struct {
	det    voice.Detector
	record func(ctx context.Context, source string, samples int) ([]int16, error)
	out    io.Writer
	floor  float64
}

func (c *wakeCompare) run(ctx context.Context, sources []string, seconds float64) error {
	n := int(seconds * compareRate)
	recs := make([][]int16, len(sources))
	errs := make([]error, len(sources))
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i], errs[i] = c.record(ctx, src, n)
		}()
	}
	wg.Wait()
	var failed error
	for i, src := range sources {
		if errs[i] != nil {
			fmt.Fprintf(c.out, "%s: %v\n", src, errs[i])
			failed = fmt.Errorf("%s not recorded (is the canceller engaged? play music first)", src)
			continue
		}
		scores, err := scoreChunks(c.det, recs[i])
		if err != nil {
			return fmt.Errorf("%s: score: %w", src, err)
		}
		c.report(src, float64(len(recs[i]))/compareRate, scores)
	}
	return failed
}

// scoreChunks runs samples through det from a clean start, one score per 80 ms chunk (a
// trailing part-chunk is dropped, as the pipeline's capture never yields one).
func scoreChunks(det voice.Detector, samples []int16) ([]float32, error) {
	det.Reset()
	scores := make([]float32, 0, len(samples)/compareChunk)
	for i := 0; i+compareChunk <= len(samples); i += compareChunk {
		s, err := det.Process(samples[i : i+compareChunk])
		if err != nil {
			return nil, err
		}
		scores = append(scores, s)
	}
	return scores, nil
}

// report prints one stream's peak and every frame at or above the floor. A frame's time is the
// end of its chunk.
func (c *wakeCompare) report(source string, recordedSecs float64, scores []float32) {
	at := func(i int) float64 { return float64(i+1) * compareChunk / compareRate }
	maxI := -1
	for i, s := range scores {
		if maxI < 0 || s > scores[maxI] {
			maxI = i
		}
	}
	if maxI < 0 {
		fmt.Fprintf(c.out, "%-18s %.1f s: no full frames recorded\n", source, recordedSecs)
		return
	}
	fmt.Fprintf(c.out, "%-18s %.1f s: max score %.3f at %.2f s\n", source, recordedSecs, scores[maxI], at(maxI))
	var hits []string
	for i, s := range scores {
		if float64(s) >= c.floor {
			hits = append(hits, fmt.Sprintf("%.2fs %.3f", at(i), s))
		}
	}
	if len(hits) > 0 {
		fmt.Fprintf(c.out, "  frames >= %.2f: %s\n", c.floor, strings.Join(hits, "  "))
	}
}

// parecRecord records n samples of 16 kHz mono s16le from a Pulse source, then stops parec. It
// starts streaming as soon as parec connects, so all the sources hear the same utterance.
func parecRecord(ctx context.Context, source string, n int) ([]int16, error) {
	cmd := exec.CommandContext(ctx, "parec", "--device="+source,
		fmt.Sprintf("--rate=%d", compareRate), "--channels=1", "--format=s16le", "--raw")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("parec: %w", err)
	}
	buf := make([]byte, 2*n)
	read, rerr := io.ReadFull(out, buf)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if read < 2 {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("parec: %s", msg)
		}
		return nil, fmt.Errorf("parec: %w", rerr)
	}
	samples := make([]int16, read/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
	}
	return samples, nil
}
