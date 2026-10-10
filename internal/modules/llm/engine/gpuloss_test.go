package engine

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExpectedGPU(t *testing.T) {
	cuda := Placement{Flavour: FlavourCUDA, Devices: "0"}
	cases := []struct {
		name    string
		c       LabelConfig
		last    Placement
		want    Flavour
		expects bool
	}{
		{"cpu-only install: auto, no devices, no history", LabelConfig{GPUBackend: "auto", GPULayers: 999}, Placement{}, "", false},
		{"auto with devices configured", LabelConfig{GPUBackend: "auto", GPUDevices: "0", GPULayers: 999}, Placement{}, "", true},
		{"auto, ran on cuda before", LabelConfig{GPUBackend: "auto", GPULayers: 999}, cuda, FlavourCUDA, true},
		{"explicit cuda", LabelConfig{GPUBackend: "cuda", GPULayers: 999}, Placement{}, FlavourCUDA, true},
		{"explicit backend beats history", LabelConfig{GPUBackend: "vulkan", GPULayers: 999}, cuda, FlavourVulkan, true},
		{"explicit cpu, even after cuda", LabelConfig{GPUBackend: "cpu", GPUDevices: "0", GPULayers: 999}, cuda, "", false},
		{"gpu_layers 0 (embeddings default)", LabelConfig{GPUBackend: "auto", GPUDevices: "0", GPULayers: 0}, cuda, "", false},
		{"history on the cpu", LabelConfig{GPUBackend: "auto", GPULayers: 999}, Placement{Flavour: FlavourCPU}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, ok := ExpectedGPU(tc.c, tc.last)
			if w != tc.want || ok != tc.expects {
				t.Fatalf("got %q %v, want %q %v", w, ok, tc.want, tc.expects)
			}
		})
	}
}

func TestGPULost(t *testing.T) {
	gpu := Hardware{Flavour: FlavourCUDA, Devices: []Device{{Backend: FlavourCUDA, Index: 0, TotalMB: 24000}}}
	none := Hardware{Flavour: FlavourCPU}
	broken := Hardware{Flavour: FlavourCPU, Fault: DriverMismatchFault("580.173.04", "580.178.04")}
	cuda := Placement{Flavour: FlavourCUDA}
	cases := []struct {
		name string
		want Flavour
		hw   Hardware
		last Placement
		lost bool
	}{
		{"any GPU, one is there", "", gpu, Placement{}, false},
		{"any GPU, none detected", "", none, Placement{}, true},
		{"cuda as before", FlavourCUDA, gpu, cuda, false},
		{"cuda seen before, gone now", FlavourCUDA, none, cuda, true},
		{"driver mismatch", FlavourCUDA, broken, Placement{}, true},
		{"explicit backend never seen, no fault: engine decides as before", FlavourVulkan, none, Placement{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GPULost(tc.want, tc.hw, tc.last); got != tc.lost {
				t.Fatalf("got %v", got)
			}
		})
	}
}

// memStore is an in-memory GPUMemory.
type memStore struct {
	mu sync.Mutex
	m  map[string]Placement
}

func (s *memStore) Load() (map[string]Placement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Placement{}
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}

func (s *memStore) Save(m map[string]Placement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m = m
	return nil
}

func (s *memStore) get(label string) Placement {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[label]
}

// hwBox is a swappable detection result.
type hwBox struct {
	mu sync.Mutex
	h  Hardware
}

func (b *hwBox) set(h Hardware) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h.DetectedAt.IsZero() {
		h.DetectedAt = time.Now()
	}
	b.h = h
}

func (b *hwBox) get(context.Context) Hardware {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.h
}

var (
	hwCUDA     = Hardware{Flavour: FlavourCUDA, Devices: []Device{{Backend: FlavourCUDA, Index: 0, ID: "CUDA0", TotalMB: 24000}}}
	hwCPU      = Hardware{Flavour: FlavourCPU}
	hwMismatch = Hardware{Flavour: FlavourCPU, Fault: DriverMismatchFault("580.173.04", "580.178.04")}
)

func gpuRig(t *testing.T, h Hardware, mem GPUMemory) (*rig, *hwBox) {
	g := newRig(t)
	box := &hwBox{}
	box.set(h)
	g.r.Hardware = box.get
	g.r.Memory = mem
	return g, box
}

// gpuRefused asserts the label fails at once with gpu_unavailable and the given reason.
func gpuRefused(t *testing.T, r *Resolver, label, reason string) {
	t.Helper()
	start := time.Now()
	_, err := r.Resolve(context.Background(), label)
	var nr *NotReadyError
	if !errors.As(err, &nr) || nr.State != StateGPUUnavailable || nr.Reason != reason {
		t.Fatalf("want gpu_unavailable %q, got %v", reason, err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("refusal took %v: it must be immediate", d)
	}
}

func TestCPUOnlyInstallUnaffected(t *testing.T) {
	g, _ := gpuRig(t, hwCPU, &memStore{})
	g.src.set(g.llm(LabelLive, g.model("a.gguf")))
	g.ready(LabelLive)
	g.r.Reconcile(context.Background())
	is := g.r.Instances()
	if len(is) != 1 || is[0].Flavour != FlavourCPU {
		t.Fatalf("%+v", is)
	}
	if got := g.r.GPUUnavailable(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestDriverMismatchRefusesInsteadOfCPU(t *testing.T) {
	g, _ := gpuRig(t, hwMismatch, &memStore{})
	live := g.llm(LabelLive, g.model("a.gguf"))
	live.GPUDevices = "0" // what the wizard writes on a GPU box
	g.src.set(live)
	emb := LabelConfig{Label: LabelEmbeddings, Kind: KindLlama, ModelKind: ModelEmbedding, Engine: ModeLocal, Model: "e",
		ModelPath: g.model("e.gguf"), Context: 512, Parallel: 4, GPULayers: 0, GPUBackend: "auto", Embedding: true}
	g.src.set(emb)

	gpuRefused(t, g.r, LabelLive, UserMsgDriverUpdated)
	// Embeddings run on the CPU by design (gpu_layers 0): unaffected.
	g.ready(LabelEmbeddings)
	g.r.Reconcile(context.Background())
	for _, s := range g.starts() {
		for i, a := range s.Args {
			if a == "-m" && s.Args[i+1] == live.ModelPath {
				t.Fatalf("live was started: %v", s.Args)
			}
		}
	}

	st := g.r.Status(context.Background())
	var ls LabelStatus
	for _, s := range st {
		if s.Label == LabelLive {
			ls = s
		}
	}
	if ls.State != StateGPUUnavailable || ls.Reason != UserMsgDriverUpdated {
		t.Fatalf("%+v", ls)
	}
	f := FaultFor(hwMismatch, st)
	if f == nil || f.Kind != FaultDriverMismatch || len(f.Labels) != 1 || f.Labels[0] != LabelLive ||
		f.Message != "NVIDIA driver updated (kernel 580.173.04, libraries 580.178.04): reboot the server." {
		t.Fatalf("%+v", f)
	}
	if got := g.r.GPUUnavailable(); len(got) != 1 || got[0] != LabelLive {
		t.Fatalf("%v", got)
	}
}

func TestLastKnownGPUIsRemembered(t *testing.T) {
	mem := &memStore{}
	g, _ := gpuRig(t, hwCUDA, mem)
	g.src.set(g.llm(LabelLive, g.model("a.gguf"))) // all auto: nothing in the settings says GPU
	g.ready(LabelLive)
	g.r.Reconcile(context.Background())
	if p := mem.get(LabelLive); p.Flavour != FlavourCUDA {
		t.Fatalf("not remembered: %+v", p)
	}

	// jarvisd restarts with the GPU gone and no driver explanation: the generic reason.
	g.r.StopAll(context.Background())
	g2, _ := gpuRig(t, hwCPU, mem)
	g2.src.set(g2.llm(LabelLive, g.model("a.gguf")))
	gpuRefused(t, g2.r, LabelLive, UserMsgGPUUnavailable)
	if f := FaultFor(hwCPU, g2.r.Status(context.Background())); f == nil || f.Kind != FaultGPUUnavailable ||
		f.UserMessage != UserMsgGPUUnavailable || len(f.Labels) != 1 {
		t.Fatalf("%+v", f)
	}

	// The GPU comes back: the label runs again.
	g2.r.Hardware = (&hwBox{h: hwCUDA}).get
	g2.ready(LabelLive)
	if got := g2.r.GPUUnavailable(); len(got) != 0 {
		t.Fatalf("%v", got)
	}

	// The operator's way out on a box that lost its GPU for good: gpu_backend cpu, which also
	// forgets the history so "auto" means CPU afterwards.
	g2.r.Hardware = (&hwBox{h: hwCPU}).get
	c := g2.llm(LabelLive, g.model("a.gguf"))
	c.GPUBackend = "cpu"
	g2.src.set(c)
	g2.ready(LabelLive)
	g2.r.Reconcile(context.Background())
	if p := mem.get(LabelLive); p != (Placement{}) {
		t.Fatalf("still remembered: %+v", p)
	}
}

func TestRunningGPUEngineSurvivesLaterFault(t *testing.T) {
	g, box := gpuRig(t, hwCUDA, &memStore{})
	c := g.llm(LabelLive, g.model("a.gguf"))
	c.GPUDevices = "0"
	g.src.set(c)
	ep := g.ready(LabelLive)
	g.r.Reconcile(context.Background())

	// A driver upgrade lands while jarvisd runs; the periodic check re-detects. The engine
	// already loaded keeps working until the reboot, so it keeps serving.
	time.Sleep(5 * time.Millisecond)
	box.set(hwMismatch)
	g.r.Reconcile(context.Background())
	got, err := g.r.Resolve(context.Background(), LabelLive)
	if err != nil || got.Engine != ep.Engine {
		t.Fatalf("running engine dropped: %v %+v", err, got)
	}
	if n := len(g.starts()); n != 1 {
		t.Fatalf("%d starts", n)
	}

	// Once it dies, it isn't brought back on the CPU.
	g.r.StopAll(context.Background())
	gpuRefused(t, g.r, LabelLive, UserMsgDriverUpdated)
}

func TestFileGPUMemory(t *testing.T) {
	f := FileGPUMemory{Path: filepath.Join(t.TempDir(), "engines", "gpu-placements.json")}
	m, err := f.Load()
	if err != nil || len(m) != 0 {
		t.Fatalf("%v %v", m, err)
	}
	want := map[string]Placement{LabelLive: {Flavour: FlavourCUDA, Devices: "0"}, LabelBackground: {Flavour: FlavourCUDA, Devices: "1"}}
	if err := f.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := f.Load()
	if err != nil || len(got) != 2 || got[LabelBackground] != want[LabelBackground] {
		t.Fatalf("%v %v", got, err)
	}
}

func TestWatchGPUReDetectsOnDriverChange(t *testing.T) {
	var mu sync.Mutex
	var fault *GPUFault
	det := &Detector{Platform: Platform{"linux", "amd64"}, Run: fakeRunner(nil), Driver: func() *GPUFault {
		mu.Lock()
		defer mu.Unlock()
		return fault
	}}
	det.Hardware(context.Background(), false)
	res := &Resolver{Source: &mapSource{}, Binaries: &fakeBinaries{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go WatchGPU(ctx, det, res, 10*time.Millisecond)
	mu.Lock()
	fault = DriverMismatchFault("580.173.04", "580.178.04")
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for det.Hardware(context.Background(), false).Fault == nil {
		if time.Now().After(deadline) {
			t.Fatal("the watch never re-detected")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
