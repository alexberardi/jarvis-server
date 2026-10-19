package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
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
