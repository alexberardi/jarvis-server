package voice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// With a Switch the pipeline's mic is the Switch, started on Input (the Pi's echo canceller
// moves it); without one, Input opened directly.
func TestOpenMicThroughTheSwitch(t *testing.T) {
	var opened []string
	sw := device.NewSwitch(CaptureRate, 1)
	sw.Open = func(name string, rate, ch int) (device.Capture, error) {
		opened = append(opened, name)
		if name == "gone" {
			return nil, errors.New("no such PCM")
		}
		return device.NewWAVCapture(nil, rate, rate, ch, false), nil
	}
	mic, err := openMic(Options{Input: "dsnoopmic", Switch: sw})
	if err != nil || mic != device.Capture(sw) || sw.Base() != "dsnoopmic" || len(opened) != 1 || opened[0] != "dsnoopmic" {
		t.Fatalf("mic %v, err %v, base %q, opened %v", mic, err, sw.Base(), opened)
	}
	if _, err := openMic(Options{Input: "gone", Switch: sw}); err == nil {
		t.Fatal("a missing mic opened")
	}
	if _, err := openMic(Options{Input: "wav:/nonexistent.wav"}); err == nil {
		t.Fatal("no Switch: Input must be opened directly")
	}
}

// cancelOnRead is a microphone that ends the pipeline at its first read.
type cancelOnRead struct {
	cancel context.CancelFunc
	reads  *atomic.Int32
}

func (c cancelOnRead) Read(buf []int16) error { c.reads.Add(1); c.cancel(); return nil }
func (c cancelOnRead) Overruns() int          { return 0 }
func (c cancelOnRead) Close() error           { return nil }

// Serve's pipeline listens through the Switch, started on Input: without it the echo canceller
// has no capture to move (its Base is not the Pi's mic, so it never engages).
func TestServeListensThroughTheSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reads atomic.Int32
	var opened []string
	sw := device.NewSwitch(CaptureRate, 1)
	sw.Open = func(name string, rate, ch int) (device.Capture, error) {
		opened = append(opened, name)
		return cancelOnRead{cancel, &reads}, nil
	}
	o := Options{Input: "dsnoopmic", Output: "default", Config: DefaultConfig(), Detector: &fakeDetector{},
		Server: newServer(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Switch: sw}
	if err := serveOnce(ctx, o); err != nil {
		t.Fatal(err)
	}
	if sw.Base() != "dsnoopmic" || len(opened) == 0 || opened[0] != "dsnoopmic" || reads.Load() == 0 {
		t.Fatalf("base %q, opened %v, reads %d: the pipeline must read the Switch started on Input",
			sw.Base(), opened, reads.Load())
	}
}
