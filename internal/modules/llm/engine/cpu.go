package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
	"github.com/alexberardi/jarvis-server/internal/voice/fallback"
)

// The local CPU fallback of a remote stt label (docs/llm/07-remote-speech.md §2, decision
// 2026-10-22-remote-speech-fallback.md): when the provider fails and fallback=cpu, the stt
// module sends the clip to a whisper-server running the label's own model on CPU. It is an
// engine like any other, bound under the label's name plus CPUSuffix so it never replaces the
// label's binding; it starts on first use and stops after CPUIdle unused, or once the label is
// no longer remote with fallback=cpu.

// CPUSuffix is appended to a label's name for its CPU fallback binding (Instances' labels).
const CPUSuffix = ":cpu"

// cpuPoll is how often ResolveCPU asks again while the engine loads.
var cpuPoll = 50 * time.Millisecond

// ResolveCPU returns the endpoint of label's local CPU fallback (a remote stt label with
// fallback=cpu), starting it if needed and waiting up to CPUWait while it loads. It ignores
// engine=remote and the gpu settings: the label's model runs with gpu_layers=0 on a CPU build.
// With no model it returns an ErrNotConfigured saying to install one; nothing is downloaded.
func (r *Resolver) ResolveCPU(ctx context.Context, label string) (Endpoint, error) {
	wait := r.CPUWait
	if wait <= 0 {
		wait = 30 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		ep, err := r.resolveWith(ctx, func() (Endpoint, *instance, error) { return r.applyCPU(ctx, label) })
		if err == nil || !loadingCPU(err) || !time.Now().Before(deadline) {
			return ep, err
		}
		select {
		case <-ctx.Done():
			return ep, err
		case <-time.After(cpuPoll):
		}
	}
}

func loadingCPU(err error) bool {
	var nr *NotReadyError
	if !errors.As(err, &nr) {
		return false
	}
	switch nr.State {
	case string(engines.Starting), string(engines.Restarting), "fetching_engine":
		return true
	}
	return false
}

// applyCPU is ensure for label's CPU fallback binding. r.mu is held.
func (r *Resolver) applyCPU(ctx context.Context, label string) (Endpoint, *instance, error) {
	bind := label + CPUSuffix
	r.cpuUsed[label] = time.Now()
	c, err := r.cpuConfig(ctx, label)
	if err != nil {
		delete(r.bound, bind)
		r.last[bind] = err
		return Endpoint{}, nil, err
	}
	ep, inst, err := r.applyConfig(ctx, bind, c)
	r.last[bind] = err
	return ep, inst, err
}

// cpuConfig is label's configuration for its CPU fallback, or why it has none.
func (r *Resolver) cpuConfig(ctx context.Context, label string) (LabelConfig, error) {
	cur, err := r.Source.Label(ctx, label)
	if err != nil {
		return cur, err
	}
	if cur.Kind != KindWhisper || cur.Engine != ModeRemote {
		return cur, fmt.Errorf("%w: %s has no CPU fallback (only a remote stt label has one)", ErrNotConfigured, label)
	}
	if cur.Fallback != FallbackCPU {
		return cur, fmt.Errorf("%w: %s.fallback is %q, not cpu", ErrNotConfigured, label, cur.Fallback)
	}
	c := cur
	c.Engine = ModeLocal
	if ls, ok := r.Source.(LocalSource); ok {
		if c, err = ls.LocalLabel(ctx, label); err != nil {
			return c, err
		}
	}
	switch {
	case c.Problem != "":
		return c, &NotReadyError{Label: label, State: "misconfigured", Reason: "local CPU fallback: " + c.Problem}
	case c.ModelPath == "":
		return c, fmt.Errorf("%w: no whisper model for the local CPU fallback; install one on the Models page and pick it as the stt model",
			ErrNotConfigured)
	}
	c.Engine, c.GPUBackend, c.GPULayers, c.GPUDevices = ModeLocal, string(FlavourCPU), 0, ""
	return c, nil
}

// dropCPU unbinds the CPU fallbacks unused for CPUIdle or no longer configured, for gc to
// stop. r.mu is held.
func (r *Resolver) dropCPU(ctx context.Context) {
	idle := r.CPUIdle
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	for bind := range r.bound {
		label, ok := strings.CutSuffix(bind, CPUSuffix)
		if !ok {
			continue
		}
		c, err := r.Source.Label(ctx, label)
		stale := time.Since(r.cpuUsed[label]) > idle
		if stale || err != nil || c.Engine != ModeRemote || c.Fallback != FallbackCPU {
			r.Log.Info("stopping the local CPU fallback", "label", label, "idle", stale)
			delete(r.bound, bind)
			delete(r.last, bind)
		}
	}
}

// fallbackStatus is a remote stt label's fallback for Status; nil for every other label.
func (r *Resolver) fallbackStatus(ctx context.Context, c LabelConfig) *fallback.Status {
	if c.Kind != KindWhisper || c.Engine != ModeRemote {
		return nil
	}
	var st fallback.Status
	if r.SpeechFallback != nil {
		if s, ok := r.SpeechFallback(c.Label); ok {
			st = s
		}
	}
	st.Mode = c.Fallback
	if st.Mode == "" {
		st.Mode = FallbackOff
	}
	if st.Mode == FallbackCPU {
		if _, err := r.cpuConfig(ctx, c.Label); err != nil {
			st.Warning = err.Error()
		}
	}
	return &st
}
