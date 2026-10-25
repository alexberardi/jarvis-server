package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// voiceSwitch turns listening for the wake word on and off in the running agent: the status
// icon's "Listen for "Hey Jarvis"" toggle. Off closes the microphone (the pipeline stops); on
// starts it again without restarting the process. The wake model loads once, on the first on.
// Each choice is saved as agent.json's `voice`, so the next start keeps it.
type voiceSwitch struct {
	log *slog.Logger
	// load builds the voice stack (loadVoice); serve runs the pipeline until ctx ends
	// (voice.Serve); persist saves the choice; changed shows it (the tray's checkmark).
	load    func(ctx context.Context) (*voiceStack, error)
	serve   func(ctx context.Context, vs *voiceStack, cc *node.CC)
	persist func(on bool) error
	changed func(on bool)
	// available: voice works on this OS (the control socket's voice-on/off refuses otherwise).
	available bool
	// waiting, if set, is called when listening is turned on before the agent has reached its
	// server: the pipeline starts on the connection, so there is nothing to listen with yet
	// and the user is told instead of nothing happening (#585).
	waiting func()
	// connected is closed once Serve has the agent's connection (tests wait on it).
	connected chan struct{}

	indMu      sync.Mutex
	indicators []voice.Indicator

	mu      sync.Mutex
	on      bool
	ctx     context.Context // the runner's, once connected
	cc      *node.CC
	vs      *voiceStack
	err     string
	cancel  context.CancelFunc // the pipeline running now
	running chan struct{}      // closed when it has stopped
	loadMu  sync.Mutex         // one load at a time
}

func newVoiceSwitch(on bool, log *slog.Logger) *voiceSwitch {
	return &voiceSwitch{on: on, log: log, connected: make(chan struct{}),
		persist: func(bool) error { return nil }, changed: func(bool) {}}
}

// addIndicator shows the pipeline's states somewhere (the island, the HAT's LEDs). Before the
// first on only.
func (w *voiceSwitch) addIndicator(i voice.Indicator) {
	w.indMu.Lock()
	defer w.indMu.Unlock()
	w.indicators = append(w.indicators, i)
}

// On reports whether listening is on (asked for, and not failed).
func (w *voiceSwitch) On() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.on
}

// lastErr is why voice last failed to start ("" if it didn't).
func (w *voiceSwitch) lastErr() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Set turns listening on or off now, and saves the choice.
func (w *voiceSwitch) Set(on bool) {
	if err := w.persist(on); err != nil {
		w.log.Warn("voice: couldn't save the choice; it lasts until the agent restarts", "err", err)
	}
	w.mu.Lock()
	w.on = on
	w.mu.Unlock()
	w.changed(on)
	if on {
		w.log.Info("voice: turned on from the menu")
		w.start()
		if w.pending() {
			w.log.Info("voice: listening starts once the Jarvis server answers")
			if w.waiting != nil {
				w.waiting()
			}
		}
	} else {
		w.log.Info("voice: turned off from the menu")
		w.stop()
	}
}

// pending: listening is on but can't start yet, because the agent hasn't reached its server
// (Serve, the runner's OnConnected, hands the switch the connection).
func (w *voiceSwitch) pending() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.on && w.ctx == nil
}

// Serve is the runner's OnConnected: it listens while on, until ctx ends.
func (w *voiceSwitch) Serve(ctx context.Context, cc *node.CC) {
	w.mu.Lock()
	w.ctx, w.cc = ctx, cc
	w.mu.Unlock()
	w.start()
	close(w.connected)
	<-ctx.Done()
	w.stop()
	w.mu.Lock()
	vs := w.vs
	w.vs = nil
	w.mu.Unlock()
	if vs != nil {
		vs.close()
	}
}

// start runs the pipeline if it should run and doesn't.
func (w *voiceSwitch) start() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.on || w.ctx == nil || w.running != nil || w.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(w.ctx)
	done := make(chan struct{})
	w.cancel, w.running = cancel, done
	cc := w.cc
	go func() {
		defer close(done)
		vs, err := w.stack(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return // turned off (or quitting) while loading
			}
			w.log.Error("voice: couldn't start listening", "err", err)
			w.mu.Lock()
			if w.running == done {
				w.on, w.err = false, err.Error()
			}
			w.mu.Unlock()
			w.changed(false)
			return
		}
		w.mu.Lock()
		w.err = ""
		w.mu.Unlock()
		w.serve(ctx, vs, cc)
	}()
}

// stack loads the voice stack on first use, with the indicators attached.
func (w *voiceSwitch) stack(ctx context.Context) (*voiceStack, error) {
	w.loadMu.Lock()
	defer w.loadMu.Unlock()
	w.mu.Lock()
	vs := w.vs
	w.mu.Unlock()
	if vs != nil {
		return vs, nil
	}
	vs, err := w.load(ctx)
	if err != nil {
		return nil, err
	}
	w.indMu.Lock()
	for _, i := range w.indicators {
		vs.addIndicator(i)
	}
	w.indMu.Unlock()
	w.mu.Lock()
	w.vs = vs
	w.mu.Unlock()
	return vs, nil
}

// stop ends the pipeline and waits for it (the microphone is closed when it returns), then
// hides whatever the indicators were showing.
func (w *voiceSwitch) stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.running
	w.cancel, w.running = nil, nil
	w.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	w.indMu.Lock()
	defer w.indMu.Unlock()
	for _, i := range w.indicators {
		i.SetState(voice.CueIdle, 0)
	}
}

// wait waits for a pipeline start in progress to settle (tests).
func (w *voiceSwitch) wait() {
	w.mu.Lock()
	done := w.running
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

// saveVoice writes agent.json's voice key (path: run's --config, or the default file).
func saveVoice(path string, on bool) error {
	if path == "" {
		var err error
		if path, err = settings.DefaultPath(); err != nil {
			return err
		}
	}
	return settings.Update(path, func(s *settings.Settings) error { s.Voice = &on; return nil })
}
