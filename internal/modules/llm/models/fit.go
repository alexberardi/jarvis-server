package models

import (
	"fmt"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// Fit is a VRAM fit estimate for a model on the detected hardware. It is a guide for the
// admin UI, not a guarantee: KV size comes from the catalog when known, else from a rough
// size-based guess.
type Fit struct {
	// Verdict: "fits", "tight" (within 10% of the card), "split" (only across several cards
	// with tensor split), "cpu" (runs on the CPU in system RAM: no usable GPU, or the labels
	// that take it are set to the CPU), "too_big" (for the card, or for RAM on the CPU), or
	// "in_binary" (tts/speaker: sherpa-onnx on the CPU inside jarvisd).
	Verdict    string `json:"verdict"`
	NeededMB   int64  `json:"needed_mb"`
	Context    int    `json:"context"`
	Device     string `json:"device,omitempty"` // the largest card considered
	DeviceMB   int64  `json:"device_mb,omitempty"`
	FreeMB     int64  `json:"free_mb,omitempty"`
	KVEstimate bool   `json:"kv_estimated"` // true when the KV size is a guess
	// CommittedMB is what engines already assigned to that card need, and Alongside names
	// their labels: the verdict is for this model next to them, not on an empty card.
	CommittedMB int64    `json:"committed_mb,omitempty"`
	Alongside   []string `json:"alongside,omitempty"`
	// RAMMB is the host's RAM when the model is judged for the CPU (0 = unknown).
	RAMMB int64 `json:"ram_mb,omitempty"`
}

// Resident is GPU memory an assigned local engine already needs. Labels sharing one engine
// (same model, context and devices) are one resident.
type Resident struct {
	Labels   []string `json:"labels"`
	Model    string   `json:"model"`
	NeededMB int64    `json:"needed_mb"`
	// Devices are backend device indexes; empty means the largest card (the engine default).
	Devices []int `json:"devices,omitempty"`
}

const mb = 1 << 20

// cpuRAMShare is the percentage of system RAM a model on the CPU may take: the OS, jarvisd
// and the other engines need the rest.
const cpuRAMShare = 70

// OnFlavour is hw as a label running the f build sees it: on CPU no card counts.
func OnFlavour(hw engine.Hardware, f engine.Flavour) engine.Hardware {
	if f != "" {
		hw.Flavour = f
	}
	return hw
}

// estimateNeed returns the bytes a model needs at ctx tokens of f16 KV cache, all of it: in
// system RAM on the CPU. On a GPU, an LLM's card holds that less its host bytes (gpuNeed).
//
// The 600 MB covers the engine's CUDA context and its compute buffers, and ~3% the rest of
// llama.cpp's own allocations. Checked against a running engine: Qwen3.5-9B Q4_K_M with its
// F16 projector at 32768 context, f16 KV, one slot, measured 7506 MB in nvidia-smi on a
// 3080 Ti; gpuNeed says 7560 MB.
func estimateNeed(kind string, weights, kvPerTok int64, ctx int) (int64, bool) {
	guessed := false
	switch kind {
	case engine.ModelSTT:
		// whisper: weights plus encoder/decoder buffers and the CUDA context (small.en: 466 MB
		// of weights measured 1056 MB in whisper-server on a 3080 Ti).
		return weights + weights/4 + 500*mb, false
	case engine.ModelEmbedding, engine.ModelMMProj:
		return weights + 100*mb, false
	}
	if kvPerTok <= 0 {
		// About 2.5e-5 bytes of KV per weight byte per token covers dense Qwen/Llama models
		// at Q4 (8B: 2.9e-5, 14B: 1.8e-5); hybrid models need far less.
		kvPerTok = weights / 40000
		guessed = true
	}
	return weights + kvPerTok*int64(ctx) + weights*3/100 + 600*mb, guessed
}

// gpuNeed is estimateNeed for a card: an LLM's host bytes (its input token embeddings, which
// llama.cpp keeps in system RAM) don't count.
func gpuNeed(kind string, weights, host, kvPerTok int64, ctx int) (int64, bool) {
	need, guessed := estimateNeed(kind, weights, kvPerTok, ctx)
	if kind == engine.ModelLLM && host > 0 && host < weights {
		need -= host
	}
	return need, guessed
}

// FitFor estimates a model of weights bytes (plus projector) at ctx on an otherwise empty hw.
func FitFor(hw engine.Hardware, kind string, weights, kvPerTok int64, ctx int) Fit {
	return FitAlongside(hw, kind, weights, kvPerTok, ctx, nil)
}

// FitAlongside estimates a model next to the residents: each card's usable memory is reduced
// by what the residents placed on it need, and the model is judged on the card with the most
// left.
func FitAlongside(hw engine.Hardware, kind string, weights, kvPerTok int64, ctx int, residents []Resident) Fit {
	return fitAlongside(hw, kind, weights, 0, kvPerTok, ctx, residents)
}

// fitAlongside is FitAlongside for a model that keeps host bytes of its weights in system
// RAM on a GPU (Entry.HostBytes).
func fitAlongside(hw engine.Hardware, kind string, weights, host, kvPerTok int64, ctx int, residents []Resident) Fit {
	if ctx <= 0 {
		ctx = 8192
	}
	if kind == KindTTS || kind == KindSpeaker {
		// In-binary (sherpa-onnx, CPU): RAM only.
		return Fit{Verdict: "in_binary", NeededMB: (weights + 100*mb) / mb}
	}
	need, guessed := estimateNeed(kind, weights, kvPerTok, ctx)
	f := Fit{NeededMB: need / mb, Context: ctx, KVEstimate: guessed}
	devs := hw.Discrete(hw.Flavour)
	if hw.Flavour == engine.FlavourCPU || len(devs) == 0 {
		// On the CPU the whole model lives in system RAM.
		f.Verdict, f.RAMMB = "cpu", hw.RAMMB
		if hw.RAMMB > 0 && f.NeededMB > hw.RAMMB*cpuRAMShare/100 {
			f.Verdict = "too_big"
		}
		return f
	}
	need, _ = gpuNeed(kind, weights, host, kvPerTok, ctx)
	f.NeededMB = need / mb
	committed := map[int]int64{}
	labels := map[int][]string{}
	for _, r := range residents {
		on := r.Devices
		if len(on) == 0 {
			on = []int{devs[0].Index}
		}
		for _, i := range on {
			committed[i] += r.NeededMB / int64(len(on))
			labels[i] = append(labels[i], r.Labels...)
		}
	}
	left := func(d engine.Device) int64 { return d.TotalMB*95/100 - committed[d.Index] }
	best := devs[0]
	var total int64
	for _, d := range devs {
		if left(d) > left(best) {
			best = d
		}
		total += max(left(d), 0)
	}
	f.Device, f.DeviceMB, f.FreeMB = best.Name, best.TotalMB, best.FreeMB
	f.CommittedMB, f.Alongside = committed[best.Index], labels[best.Index]
	usable := left(best)
	switch {
	case f.NeededMB <= usable-(best.TotalMB*95/100)/10:
		f.Verdict = "fits"
	case f.NeededMB <= usable:
		f.Verdict = "tight"
	case len(devs) > 1 && f.NeededMB <= total:
		f.Verdict = "split"
	default:
		f.Verdict = "too_big"
	}
	return f
}

// EntryFit estimates a catalog entry (with its projector) at its default context.
func EntryFit(hw engine.Hardware, e Entry) Fit {
	return entryFitAlongside(hw, e, nil)
}

func entryFitAlongside(hw engine.Hardware, e Entry, residents []Resident) Fit {
	w := e.Size
	if e.MMProj != "" {
		if p, ok := CatalogEntry(e.MMProj); ok {
			w += p.Size
		}
	}
	return minMemory(hw, e, fitAlongside(hw, e.Kind, w, e.HostBytes, e.KVBytesPerTok, e.ContextDefault, residents))
}

// hostMemoryMB is the machine's memory as an entry's MinMemoryMB means it: physical RAM, or on
// Apple Silicon the unified memory behind the Metal device's usable share (0 = unknown).
func hostMemoryMB(hw engine.Hardware) int64 {
	if hw.RAMMB > 0 {
		return hw.RAMMB
	}
	for _, d := range hw.Devices {
		if d.Backend == engine.FlavourMetal {
			return d.TotalMB * 10 / 7
		}
	}
	return 0
}

// minMemory makes f too_big for an entry the machine has less memory than it asks for.
func minMemory(hw engine.Hardware, e Entry, f Fit) Fit {
	if mem := hostMemoryMB(hw); e.MinMemoryMB > 0 && mem > 0 && mem < e.MinMemoryMB {
		f.Verdict = "too_big"
	}
	return f
}

// Overcommitted warns, per card, when the assigned engines together need more than it has
// (they would crash-loop on out-of-memory). Empty when everything fits.
func Overcommitted(hw engine.Hardware, residents []Resident) []string {
	devs := hw.Discrete(hw.Flavour)
	if hw.Flavour == engine.FlavourCPU || len(devs) == 0 {
		return []string{}
	}
	out := []string{}
	for _, d := range devs {
		var need int64
		var labels []string
		for _, r := range residents {
			on := r.Devices
			if len(on) == 0 {
				on = []int{devs[0].Index}
			}
			for _, i := range on {
				if i == d.Index {
					need += r.NeededMB / int64(len(on))
					labels = append(labels, r.Labels...)
				}
			}
		}
		if usable := d.TotalMB * 95 / 100; need > usable {
			out = append(out, fmt.Sprintf("%s (%s, %d MB): %s need about %d MB together; move one to another card, "+
				"lower its context, or pick a smaller model", d.ID, d.Name, d.TotalMB, strings.Join(labels, ", "), need))
		}
	}
	return out
}
