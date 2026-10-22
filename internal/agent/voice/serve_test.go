package voice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
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

// #486: each reply and chime opens the device ReplyOutput names when it starts (a node setting
// or a Bluetooth route changes it between replies), and Output when that device won't open (a
// Pi whose asound.conf predates it).
func TestPlaybackOpensTheReplyOutputOrFallsBack(t *testing.T) {
	var opened []string
	open := func(name string, rate, ch int) (device.Playback, error) {
		opened = append(opened, name)
		if name == "jarvis_reply" {
			return nil, errors.New("Unknown PCM jarvis_reply")
		}
		return device.CreateWAVPlayback(t.TempDir()+"/out.wav", rate, ch)
	}
	reply := "output"
	o := Options{Output: "output", ReplyOutput: func() string { return reply },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pb := playback(o, open)
	for _, r := range []string{"output", "jarvis_reply"} {
		reply = r
		p, err := pb(24000, 1)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
	}
	if want := []string{"output", "jarvis_reply", "output"}; !slices.Equal(opened, want) {
		t.Fatalf("opened %v, want %v", opened, want)
	}

	// No ReplyOutput: Output, as before.
	opened = nil
	if p, err := playback(Options{Output: "output"}, open)(24000, 1); err != nil {
		t.Fatal(err)
	} else {
		p.Close()
	}
	if !slices.Equal(opened, []string{"output"}) {
		t.Fatalf("opened %v", opened)
	}
}
