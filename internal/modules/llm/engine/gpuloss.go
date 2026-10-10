package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
)

// ExpectedGPU decides whether a label must run on a GPU, and which backend ("" = any). It
// must when it offloads layers (gpu_layers ≠ 0) and either its settings name a GPU backend or
// devices, or it last ran healthy on a GPU (last, from GPUMemory). A label with no GPU in its
// settings and no GPU history (a CPU-only install) is not expected to, and runs on the CPU as
// before.
func ExpectedGPU(c LabelConfig, last Placement) (Flavour, bool) {
	if c.GPULayers == 0 {
		return "", false
	}
	f, err := ParseFlavour(c.GPUBackend)
	switch {
	case err != nil || f == FlavourCPU:
		return "", false
	case f != "":
		return f, true
	case last.Flavour != "" && last.Flavour != FlavourCPU:
		return last.Flavour, true
	case normList(c.GPUDevices) != "":
		return "", true
	}
	return "", false
}

// GPULost reports whether a label expecting a GPU of backend want ("" = any) can't have it:
// detection sees no such device, and either it saw one before (last) or a driver fault says
// why. A configured backend detection never saw, with no fault, keeps today's behaviour (the
// engine decides), since some backends (vulkan without vulkaninfo) are only visible once their
// build is installed.
func GPULost(want Flavour, hw Hardware, last Placement) bool {
	if want == "" {
		return hw.Flavour == "" || hw.Flavour == FlavourCPU
	}
	if len(hw.Discrete(want)) > 0 {
		return false
	}
	return hw.Fault != nil || last.Flavour == want
}

// gpuGate refuses a label whose expected GPU is lost, instead of letting it start on the CPU
// (prod outage 2026-10-10: every engine came up with -ngl 0 and every live call timed out).
// An engine that was already running on the GPU before the loss was detected keeps serving
// (the driver it loaded still works until the reboot) and is returned as keep. r.mu is held.
func (r *Resolver) gpuGate(ctx context.Context, label string, c LabelConfig) (*NotReadyError, *instance) {
	if r.Hardware == nil {
		return nil, nil
	}
	last := r.lastGood[label]
	want, expected := ExpectedGPU(c, last)
	if !expected {
		r.gpuBack(label)
		return nil, nil
	}
	hw := r.Hardware(ctx)
	if !GPULost(want, hw, last) {
		r.gpuBack(label)
		return nil, nil
	}
	if h, ok := r.bound[label]; ok {
		if inst := r.instances[h]; inst != nil && inst.key.Flavour != FlavourCPU && inst.key.GPULayers != 0 &&
			inst.key.Model == c.ModelPath {
			st := inst.sup.Status()
			if (st.State == engines.Healthy || st.State == engines.Unhealthy) && !st.Started.IsZero() &&
				st.Started.Before(hw.DetectedAt) {
				return nil, inst
			}
		}
	}
	msg := UserMsgGPUUnavailable
	if hw.Fault != nil && hw.Fault.UserMessage != "" {
		msg = hw.Fault.UserMessage
	}
	if r.gpuDown[label] != msg {
		r.gpuDown[label] = msg
		args := []any{"label", label, "want", want, "devices", c.GPUDevices, "last_flavour", last.Flavour,
			"last_devices", last.Devices}
		if hw.Fault != nil {
			args = append(args, "fault", hw.Fault.Kind, "kernel_module", hw.Fault.KernelVersion,
				"libraries", hw.Fault.LibraryVersion)
			if hw.Fault.Kind == FaultDriverMismatch {
				args = append(args, "action", "reboot required")
			}
		}
		r.Log.Error("label expects a GPU that isn't usable: not starting it on the CPU", args...)
	}
	return &NotReadyError{Label: label, State: StateGPUUnavailable, Reason: msg}, nil
}

// gpuBack notes a label that is no longer refused for a lost GPU. r.mu is held.
func (r *Resolver) gpuBack(label string) {
	if _, ok := r.gpuDown[label]; ok {
		delete(r.gpuDown, label)
		r.Log.Info("label's GPU is usable again", "label", label)
	}
}

// recordGPU remembers, per label, the GPU placement of a healthy engine detection can see,
// and forgets it when the label runs healthy on the CPU by choice (gpu_backend cpu or
// gpu_layers 0: the operator's way out once a GPU is really gone). r.mu is held.
func (r *Resolver) recordGPU(ctx context.Context) {
	if r.Hardware == nil {
		return
	}
	var hw *Hardware
	changed := false
	for label, h := range r.bound {
		inst := r.instances[h]
		if inst == nil || inst.sup.Status().State != engines.Healthy {
			continue
		}
		k := inst.key
		if k.Flavour == FlavourCPU || k.GPULayers == 0 {
			if _, ok := r.lastGood[label]; ok {
				delete(r.lastGood, label)
				changed = true
			}
			continue
		}
		if hw == nil {
			h := r.Hardware(ctx)
			hw = &h
		}
		if len(hw.Discrete(k.Flavour)) == 0 {
			continue
		}
		if p := (Placement{Flavour: k.Flavour, Devices: k.Devices}); r.lastGood[label] != p {
			r.lastGood[label] = p
			changed = true
		}
	}
	if changed && r.Memory != nil {
		if err := r.Memory.Save(maps.Clone(r.lastGood)); err != nil {
			r.Log.Warn("saving the last-known GPU placements", "err", err)
		}
	}
}

// GPUUnavailable lists the labels currently refused for a lost GPU.
func (r *Resolver) GPUUnavailable() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.init()
	out := slices.Collect(maps.Keys(r.gpuDown))
	slices.Sort(out)
	return out
}

// GPUMemory persists the last-known-good GPU placement per label.
type GPUMemory interface {
	Load() (map[string]Placement, error)
	Save(map[string]Placement) error
}

// FileGPUMemory keeps it in a JSON file (<home>/engines/gpu-placements.json).
type FileGPUMemory struct{ Path string }

// Load returns the saved placements; a missing file is an empty map.
func (f FileGPUMemory) Load() (map[string]Placement, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Placement{}, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]Placement
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Save writes the placements atomically.
func (f FileGPUMemory) Save(m map[string]Placement) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// FaultFor is the GPU warning the admin shows: the driver fault detection found, or a generic
// one when labels are refused for a lost GPU without one; nil when all is well.
func FaultFor(hw Hardware, labels []LabelStatus) *GPUFault {
	var down []string
	for _, ls := range labels {
		if ls.State == StateGPUUnavailable {
			down = append(down, ls.Label)
		}
	}
	if hw.Fault != nil {
		f := *hw.Fault
		f.Labels = down
		return &f
	}
	if len(down) > 0 {
		return UnavailableFault(down)
	}
	return nil
}

// WatchGPU re-checks the GPU every interval until ctx ends: when the cheap driver check
// changes (a driver upgrade while jarvisd runs) or labels are refused for a lost GPU, it
// re-detects and has the resolver reconcile. So the admin's warning appears without a
// jarvisd restart, and labels come back on their own if the GPU returns.
func WatchGPU(ctx context.Context, det *Detector, res *Resolver, every time.Duration) {
	if every <= 0 {
		every = 3 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if det.DriverChanged() || len(res.GPUUnavailable()) > 0 {
			det.Hardware(ctx, true)
			res.Notify()
		}
	}
}
