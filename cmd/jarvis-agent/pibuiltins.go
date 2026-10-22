package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/music"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/reminders"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// sayLimit bounds one announcement: waiting for a turn to end, then speaking.
const sayLimit = 2 * time.Minute

// newPiHost is the Pi's side of its built-in commands' SDK backends (go-pi-node.md §5.2): the
// timers (stateDir/timers.json, said on expiry), the alert queue, the volume
// (stateDir/volume.json, applied again now) and speech, all through the voice pipeline's
// microphone between turns. Command-center is reached once connected (SetCC). It stops when
// ctx ends.
func newPiHost(ctx context.Context, stateDir string, mic *voice.Mic, log *slog.Logger) *sdkhost.Host {
	h := &sdkhost.Host{Alerts: &sdkhost.Alerts{}, Log: log,
		Audio: &sdkhost.Audio{Path: filepath.Join(stateDir, "volume.json"), Run: sdkhost.ExecRun, Log: log}}
	// Queued, never waited for: a command may speak mid-turn, while the pipeline waits for it.
	// A pipeline not listening yet (a timer that ended while the agent was down, said at
	// start) or reopening its microphone is waited for, up to sayLimit.
	h.Say = func(text string) bool {
		go func() {
			sctx, cancel := context.WithTimeout(ctx, sayLimit)
			defer cancel()
			for {
				err := mic.Say(sctx, text)
				if !errors.Is(err, voice.ErrMicUnavailable) {
					if err != nil {
						log.Warn("voice: not said", "text", text, "err", err)
					}
					return
				}
				select {
				case <-sctx.Done():
					log.Warn("voice: not said: no microphone is listening", "text", text)
					return
				case <-time.After(time.Second):
				}
			}
		}()
		return true
	}
	h.Timers = &timers.Service{Path: filepath.Join(stateDir, "timers.json"), Log: log,
		Fire: func(t timers.Timer) {
			log.Info("timers: done", "id", t.ID, "label", t.Label)
			h.Say(timers.Message(t.Label))
		}}
	h.Timers.Start()
	go h.Audio.Restore(ctx)
	go func() { <-ctx.Done(); h.Timers.Close() }()
	return h
}

// startAlerts runs the Pi's alerts: the announcer, which says the queue's announceable alerts
// between turns, and the reminders, polled from the sidecar's reminder service into the queue
// (go-pi-node.md §7 4.5). It stops when ctx ends.
func startAlerts(ctx context.Context, h *sdkhost.Host, mic *voice.Mic, live *sidecar.Live, log *slog.Logger) *sdkhost.Announcer {
	a := &sdkhost.Announcer{Alerts: h.Alerts, Say: sayNow(mic), Log: log}
	tick := time.NewTicker(sdkhost.SweepEvery)
	go func() { defer tick.Stop(); a.Run(ctx, tick.C) }()
	if live != nil {
		p := &reminders.Poller{Alerts: h.Alerts, Log: log, Check: func(ctx context.Context) ([]reminders.Due, int, error) {
			due, pending, err := live.RemindersDue(ctx)
			out := make([]reminders.Due, len(due))
			for i, d := range due {
				out[i] = reminders.Due(d)
			}
			return out, pending, err
		}}
		go p.Run(ctx)
	}
	return a
}

// sayNow speaks text between turns and returns once it is said (the announcer's Say). Nothing
// played (no pipeline listening, or the turn in progress outlasting sayLimit) wraps
// sdkhost.ErrUnheard, so the alert stays queued.
func sayNow(mic *voice.Mic) func(context.Context, string) error {
	return func(ctx context.Context, text string) error {
		sctx, cancel := context.WithTimeout(ctx, sayLimit)
		defer cancel()
		err := mic.Say(sctx, text)
		if errors.Is(err, voice.ErrMicUnavailable) || (err != nil && sctx.Err() != nil) {
			return fmt.Errorf("%w: %w", sdkhost.ErrUnheard, err)
		}
		return err
	}
}

// alertFlash is how long a button press that couldn't be heard shows red (Python, TTS missing).
const alertFlash = 2 * time.Second

// readAlerts is the HAT button's short press (N:announce_pending_and_flush): every pending
// alert read out, or "No new notifications."; nothing heard flashes the error pattern.
func readAlerts(ctx context.Context, a *sdkhost.Announcer, leds *hat.LEDs, log *slog.Logger) {
	n, err := a.ReadAll(ctx)
	if err != nil {
		log.Warn("hat: alerts not read", "err", err)
		if leds != nil {
			leds.Preview(hat.Error, alertFlash)
		}
		return
	}
	log.Info("hat: button read the alerts", "count", n)
}

// alertLED shows the queue on the HAT's stable layer: the middle LED purple while an alert would
// be said, normal otherwise (N:main.py's on_change).
func alertLED(leds *hat.LEDs) func(announceable int) {
	return func(n int) {
		if n > 0 {
			leds.SetPattern(hat.Alert)
		} else {
			leds.SetPattern(hat.Normal)
		}
	}
}

// newPiMusic is the Pi's music ducking and takeover (internal/agent/music) with N:'s defaults;
// with the HAT, its mic gain follows the music (the codec's PGA) and the music is echo-cancelled
// out of the mic (the voice pipeline reads micSwitch's). Only on a Pi install (the HAT, or
// install-pi.sh's marker): a dev box run with --profile pi (node-sim, the voice lane) must
// never SIGSTOP or interrupt the user's own players.
func newPiMusic(detected func() bool, exists func(string) bool, log *slog.Logger) *music.Ducker {
	if !detected() && !exists(piMarker) {
		return nil
	}
	if !detected() {
		return music.New(nil, log)
	}
	d := music.New(hatGain{}, log)
	sw := device.NewSwitch(voice.CaptureRate, 1)
	ec := music.NewEchoCancel(sw, log)
	sw.OnFallback = func(name, reason string, err error) {
		if log != nil {
			log.Warn("voice: echo-cancelled capture failed; back on the plain mic until the music starts again",
				"device", name, "reason", reason, "err", err)
		}
		go ec.GiveUp(context.Background(), reason) // pactl must not hold up the capture loop
	}
	d.EC = ec
	return d
}

// micSwitch is the voice pipeline's microphone when the echo canceller switches it, else nil.
func micSwitch(d *music.Ducker) *device.Switch {
	if d == nil || d.EC == nil {
		return nil
	}
	sw, _ := d.EC.Capture.(*device.Switch)
	return sw
}

// hatGain is the HAT codec's mic PGA.
type hatGain struct{}

func (hatGain) Get(ctx context.Context) (int, bool) { return hat.CapturePGA(ctx, hat.ExecRun) }
func (hatGain) Set(ctx context.Context, pct int) bool {
	return hat.SetCapturePGA(ctx, hat.ExecRun, pct)
}

// connectHost hands host the node's command-center client as the runner connects, before
// the rest of OnConnected (the voice pipeline) runs.
func connectHost(r *node.Runner, host *sdkhost.Host) {
	if host == nil {
		return
	}
	serve := r.OnConnected
	r.OnConnected = func(ctx context.Context, cc *node.CC) {
		host.SetCC(cc)
		if serve != nil {
			serve(ctx, cc)
		}
	}
}
