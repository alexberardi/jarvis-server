package models

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// sizedFile is a sparse file of n bytes under the env's home.
func sizedFile(t *testing.T, e *env, name string, n int64) string {
	t.Helper()
	p := filepath.Join(e.home, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
	return p
}

// oneRTX gives the env the devbox's card: one 12 GB RTX on CUDA.
func oneRTX(t *testing.T, e *env) {
	t.Helper()
	e.mgr.Detector = &engine.Detector{Platform: engine.Platform{OS: "linux", Arch: "amd64"},
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "nvidia-smi" {
				return []byte("0, NVIDIA GeForce RTX 3080 Ti, 12288, 12100\n"), nil
			}
			return nil, io.EOF
		}}
	linux := engine.Platform{OS: "linux", Arch: "amd64"}
	rel := engine.Releases[engine.KindLlama]
	saved := rel.Assets
	rel.Assets = map[engine.Platform]map[engine.Flavour][]engine.Asset{linux: {engine.FlavourCUDA: {{Name: "x"}}}}
	engine.Releases[engine.KindLlama] = rel
	t.Cleanup(func() { rel.Assets = saved; engine.Releases[engine.KindLlama] = rel })
	e.mgr.Detector.Hardware(e.ctx, true)
}

func llmLabel(label, model string) engine.LabelConfig {
	d, _ := engine.LabelDefFor(label)
	return engine.LabelConfig{Label: label, Kind: d.Kind, ModelKind: d.ModelKind, Engine: engine.ModeLocal,
		Model: model, ModelPath: model, Context: 8192, Parallel: 1, GPULayers: 999}
}

// #562: a 6 GB model fits a 12 GB card alone, not twice; PlacementFit says so, naming the card.
func TestPlacementFit(t *testing.T) {
	e := newEnv(t)
	oneRTX(t, e)
	m := sizedFile(t, e, "qwen.gguf", 6<<30)
	live, bg := llmLabel("live", m), llmLabel("background", m)
	bg.GPULayers = 40

	if why := e.mgr.PlacementFit(e.ctx, live, engine.FlavourCUDA, nil); why != "" {
		t.Fatalf("alone: %q", why)
	}
	full := bg
	full.GPULayers = 999
	why := e.mgr.PlacementFit(e.ctx, full, engine.FlavourCUDA, []engine.LabelConfig{live})
	for _, want := range []string{"NVIDIA GeForce RTX 3080 Ti (12288 MB) can't hold background", "next to live",
		"two copies of qwen.gguf", "engine=shared"} {
		if !strings.Contains(why, want) {
			t.Errorf("reason %q lacks %q", why, want)
		}
	}
	// Partial offload, a CPU engine or one on another card isn't judged.
	if why := e.mgr.PlacementFit(e.ctx, bg, engine.FlavourCUDA, []engine.LabelConfig{live}); why != "" {
		t.Errorf("partial offload: %q", why)
	}
	if why := e.mgr.PlacementFit(e.ctx, full, engine.FlavourCPU, []engine.LabelConfig{live}); why != "" {
		t.Errorf("cpu: %q", why)
	}
	other := live
	other.GPUDevices = "1"
	if why := e.mgr.PlacementFit(e.ctx, full, engine.FlavourCUDA, []engine.LabelConfig{other}); why != "" {
		t.Errorf("alongside on another card: %q", why)
	}
	// A small one fits next to it; one too big for the card alone is llama.cpp's to offload.
	small := llmLabel("background", sizedFile(t, e, "small.gguf", 1<<30))
	if why := e.mgr.PlacementFit(e.ctx, small, engine.FlavourCUDA, []engine.LabelConfig{live}); why != "" {
		t.Errorf("small: %q", why)
	}
	huge := llmLabel("background", sizedFile(t, e, "huge.gguf", 14<<30))
	if why := e.mgr.PlacementFit(e.ctx, huge, engine.FlavourCUDA, []engine.LabelConfig{live}); why != "" {
		t.Errorf("too big alone: %q", why)
	}
}

// A model outside the catalog has only a guessed (dense) KV size, which a sliding-window model
// like Gemma 4 needs far less than: prod's whisper was refused next to Gemma 4 26B (18.9 GB
// measured, 32.6 GB guessed). Placement judges such a model by its weights, so whisper starts
// next to it, while two copies that can't fit even by weight are still refused (TestPlacementFit).
func TestPlacementFitJudgesAGuessedKVByWeights(t *testing.T) {
	e := newEnv(t)
	oneRTX(t, e)
	live := llmLabel("live", sizedFile(t, e, "gemma.gguf", 7<<30))
	live.Context = 32768 // guessed KV ≈ 6 GB: ≈ 14 GB, more than the card
	stt := llmLabel("stt", sizedFile(t, e, "whisper.bin", 600<<20))
	if why := e.mgr.PlacementFit(e.ctx, stt, engine.FlavourCUDA, []engine.LabelConfig{live}); why != "" {
		t.Fatalf("whisper next to a guessed-KV model: %q", why)
	}
	big := llmLabel("stt", sizedFile(t, e, "big.bin", 4<<30))
	if why := e.mgr.PlacementFit(e.ctx, big, engine.FlavourCUDA, []engine.LabelConfig{live}); why == "" {
		t.Fatal("7 GB of weights and a 4 GB whisper on a 12 GB card was let through")
	}
}

// #562: Residents counts engines as the resolver runs them. Two labels on one model that differ
// in parallel slots are two engines (and so overcommit the card) unless they run on one
// (a running label's endpoint names its engine).
func TestResidentsFollowEngineIdentity(t *testing.T) {
	e := newEnv(t)
	oneRTX(t, e)
	m := sizedFile(t, e, "m.gguf", 6<<30)
	live, bg := llmLabel("live", m), llmLabel("background", m)
	bg.Parallel = 2
	e.labels.status = []engine.LabelStatus{{Label: "live", Config: live}, {Label: "background", Config: bg}}
	rs := e.mgr.Residents(e.ctx)
	if len(rs) != 2 {
		t.Fatalf("two engines counted as %d residents: %+v", len(rs), rs)
	}
	if w := Overcommitted(e.mgr.hardware(e.ctx), rs); len(w) != 1 || !strings.Contains(w[0], "live, background") {
		t.Fatalf("overcommit warning %v", w)
	}
	// Running on one engine (the resolver shared it): one resident.
	for i := range e.labels.status {
		e.labels.status[i].Endpoint = &engine.Endpoint{Engine: "llama-1"}
	}
	if rs := e.mgr.Residents(e.ctx); len(rs) != 1 || !slices.Equal(rs[0].Labels, []string{"live", "background"}) {
		t.Fatalf("one engine: %+v", rs)
	}
}
