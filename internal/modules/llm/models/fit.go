package models

import (
	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// Fit is a VRAM fit estimate for a model on the detected hardware. It is a guide for the
// admin UI, not a guarantee: KV size comes from the catalog when known, else from a rough
// size-based guess.
type Fit struct {
	// Verdict: "fits", "tight" (within 10% of the card), "split" (only across several cards
	// with tensor split), "cpu" (no usable GPU: runs in RAM, slowly), "too_big", or
	// "in_binary" (tts/speaker: sherpa-onnx on the CPU inside jarvisd).
	Verdict    string `json:"verdict"`
	NeededMB   int64  `json:"needed_mb"`
	Context    int    `json:"context"`
	Device     string `json:"device,omitempty"` // the largest card considered
	DeviceMB   int64  `json:"device_mb,omitempty"`
	FreeMB     int64  `json:"free_mb,omitempty"`
	KVEstimate bool   `json:"kv_estimated"` // true when the KV size is a guess
}

const mb = 1 << 20

// estimateNeed returns the bytes a model needs at ctx tokens of f16 KV cache.
func estimateNeed(kind string, weights, kvPerTok int64, ctx int) (int64, bool) {
	guessed := false
	switch kind {
	case engine.ModelSTT:
		// whisper: weights plus encoder/decoder buffers.
		return weights + weights/4 + 200*mb, false
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

// FitFor estimates a model of weights bytes (plus projector) at ctx on hw.
func FitFor(hw engine.Hardware, kind string, weights, kvPerTok int64, ctx int) Fit {
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
		f.Verdict = "cpu"
		return f
	}
	best := devs[0]
	f.Device, f.DeviceMB, f.FreeMB = best.Name, best.TotalMB, best.FreeMB
	usable := best.TotalMB * 95 / 100
	var total int64
	for _, d := range devs {
		total += d.TotalMB * 95 / 100
	}
	switch {
	case f.NeededMB <= usable*9/10:
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
	w := e.Size
	if e.MMProj != "" {
		if p, ok := CatalogEntry(e.MMProj); ok {
			w += p.Size
		}
	}
	return FitFor(hw, e.Kind, w, e.KVBytesPerTok, e.ContextDefault)
}
