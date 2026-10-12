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

func serveOnce(ctx context.Context, o Options) error {
	mic, err := device.OpenCapture(o.Input, CaptureRate, 1)
	if err != nil {
		return err
	}
	defer mic.Close()
	o.Detector.Reset()
	p := &Pipeline{Cfg: o.Config, Capture: mic, Detector: o.Detector, Server: o.Server, Tools: o.Tools, Log: o.Log, OnTurn: o.OnTurn, Indicator: o.Indicator,
		Playback: func(rate, ch int) (device.Playback, error) { return device.OpenPlayback(o.Output, rate, ch) }}
	o.Log.Info("voice: listening", "input", o.Input, "output", o.Output, "backend", device.Backend(),
		"wake_threshold", o.Config.WakeThreshold)
	err = p.Run(ctx)
	if n := mic.Overruns(); n > 0 || p.Dropped() > 0 {
		o.Log.Info("voice: capture stats", "overruns", n, "dropped_chunks", p.Dropped())
	}
	if err == nil && ctx.Err() == nil {
		err = fmt.Errorf("pipeline stopped")
	}
	return err
}
