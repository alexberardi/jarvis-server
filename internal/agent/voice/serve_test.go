package voice

import (
	"errors"
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
