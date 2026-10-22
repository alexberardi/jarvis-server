package voice

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// Options is a pipeline bound to real devices.
type Options struct {
	Input, Output string // device names: "default", an ALSA PCM name, or "wav:PATH"
	Config        Config
	Detector      Detector
	Server        Server
	Tools         node.Toolset // client tools for voice turns (nil: none)
	Log           *slog.Logger
	OnTurn        func(Turn)
	Indicator     Indicator // the on-screen state (nil: none)
	// Vocabulary is the names speech-to-text should expect (nil: none; Pipeline.Vocabulary).
	Vocabulary func(ctx context.Context) string
	// Live, if set, retunes and measures whichever pipeline is running (Pipeline.Live).
	Live *Live
	// Mic, when set, records takes through the pipeline (Pipeline.Mic).
	Mic *Mic
	// Music, when set, is ducked for each turn (Pipeline.Music).
	Music Music
	// Computer, when set, is paused by the wake word and stopped by "stop" (Pipeline.Computer).
	Computer Computer
	// WakeScores is the calibration history (Pipeline.WakeScores), kept across restarts.
	WakeScores *WakeScores
	// Switch, when set, is the microphone, started on Input: the Pi's echo canceller moves it to
	// the cancelled capture while the node's music plays (internal/agent/music).
	Switch *device.Switch
}

// Serve listens on Input until ctx ends. If the pipeline stops (a microphone unplugged or
// taken by another app, a wake-word engine error) it is restarted with backoff rather than
// taking the agent down; desktop tools keep working meanwhile.
func Serve(ctx context.Context, o Options) error {
	backoff := 2 * time.Second
	for {
		started := time.Now()
		err := serveOnce(ctx, o)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		o.Log.Warn("voice: pipeline stopped; restarting", "input", o.Input, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, time.Minute)
	}
}

// openMic opens Input, through the Switch when there is one.
func openMic(o Options) (device.Capture, error) {
	if o.Switch == nil {
		return device.OpenCapture(o.Input, CaptureRate, 1)
	}
	if err := o.Switch.Start(o.Input); err != nil {
		return nil, err
	}
	return o.Switch, nil
}

func serveOnce(ctx context.Context, o Options) error {
	mic, err := openMic(o)
	if err != nil {
		return err
	}
	defer mic.Close()
	o.Detector.Reset()
	p := &Pipeline{Cfg: o.Config, Capture: mic, Detector: o.Detector, Server: o.Server, Tools: o.Tools, Log: o.Log, OnTurn: o.OnTurn, Indicator: o.Indicator,
		Vocabulary: o.Vocabulary, Live: o.Live, Mic: o.Mic, Music: o.Music, Computer: o.Computer, WakeScores: o.WakeScores,
		Playback: func(rate, ch int) (device.Playback, error) { return device.OpenPlayback(o.Output, rate, ch) }}
	o.Log.Info("voice: listening", "input", o.Input, "output", o.Output, "backend", device.Backend(),
		"wake_threshold", o.Config.WakeThreshold, "wake_threshold_auto", o.Config.WakeThresholdAuto)
	err = p.Run(ctx)
	if n := mic.Overruns(); n > 0 || p.Dropped() > 0 {
		o.Log.Info("voice: capture stats", "overruns", n, "dropped_chunks", p.Dropped())
	}
	if err == nil && ctx.Err() == nil {
		err = fmt.Errorf("pipeline stopped")
	}
	return err
}
