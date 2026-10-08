package models

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/engines"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// TestMain doubles as a fake engine (llama-server or whisper-server): with FAKE_ENGINE=1 it
// serves /health on --port and records nothing else. The engine package's tests cover argv
// and endpoints; here it only proves the stack wires install → label → running engine.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_ENGINE") == "1" {
		args := os.Args[1:]
		port, model := "", ""
		for i := 0; i < len(args)-1; i++ {
			switch args[i] {
			case "--port":
				port = args[i+1]
			case "-m":
				model = args[i+1]
			}
		}
		if _, err := os.Stat(model); err != nil {
			os.Exit(1)
		}
		l, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			os.Exit(2)
		}
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"status":"ok","model":%q}`, filepath.Base(model))
		}))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func blob(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)*7 + seed
	}
	return b
}

// hub fakes Hugging Face (API + resolve) and the engine release server.
type hub struct {
	mu       sync.Mutex
	files    map[string][]byte // URL path -> content
	repos    map[string]string // repo -> /api/models JSON
	gated    map[string]bool
	token    string
	throttle time.Duration // sleep per 32 KiB written
	cutOnce  map[string]bool
	hits     map[string]int
	hold     map[string]chan struct{} // URL path -> requests wait until it is closed
}

func newHub() *hub {
	return &hub{files: map[string][]byte{}, repos: map[string]string{}, gated: map[string]bool{},
		cutOnce: map[string]bool{}, hits: map[string]int{}, hold: map[string]chan struct{}{}}
}

func (h *hub) addRepo(repo, rev string, files map[string][]byte) {
	type sib struct {
		Name string `json:"rfilename"`
		Size int64  `json:"size"`
		LFS  any    `json:"lfs,omitempty"`
	}
	var sibs []sib
	for name, b := range files {
		sibs = append(sibs, sib{Name: name, Size: int64(len(b)), LFS: map[string]any{"sha256": sum(b), "size": len(b)}})
		h.files["/"+repo+"/resolve/"+rev+"/"+name] = b
	}
	sibs = append(sibs, sib{Name: "README.md", Size: 10})
	body, _ := json.Marshal(map[string]any{"id": repo, "sha": rev, "gated": false, "siblings": sibs})
	h.repos[repo] = string(body)
}

func (h *hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.hits[r.URL.Path]++
	throttle := h.throttle
	h.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/api/models/") {
		repo := strings.TrimPrefix(r.URL.Path, "/api/models/")
		repo, _, _ = strings.Cut(repo, "/revision/")
		if h.gated[repo] && r.Header.Get("Authorization") != "Bearer "+h.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, ok := h.repos[repo]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, body)
		return
	}
	h.mu.Lock()
	data, ok := h.files[r.URL.Path]
	hold := h.hold[r.URL.Path]
	h.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	start := 0
	if rg := r.Header.Get("Range"); rg != "" {
		start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		if start >= len(data) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
	}
	body := data[start:]
	h.mu.Lock()
	cut := h.cutOnce[r.URL.Path]
	h.cutOnce[r.URL.Path] = false
	h.mu.Unlock()
	if cut {
		w.Write(body[:len(body)/2])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	for len(body) > 0 {
		n := min(len(body), 32<<10)
		if _, err := w.Write(body[:n]); err != nil {
			return
		}
		body = body[n:]
		if throttle > 0 {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(throttle):
			}
		}
	}
}

type fakeLabels struct {
	mu        sync.Mutex
	notified  int
	status    []engine.LabelStatus
	instances []engine.InstanceStatus
}

func (f *fakeLabels) Status(context.Context) []engine.LabelStatus { return f.status }
func (f *fakeLabels) Instances() []engine.InstanceStatus          { return f.instances }
func (f *fakeLabels) Notify() {
	f.mu.Lock()
	f.notified++
	f.mu.Unlock()
}

type env struct {
	t      *testing.T
	ctx    context.Context
	home   string
	hub    *hub
	srv    *httptest.Server
	db     *db.DB
	set    *settings.Service
	q      *queue.Queue
	mgr    *Manager
	labels *fakeLabels
	deps   module.Deps
}

func tarGz(t *testing.T, files map[string]string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// testCatalog installs a small catalog served by the hub.
func (e *env) testCatalog() {
	model, proj, whisper := blob(300<<10, 1), blob(100<<10, 2), blob(50<<10, 3)
	e.hub.addRepo("acme/Tiny-GGUF", "rev1", map[string][]byte{"Tiny-Q4_K_M.gguf": model, "mmproj-F16.gguf": proj})
	e.hub.addRepo("acme/whisper", "rev2", map[string][]byte{"ggml-tiny.bin": whisper})
	cat := []Entry{
		{ID: "tiny", Display: "Tiny", Kind: "llm", Repo: "acme/Tiny-GGUF", Revision: "rev1", File: "Tiny-Q4_K_M.gguf",
			Size: int64(len(model)), SHA256: sum(model), MMProj: "tiny-mmproj", ContextDefault: 2048, PromptProvider: "Qwen3_8B_Compressed"},
		{ID: "tiny-mmproj", Kind: "mmproj", Repo: "acme/Tiny-GGUF", Revision: "rev1", File: "mmproj-F16.gguf",
			Size: int64(len(proj)), SHA256: sum(proj)},
		{ID: "whisper-tiny", Kind: "stt", Repo: "acme/whisper", Revision: "rev2", File: "ggml-tiny.bin",
			Size: int64(len(whisper)), SHA256: sum(whisper)},
	}
	b, _ := json.Marshal(cat)
	old := catalogData
	catalogData = b
	e.t.Cleanup(func() { catalogData = old })
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	home := t.TempDir()
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, d, "llm", os.DirFS("../migrations")); err != nil {
		t.Fatal(err)
	}
	set, err := settings.New(d, "llm", SettingDefinitions(), quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := newHub()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	set.Set(ctx, engine.KeyHFEndpoint, srv.URL, settings.Scope{})
	set.Set(ctx, engine.KeyLlamaBaseURL, srv.URL+"/llama", settings.Scope{})
	set.Set(ctx, engine.KeyWhisperBaseURL, srv.URL+"/whisper", settings.Scope{})

	// Engine releases for this host, served by the hub.
	host := engine.Host()
	for _, k := range engine.Kinds {
		archive := tarGz(t, map[string]string{"pkg/" + k.BinaryName(host): "bin", "pkg/libggml.so": "lib"})
		old := engine.Releases[k]
		r := old
		r.Build = "b0"
		// <base>/<tag>/<asset>: tag b0 for llama, engines-whisper-b0 for our whisper builds.
		h.files["/"+map[engine.Kind]string{engine.KindLlama: "llama", engine.KindWhisper: "whisper"}[k]+"/"+r.Tag()+"/eng.tar.gz"] = archive
		r.Assets = map[engine.Platform]map[engine.Flavour][]engine.Asset{host: {
			engine.FlavourCPU: {{Name: "eng.tar.gz", Size: int64(len(archive)), SHA256: sum(archive)}},
		}}
		engine.Releases[k] = r
		t.Cleanup(func() { engine.Releases[k] = old })
	}

	q := queue.New(d, quiet())
	q.PollInterval = 10 * time.Millisecond
	deps := module.Deps{Config: config.Config{Home: home}, DB: d, Log: quiet(), Queue: q}
	str := func(k string) string { return set.String(context.Background(), k, settings.Scope{}) }
	bins := engine.NewBinaries(home, quiet())
	bins.BaseURL = func(k engine.Kind) string { return str(engine.BaseURLKey(k)) }
	bins.Override = func(k engine.Kind) string { return str(engine.PathKey(k)) }
	labels := &fakeLabels{}
	mgr := &Manager{
		Store: &Store{DB: d}, Settings: set, Queue: q, Binaries: bins,
		Detector:  &engine.Detector{Platform: host, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, io.EOF }},
		Labels:    labels,
		HF:        &HF{Endpoint: func() string { return str(engine.KeyHFEndpoint) }, Token: func() string { return str(engine.KeyHFToken) }},
		ModelsDir: filepath.Join(home, "models"), Log: quiet(), ProgressEvery: time.Millisecond,
	}
	e := &env{t: t, ctx: ctx, home: home, hub: h, srv: srv, db: d, set: set, q: q, mgr: mgr, labels: labels, deps: deps}
	return e
}

func (e *env) start() {
	e.mgr.RegisterJobs(e.q)
	e.q.Start(e.ctx)
}

func (e *env) waitInstall(id int64, want string) Install {
	e.t.Helper()
	deadline := time.Now().Add(60 * time.Second) // sliced downloads under -race on CI runners are slow
	for {
		inst, err := e.mgr.Store.GetInstall(e.ctx, id)
		if err != nil {
			e.t.Fatal(err)
		}
		if inst.State == want {
			return inst
		}
		if inst.State == InstallFailed || inst.State == InstallDone || inst.State == InstallCancelled || time.Now().After(deadline) {
			e.t.Fatalf("install %d: state %s (want %s), error %q", id, inst.State, want, inst.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCatalogLint(t *testing.T) {
	ids := map[string]bool{}
	for _, e := range Catalog() {
		if ids[e.ID] {
			t.Errorf("duplicate id %s", e.ID)
		}
		ids[e.ID] = true
		hfOK := ValidRepo(e.Repo) && len(e.Revision) == 40
		urlOK := strings.HasPrefix(e.URL, "https://") && e.Repo == ""
		if !ValidID(e.ID) || !ValidKind(e.Kind) || !(hfOK || urlOK) || len(e.SHA256) != 64 || e.Size <= 0 {
			t.Errorf("bad entry %+v", e)
		}
		if e.Kind == "llm" {
			found := false
			for _, p := range KeptPromptProviders {
				found = found || p == e.PromptProvider
			}
			if !found {
				t.Errorf("%s: prompt provider %q is not kept (D11/D12)", e.ID, e.PromptProvider)
			}
			if e.ContextDefault == 0 || e.KVBytesPerTok == 0 {
				t.Errorf("%s: no context default / KV size", e.ID)
			}
		}
	}
	for _, e := range Catalog() {
		if e.MMProj != "" {
			p, ok := CatalogEntry(e.MMProj)
			if !ok || p.Kind != "mmproj" {
				t.Errorf("%s: projector %s missing", e.ID, e.MMProj)
			}
		}
	}
	for _, want := range []string{"qwen3-4b", "qwen3-8b", "qwen3-14b", "qwen3.5-9b", "qwen3.8-27b", "all-minilm-l6-v2", "whisper-large-v3-turbo",
		"kokoro-multi-lang-v1_0", "eres2net-voxceleb-16k"} {
		if !ids[want] {
			t.Errorf("catalog lacks %s", want)
		}
	}
	if !ids[engine.LabelDefs[2].DefaultModel] {
		t.Error("embeddings default model not in catalog")
	}
	for _, v := range VoiceLabels {
		if e, ok := CatalogEntry(v.DefaultModel); !ok || e.Kind != v.ModelKind {
			t.Errorf("voice default %s missing", v.DefaultModel)
		}
	}
}

func TestFitAndRecommend(t *testing.T) {
	two3090 := engine.Hardware{Flavour: engine.FlavourCUDA, Devices: []engine.Device{
		{Backend: engine.FlavourCUDA, Index: 0, Name: "RTX 3090", TotalMB: 24576, FreeMB: 24000},
		{Backend: engine.FlavourCUDA, Index: 1, Name: "RTX 3090", TotalMB: 24576, FreeMB: 24000},
	}}
	e27, _ := CatalogEntry("qwen3.8-27b")
	if f := EntryFit(two3090, e27); f.Verdict != "fits" || f.KVEstimate {
		t.Errorf("27B on a 3090: %+v", f)
	}
	// 131072 ctx f16 KV on one card: too much for one, fine across two.
	if f := FitFor(two3090, "llm", e27.Size, e27.KVBytesPerTok, 131072); f.Verdict != "split" {
		t.Errorf("27B @131k: %+v", f)
	}
	r := Recommend(two3090)
	if r["live"] != "qwen3.8-27b" || r["stt"] != "whisper-large-v3-turbo" || r["embeddings"] != "all-minilm-l6-v2" {
		t.Errorf("recommend %v", r)
	}
	card := func(mbs int64) engine.Hardware {
		return engine.Hardware{Flavour: engine.FlavourCUDA, Devices: []engine.Device{{Backend: engine.FlavourCUDA, Name: "GPU", TotalMB: mbs}}}
	}
	if r := Recommend(card(12288)); r["live"] != "qwen3.5-9b" {
		t.Errorf("12 GB: %v", r)
	}
	if r := Recommend(card(8192)); r["live"] != "qwen3-4b" {
		t.Errorf("8 GB: %v", r)
	}
	cpu := engine.Hardware{Flavour: engine.FlavourCPU}
	if r := Recommend(cpu); r["live"] != "qwen3-4b" || r["stt"] != "whisper-small.en" {
		t.Errorf("cpu: %v", r)
	}
	if f := FitFor(cpu, "llm", 1<<30, 0, 0); f.Verdict != "cpu" || !f.KVEstimate {
		t.Errorf("cpu fit %+v", f)
	}
}

// A10 F8: once a model is installed and assigned, catalog verdicts judged every model next
// to it, even one that would replace it ("Qwen 3 4B: Too big"), and the CPU-only embeddings
// model against a full card.
func TestCatalogFitExcludesTheLabelsItWouldReplace(t *testing.T) {
	e := newEnv(t)
	card := engine.Hardware{Flavour: engine.FlavourCUDA, Devices: []engine.Device{
		{Backend: engine.FlavourCUDA, Index: 0, ID: "CUDA0", Name: "RTX 3080 Ti", TotalMB: 12288}}}
	q9 := Resident{Labels: []string{"live", "background"}, Model: "qwen3.5-9b", NeededMB: 9500}
	stt := Resident{Labels: []string{"stt"}, Model: "whisper-small.en", NeededMB: 1100}
	desk := Resident{Labels: []string{OtherPrograms}, NeededMB: 600, Devices: []int{0}}
	rs := []Resident{q9, stt, desk}

	q4, _ := CatalogEntry("qwen3-4b")
	f := e.mgr.entryFit(e.ctx, card, q4, rs)
	if f.Verdict != "fits" || slices.Contains(f.Alongside, "live") || f.CommittedMB != stt.NeededMB+desk.NeededMB {
		t.Errorf("qwen3-4b replacing the live model: %+v", f)
	}
	// Next to the live model it would not fit: the old judgement.
	if f := EntryFit(card, q4); f.Verdict != "fits" {
		t.Fatalf("qwen3-4b alone: %+v", f)
	}
	if f := FitAlongside(card, q4.Kind, q4.Size, q4.KVBytesPerTok, q4.ContextDefault, rs); f.Verdict != "too_big" {
		t.Fatalf("test premise: qwen3-4b next to the 9B should be too big: %+v", f)
	}
	// A whisper model is judged without the current stt engine but next to the LLM.
	turbo, _ := CatalogEntry("whisper-large-v3-turbo")
	f = e.mgr.entryFit(e.ctx, card, turbo, rs)
	if slices.Contains(f.Alongside, "stt") || !slices.Contains(f.Alongside, "live") {
		t.Errorf("turbo replacing stt: %+v", f)
	}
	// Embeddings run on the CPU by default (gpu_layers 0): no VRAM verdict.
	emb, _ := CatalogEntry("all-minilm-l6-v2")
	if f := e.mgr.entryFit(e.ctx, card, emb, rs); f.Verdict != "cpu" || f.NeededMB == 0 {
		t.Errorf("embeddings on the CPU: %+v", f)
	}
	// Moved to the GPU, it is judged there.
	e.set.Set(e.ctx, "llm.embeddings.gpu_layers", int64(999), settings.Scope{})
	if f := e.mgr.entryFit(e.ctx, card, emb, rs); f.Verdict == "cpu" || f.Device == "" {
		t.Errorf("embeddings on the GPU: %+v", f)
	}
}

// The jarvis-dev incident: whisper large-v3-turbo "fit" a 12 GB card on its own, was
// recommended and assigned next to Qwen3-8B, and crash-looped on cudaMalloc.
func TestFitCountsCoResidentEngines(t *testing.T) {
	card := engine.Hardware{Flavour: engine.FlavourCUDA, Devices: []engine.Device{
		{Backend: engine.FlavourCUDA, Index: 0, ID: "CUDA0", Name: "RTX 3080 Ti", TotalMB: 12288}}}
	turbo, _ := CatalogEntry("whisper-large-v3-turbo")
	q8, _ := CatalogEntry("qwen3-8b")
	if f := EntryFit(card, turbo); f.Verdict != "fits" {
		t.Fatalf("turbo alone: %+v", f)
	}
	qwen := Resident{Labels: []string{"live", "background"}, Model: q8.ID, NeededMB: EntryFit(card, q8).NeededMB}
	// The desktop, Sunshine and the emulator held about 2.4 GB of that card.
	desk := Resident{Labels: []string{OtherPrograms}, NeededMB: 2400, Devices: []int{0}}
	f := FitAlongside(card, turbo.Kind, turbo.Size, 0, 0, []Resident{qwen, desk})
	if f.Verdict != "too_big" || f.CommittedMB != qwen.NeededMB+desk.NeededMB || !slices.Equal(f.Alongside, []string{"live", "background", OtherPrograms}) {
		t.Errorf("turbo next to qwen3-8b and the desktop: %+v", f)
	}
	if r := Recommend(card); r["stt"] != "whisper-small.en" {
		t.Errorf("12 GB recommends %v; turbo does not fit next to the live model", r)
	}
	// Two cards: the model goes where there is room.
	two := card
	two.Devices = append(slices.Clone(card.Devices), engine.Device{Backend: engine.FlavourCUDA, Index: 1, ID: "CUDA1", Name: "RTX 3060", TotalMB: 12288})
	if f := FitAlongside(two, turbo.Kind, turbo.Size, 0, 0, []Resident{qwen}); f.Verdict != "fits" || f.Device != "RTX 3060" || f.Alongside != nil {
		t.Errorf("second card: %+v", f)
	}
	if w := Overcommitted(card, []Resident{qwen, {Labels: []string{"stt"}, NeededMB: 9000}}); len(w) != 1 || !strings.Contains(w[0], "live, background, stt") {
		t.Errorf("overcommit warnings %q", w)
	}
	if w := Overcommitted(card, []Resident{qwen}); len(w) != 0 {
		t.Errorf("no overcommit: %q", w)
	}
}

func TestResidentsShareEnginesAndSkipCPU(t *testing.T) {
	e := newEnv(t)
	file := func(name string, size int64) string {
		p := filepath.Join(e.home, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := f.Truncate(size); err != nil {
			t.Fatal(err)
		}
		return p
	}
	llm, stt := file("m.gguf", 4<<30), file("w.bin", 1<<30)
	cfg := func(label, model string, mod func(*engine.LabelConfig)) engine.LabelStatus {
		d, _ := engine.LabelDefFor(label)
		c := engine.LabelConfig{Label: label, Kind: d.Kind, ModelKind: d.ModelKind, Engine: engine.ModeLocal, Model: model, Context: 8192, GPULayers: 999}
		if mod != nil {
			mod(&c)
		}
		return engine.LabelStatus{Label: label, Config: c}
	}
	e.labels.status = []engine.LabelStatus{
		cfg("live", llm, nil),
		cfg("background", llm, nil),
		cfg("embeddings", llm, func(c *engine.LabelConfig) { c.GPULayers = 0 }),
		cfg("stt", stt, func(c *engine.LabelConfig) { c.GPUDevices = "1" }),
	}
	rs := e.mgr.Residents(e.ctx)
	if len(rs) != 2 || !slices.Equal(rs[0].Labels, []string{"live", "background"}) || !slices.Equal(rs[1].Devices, []int{1}) {
		t.Fatalf("residents %+v", rs)
	}
	if got := without(rs, "live", "background"); len(got) != 1 || got[0].Labels[0] != "stt" {
		t.Errorf("without: %+v", got)
	}
	// Card memory in use at detection that our running engines don't explain is someone
	// else's: the 4 GB live/background engine was running, so 6 GB used leaves ~2 GB other.
	e.mgr.Detector = &engine.Detector{Platform: engine.Platform{OS: "linux", Arch: "amd64"},
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "nvidia-smi" {
				return []byte("0, NVIDIA GeForce RTX 3080 Ti, 12288, 6288\n1, NVIDIA GeForce RTX 3060, 12288, 12100\n"), nil
			}
			return nil, io.EOF
		}}
	linux := engine.Platform{OS: "linux", Arch: "amd64"}
	rel := engine.Releases[engine.KindLlama]
	saved := rel.Assets
	rel.Assets = map[engine.Platform]map[engine.Flavour][]engine.Asset{linux: {engine.FlavourCUDA: {{Name: "x"}}}}
	engine.Releases[engine.KindLlama] = rel
	t.Cleanup(func() { rel.Assets = saved; engine.Releases[engine.KindLlama] = rel })
	hw := e.mgr.Detector.Hardware(e.ctx, true)
	e.labels.instances = []engine.InstanceStatus{{State: engines.Healthy, Since: hw.DetectedAt.Add(-time.Minute), Labels: []string{"live", "background"}}}
	rs = e.mgr.Residents(e.ctx)
	other := rs[len(rs)-1]
	if len(rs) != 3 || other.Labels[0] != OtherPrograms || other.Devices[0] != 0 || other.NeededMB != 6000-rs[0].NeededMB {
		t.Fatalf("other programs: %+v", rs)
	}
	// Not running at detection: all 6 GB counts as other programs (conservative).
	e.labels.instances[0].Since = hw.DetectedAt.Add(time.Minute)
	if rs := e.mgr.Residents(e.ctx); rs[len(rs)-1].NeededMB != 6000 {
		t.Errorf("engine started after detection: %+v", rs)
	}
	// A10 F8: launched before detection but still loading then (it turned healthy after):
	// its memory is its own, not other programs' as well.
	for _, st := range []engines.State{engines.Starting, engines.Healthy} {
		e.labels.instances[0].State = st
		e.labels.instances[0].Started = hw.DetectedAt.Add(-time.Minute)
		if rs := e.mgr.Residents(e.ctx); rs[len(rs)-1].NeededMB != 6000-rs[0].NeededMB {
			t.Errorf("%s engine launched before detection counted twice: %+v", st, rs)
		}
	}
	e.labels.instances, e.mgr.Detector = nil, nil

	// A different context is a different engine.
	e.labels.status[1] = cfg("background", llm, func(c *engine.LabelConfig) { c.Context = 32768 })
	if rs := e.mgr.Residents(e.ctx); len(rs) != 3 || rs[1].NeededMB <= rs[0].NeededMB {
		t.Errorf("unshared background: %+v", rs)
	}
}

func TestChoices(t *testing.T) {
	r := Repo{ID: "unsloth/Big-GGUF", Files: []RepoFile{
		{Name: "BF16/Big-BF16-00002-of-00002.gguf", Size: 20},
		{Name: "BF16/Big-BF16-00001-of-00002.gguf", Size: 30},
		{Name: "Big-UD-Q4_K_XL.gguf", Size: 10},
		{Name: "Big-IQ4_XS.gguf", Size: 9},
		{Name: "mmproj-F16.gguf", Size: 2},
		{Name: "README.md", Size: 1},
		{Name: "ggml-tiny.en-q5_1.bin", Size: 3},
	}}
	cs := Choices(r)
	got := []string{}
	for _, c := range cs {
		got = append(got, fmt.Sprintf("%s/%s/%s/%d/%d", c.File, c.Kind, c.Quant, c.Size, len(c.Shards)))
	}
	want := []string{
		"mmproj-F16.gguf/mmproj/F16/2/1",
		"ggml-tiny.en-q5_1.bin/stt/Q5_1/3/1",
		"Big-IQ4_XS.gguf/llm/IQ4_XS/9/1",
		"Big-UD-Q4_K_XL.gguf/llm/UD-Q4_K_XL/10/1",
		"BF16/Big-BF16-00001-of-00002.gguf/llm/BF16/50/2",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("choices\n got %v\nwant %v", got, want)
	}
	if s, err := shardSet(r, "BF16/Big-BF16-00002-of-00002.gguf"); err != nil || len(s) != 2 || s[0].Name != "BF16/Big-BF16-00001-of-00002.gguf" {
		t.Fatalf("shards %v %v", s, err)
	}
	if Choices(Repo{ID: "second-state/All-MiniLM-L6-v2-Embedding-GGUF", Files: []RepoFile{{Name: "m-f16.gguf"}}})[0].Kind != "embedding" {
		t.Error("embedding repo")
	}
	for in, want := range map[string]string{"Qwen3-8B-Q4_K_M.gguf": "Q4_K_M", "x-q8_0.gguf": "Q8_0", "model.gguf": "", "Qwen3.8-27B-UD-Q4_K_M.gguf": "UD-Q4_K_M"} {
		if got := Quant(in); got != want {
			t.Errorf("Quant(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestHFRepo(t *testing.T) {
	e := newEnv(t)
	e.hub.addRepo("acme/Open-GGUF", "abc", map[string][]byte{"a.gguf": blob(10, 0)})
	e.hub.addRepo("acme/Gated", "def", map[string][]byte{"g.gguf": blob(10, 0)})
	e.hub.gated["acme/Gated"], e.hub.token = true, "hf_secret"
	r, err := e.mgr.HF.Repo(e.ctx, "acme/Open-GGUF", "")
	if err != nil || r.Revision != "abc" || len(r.Files) != 2 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := e.mgr.HF.Repo(e.ctx, "acme/Gated", ""); err != ErrGated {
		t.Fatalf("gated: %v", err)
	}
	e.set.Set(e.ctx, engine.KeyHFToken, "hf_secret", settings.Scope{})
	if _, err := e.mgr.HF.Repo(e.ctx, "acme/Gated", "main"); err != nil {
		t.Fatalf("with token: %v", err)
	}
	if _, err := e.mgr.HF.Repo(e.ctx, "acme/missing", ""); err != ErrRepoNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := e.mgr.HF.Repo(e.ctx, "../etc", ""); err == nil {
		t.Fatal("bad repo accepted")
	}
	if u := e.mgr.HF.FileURL("a/b", "", "dir/x y.gguf"); u != e.srv.URL+"/a/b/resolve/main/dir/x%20y.gguf" {
		t.Fatal(u)
	}
}

func TestInstallCatalogWithProjectorEngineAndAssign(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	// The queue starts after the checks on the queued install, so "queued" is deterministic.
	inst, existing, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", Assign: []string{"live", "background"}})
	if err != nil || existing {
		t.Fatal(err)
	}
	if inst.MMProjID != "tiny-mmproj" || inst.EngineKind != "llama-server" || inst.EngineFlavour != "cpu" || inst.State != InstallQueued || inst.JobID == 0 {
		t.Fatalf("%+v", inst)
	}
	engSize := engine.AssetsSize(engine.KindLlama, engine.Host(), engine.FlavourCPU)
	if inst.BytesTotal != 300<<10+100<<10+engSize {
		t.Fatalf("total %d", inst.BytesTotal)
	}
	// A second request while it is pending returns the same install.
	again, existing, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny"})
	if err != nil || !existing || again.ID != inst.ID {
		t.Fatalf("dedup: %+v %v %v", again, existing, err)
	}
	e.start()
	done := e.waitInstall(inst.ID, InstallDone)
	if done.BytesDone != done.BytesTotal || done.Phase != "done" {
		t.Fatalf("%+v", done)
	}
	m, err := e.mgr.Store.Get(e.ctx, "tiny")
	if err != nil || m.State != StateReady || m.MMProjID != "tiny-mmproj" || m.ContextDefault != 2048 || m.PromptProvider != "Qwen3_8B_Compressed" {
		t.Fatalf("%+v %v", m, err)
	}
	if m.Path != filepath.Join(e.home, "models", "acme--Tiny-GGUF", "Tiny-Q4_K_M.gguf") || !fileExists(m.Path) {
		t.Fatalf("path %s", m.Path)
	}
	if _, ok := e.mgr.Binaries.Path(engine.KindLlama, engine.FlavourCPU); !ok {
		t.Fatal("engine not fetched with the model")
	}
	for _, l := range []string{"llm.live.model", "llm.background.model"} {
		if v := e.set.String(e.ctx, l, settings.Scope{}); v != "tiny" {
			t.Fatalf("%s = %q", l, v)
		}
	}
	// The resolver's source sees it: model path, projector, catalog context.
	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store}
	c, err := src.Label(e.ctx, "live")
	if err != nil || c.ModelPath != m.Path || !strings.HasSuffix(c.MMProjPath, "mmproj-F16.gguf") || c.Context != 2048 || c.Problem != "" {
		t.Fatalf("%+v %v", c, err)
	}
	e.labels.mu.Lock()
	n := e.labels.notified
	e.labels.mu.Unlock()
	if n == 0 {
		t.Fatal("resolver not notified")
	}
	// Re-installing a ready model only runs a no-op job.
	re, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny"})
	if err != nil || re.BytesTotal != 0 {
		t.Fatalf("reinstall %+v %v", re, err)
	}
	e.waitInstall(re.ID, InstallDone)
	if e.hub.hits["/acme/Tiny-GGUF/resolve/rev1/Tiny-Q4_K_M.gguf"] != 1 {
		t.Fatal("downloaded twice")
	}
}

// A10 F7: a small install doesn't wait behind a big one.
func TestSmallInstallOvertakesBigOne(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.mgr.SmallInstallBytes = 200 << 10
	release := make(chan struct{})
	e.hub.hold["/acme/Tiny-GGUF/resolve/rev1/Tiny-Q4_K_M.gguf"] = release
	e.start()
	big, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", WithMMProj: new(bool)})
	if err != nil {
		t.Fatal(err)
	}
	small, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "whisper-tiny"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		inst Install
		lane string
	}{{big, InstallJobType}, {small, InstallSmallJobType}} {
		info, err := e.q.Get(e.ctx, c.inst.JobID)
		if err != nil || info.Type != c.lane {
			t.Fatalf("install %s: job %+v %v, want lane %s", c.inst.ModelID, info, err, c.lane)
		}
	}
	e.waitInstall(big.ID, InstallRunning)
	// The big install is held mid-download: the small one only finishes in its own lane.
	e.waitInstall(small.ID, InstallDone)
	if b, _ := e.mgr.Store.GetInstall(e.ctx, big.ID); b.State != InstallRunning {
		t.Fatalf("big install %s while the small one finished", b.State)
	}
	close(release)
	e.waitInstall(big.ID, InstallDone)
}

// A10b R8: the small lane runs two installs at once, so a voice model doesn't wait behind a
// slow engine download in the same lane.
func TestSmallLaneRunsTwoAtOnce(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.mgr.SmallInstallBytes = 1 << 30 // both installs go to the small lane
	release := make(chan struct{})
	// The whisper install holds mid-download, in its engine archive.
	e.hub.hold["/whisper/"+engine.Releases[engine.KindWhisper].Tag()+"/eng.tar.gz"] = release
	e.start()
	slow, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "whisper-tiny"})
	if err != nil {
		t.Fatal(err)
	}
	e.waitInstall(slow.ID, InstallRunning)
	fast, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", WithMMProj: new(bool)})
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []Install{slow, fast} {
		if info, err := e.q.Get(e.ctx, inst.JobID); err != nil || info.Type != InstallSmallJobType {
			t.Fatalf("install %s: job %+v %v, want the small lane", inst.ModelID, info, err)
		}
	}
	e.waitInstall(fast.ID, InstallDone)
	if s, _ := e.mgr.Store.GetInstall(e.ctx, slow.ID); s.State != InstallRunning {
		t.Fatalf("held install %s while the other finished", s.State)
	}
	close(release)
	e.waitInstall(slow.ID, InstallDone)
}

func TestInstallRepoShardsResumeAndSlices(t *testing.T) {
	e := newEnv(t)
	s1, s2, proj := blob(400<<10, 4), blob(200<<10, 5), blob(64<<10, 6)
	e.hub.addRepo("acme/Split-GGUF", "r9", map[string][]byte{
		"Q8/Split-Q8_0-00001-of-00002.gguf": s1, "Q8/Split-Q8_0-00002-of-00002.gguf": s2, "mmproj-BF16.gguf": proj})
	e.hub.cutOnce["/acme/Split-GGUF/resolve/r9/Q8/Split-Q8_0-00001-of-00002.gguf"] = true
	e.hub.throttle = 5 * time.Millisecond // ~6 MB/s: the 600 KB model spans several slices
	e.mgr.SliceDuration = 40 * time.Millisecond
	e.start()
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Split-GGUF", File: "Q8/Split-Q8_0-00002-of-00002.gguf",
		MMProjFile: "mmproj-BF16.gguf", GPUBackend: "cpu", Assign: []string{"background"}})
	if err != nil {
		t.Fatal(err)
	}
	if inst.ModelID != "split-q8_0" || inst.MMProjID != "split-mmproj-bf16" {
		t.Fatalf("%+v", inst)
	}
	firstJob := inst.JobID
	done := e.waitInstall(inst.ID, InstallDone)
	if done.JobID == firstJob {
		t.Fatal("expected continuation jobs")
	}
	m, _ := e.mgr.Store.Get(e.ctx, "split-q8_0")
	if len(m.Files) != 2 || m.Size != int64(len(s1)+len(s2)) || !strings.HasSuffix(m.Path, filepath.FromSlash("Q8/Split-Q8_0-00001-of-00002.gguf")) {
		t.Fatalf("%+v", m)
	}
	for _, f := range []string{"Q8/Split-Q8_0-00001-of-00002.gguf", "Q8/Split-Q8_0-00002-of-00002.gguf"} {
		if !fileExists(filepath.Join(e.home, "models", "acme--Split-GGUF", filepath.FromSlash(f))) {
			t.Fatal("missing shard", f)
		}
	}
	if _, err := os.Stat(filepath.Join(e.home, "models", ".partial", "split-q8_0")); !os.IsNotExist(err) {
		t.Fatal("partials left")
	}
	if e.set.String(e.ctx, "llm.background.model", settings.Scope{}) != "split-q8_0" {
		t.Fatal("not assigned")
	}
}

func TestInstallFailures(t *testing.T) {
	e := newEnv(t)
	e.hub.addRepo("acme/Gone-GGUF", "r1", map[string][]byte{"gone.gguf": blob(1000, 1)})
	delete(e.hub.files, "/acme/Gone-GGUF/resolve/r1/gone.gguf")
	e.hub.addRepo("acme/Gated-GGUF", "r1", map[string][]byte{"g.gguf": blob(10, 1)})
	e.hub.gated["acme/Gated-GGUF"] = true
	e.start()

	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{Repo: "acme/Gone-GGUF", File: "gone.gguf", GPUBackend: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	failed := e.waitInstall(inst.ID, InstallFailed)
	if !strings.Contains(failed.Error, "404") {
		t.Fatalf("error %q", failed.Error)
	}
	if m, _ := e.mgr.Store.Get(e.ctx, "gone"); m.State != StateFailed {
		t.Fatalf("model state %s", m.State)
	}

	check := func(req InstallRequest, status int) {
		t.Helper()
		_, _, err := e.mgr.Install(e.ctx, req)
		re, ok := err.(*RequestError)
		if !ok || re.Status != status {
			t.Fatalf("%+v: err %v, want %d", req, err, status)
		}
	}
	check(InstallRequest{Repo: "acme/Gated-GGUF", File: "g.gguf"}, http.StatusForbidden)
	check(InstallRequest{Repo: "acme/Nope", File: "x.gguf"}, http.StatusNotFound)
	check(InstallRequest{Repo: "acme/Gone-GGUF", File: "other.gguf"}, http.StatusUnprocessableEntity)
	check(InstallRequest{CatalogID: "no-such"}, http.StatusNotFound)
	check(InstallRequest{}, http.StatusUnprocessableEntity)
	check(InstallRequest{Repo: "acme/Gone-GGUF", File: "gone.gguf", Assign: []string{"stt"}}, http.StatusUnprocessableEntity)
	check(InstallRequest{Repo: "acme/Gone-GGUF", File: "gone.gguf", Assign: []string{"nope"}}, http.StatusUnprocessableEntity)
	check(InstallRequest{Repo: "acme/Gone-GGUF", File: "gone.gguf", GPUBackend: "opencl"}, http.StatusUnprocessableEntity)
}

func TestInstallNoEngineBuild(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	engine.Releases[engine.KindWhisper] = engine.Release{Kind: engine.KindWhisper, Build: "b0"} // nothing for this host
	e.start()
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "whisper-tiny", Assign: []string{"stt"}})
	if err != nil {
		t.Fatal(err)
	}
	if inst.EngineFlavour != "" || !strings.Contains(inst.Note, "stt.engine_path") {
		t.Fatalf("%+v", inst)
	}
	e.waitInstall(inst.ID, InstallDone)
	if e.set.String(e.ctx, "stt.model", settings.Scope{}) != "whisper-tiny" {
		t.Fatal("not assigned")
	}
}

func TestCancelInstall(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.hub.throttle = 20 * time.Millisecond
	e.start()
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", GPUBackend: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until bytes are flowing for the model.
	deadline := time.Now().Add(10 * time.Second)
	for {
		i, _ := e.mgr.Store.GetInstall(e.ctx, inst.ID)
		if i.Phase == "model" || i.Phase == "mmproj" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never started: %+v", i)
		}
		time.Sleep(5 * time.Millisecond)
	}
	c, err := e.mgr.Cancel(e.ctx, inst.ID)
	if err != nil || c.State != InstallCancelled {
		t.Fatalf("%+v %v", c, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		_, err1 := e.mgr.Store.Get(e.ctx, "tiny")
		_, err2 := os.Stat(filepath.Join(e.home, "models", ".partial", "tiny"))
		if err1 == ErrNotFound && os.IsNotExist(err2) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not cleaned up: %v %v", err1, err2)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.mgr.Cancel(e.ctx, inst.ID); err == nil {
		t.Fatal("cancel twice")
	}
}

func TestAssignDeleteRegister(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.start()
	inst, _, _ := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", Assign: []string{"live"}})
	e.waitInstall(inst.ID, InstallDone)

	err := e.mgr.Delete(e.ctx, "tiny", false)
	if re, ok := err.(*RequestError); !ok || re.Status != http.StatusConflict {
		t.Fatalf("in use: %v", err)
	}
	if err := e.mgr.Assign(e.ctx, "stt", "tiny"); err == nil {
		t.Fatal("llm assigned to stt")
	}
	path := filepath.Join(e.home, "models", "acme--Tiny-GGUF", "Tiny-Q4_K_M.gguf")
	if err := e.mgr.Delete(e.ctx, "tiny", true); err != nil {
		t.Fatal(err)
	}
	if fileExists(path) || e.set.String(e.ctx, "llm.live.model", settings.Scope{}) != "" {
		t.Fatal("not deleted / label not cleared")
	}
	// The projector is still installed; then delete it too: the repo dir goes.
	if err := e.mgr.Delete(e.ctx, "tiny-mmproj", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.home, "models", "acme--Tiny-GGUF")); !os.IsNotExist(err) {
		t.Fatal("empty repo dir kept")
	}

	// Register a file in place, assign it, delete it: the file stays.
	ext := filepath.Join(t.TempDir(), "My-Model.Q4_K_M.gguf")
	os.WriteFile(ext, []byte("gguf"), 0o644)
	m, err := e.mgr.Register(e.ctx, RegisterRequest{Path: ext, ContextDefault: 4096})
	if err != nil || m.ID != "my-model.q4_k_m" || !m.External || m.State != StateReady {
		t.Fatalf("%+v %v", m, err)
	}
	if err := e.mgr.Assign(e.ctx, "live", m.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Delete(e.ctx, m.ID, true); err != nil || !fileExists(ext) {
		t.Fatal("registered file removed", err)
	}
	if _, err := e.mgr.Register(e.ctx, RegisterRequest{Path: "relative.gguf"}); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestUpdateLabelsValidation(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.start()
	inst, _, _ := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "whisper-tiny"})
	e.waitInstall(inst.ID, InstallDone)
	ok := []map[string]map[string]any{
		{"live": {"context": float64(8192), "parallel": float64(4), "gpu_backend": "cuda", "gpu_devices": "0,1", "kv_cache_type": "q8_0",
			"flash_attn": "on", "extra_args": `--chat-template-kwargs '{"enable_thinking":false}'`}},
		{"background": {"engine": "shared"}},
		{"live": {"engine": "remote", "remote_url": "https://api.openai.com/v1", "remote_model": "gpt-4.1-nano", "remote_api_key": "sk"}},
		{"stt": {"model": "whisper-tiny", "gpu_layers": float64(0)}},
		{"live": {"mmproj": "none"}},
	}
	for _, u := range ok {
		if err := e.mgr.UpdateLabels(e.ctx, u); err != nil {
			t.Errorf("%v: %v", u, err)
		}
	}
	bad := []map[string]map[string]any{
		{"nope": {"model": "x"}},
		{"live": {"bogus": 1.0}},
		{"live": {"context": "big"}},
		{"live": {"context": 1.5}},
		{"live": {"parallel": -2.0}},
		{"live": {"engine": "shared"}},
		{"live": {"gpu_backend": "opencl"}},
		{"live": {"model": "whisper-tiny"}},
		{"live": {"model": "missing"}},
		{"live": {"model": "/no/such/file.gguf"}},
		{"live": {"mmproj": "whisper-tiny"}},
		{"live": {"extra_args": "'open"}},
		{"live": {"split_mode": "diagonal"}},
		{"stt": {"remote_url": "http://x"}},
	}
	for _, u := range bad {
		if err := e.mgr.UpdateLabels(e.ctx, u); err == nil {
			t.Errorf("%v accepted", u)
		}
	}
	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store}
	c, _ := src.Label(e.ctx, "background")
	if c.Engine != "remote" || c.SharedWith != "live" || c.RemoteModel != "gpt-4.1-nano" {
		t.Fatalf("shared background: %+v", c)
	}
}

func TestSettingsSourceDefaults(t *testing.T) {
	e := newEnv(t)
	src := engine.SettingsSource{Settings: e.set, Models: e.mgr.Store}
	c, err := src.Label(e.ctx, "embeddings")
	if err != nil || c.Engine != "local" || c.Model != "" || c.GPULayers != 0 || c.Context != 512 || !c.Embedding || c.Parallel != 4 {
		t.Fatalf("%+v %v", c, err)
	}
	// Once MiniLM is installed, embeddings use it without any setting.
	p := filepath.Join(t.TempDir(), "minilm.gguf")
	os.WriteFile(p, []byte("x"), 0o644)
	e.mgr.Store.Upsert(e.ctx, Model{ID: "all-minilm-l6-v2", Kind: "embedding", Path: p, State: StateReady, Files: []File{{Name: "minilm.gguf"}}})
	if c, _ := src.Label(e.ctx, "embeddings"); c.ModelPath != p || c.Model != "all-minilm-l6-v2" {
		t.Fatalf("%+v", c)
	}
	live, _ := src.Label(e.ctx, "live")
	if live.GPULayers != 999 || live.GPUBackend != "auto" || live.Context != 0 {
		t.Fatalf("live defaults %+v", live)
	}
	e.set.Set(e.ctx, "llm.live.model", "missing", settings.Scope{})
	if c, _ := src.Label(e.ctx, "live"); !strings.Contains(c.Problem, "not installed") {
		t.Fatalf("%+v", c)
	}
	e.set.Set(e.ctx, "llm.live.model", p, settings.Scope{})
	e.set.Set(e.ctx, "llm.live.mmproj", "/no/proj.gguf", settings.Scope{})
	if c, _ := src.Label(e.ctx, "live"); c.ModelPath != p || c.Context != 8192 || !strings.Contains(c.Problem, "mmproj") {
		t.Fatalf("%+v", c)
	}
	if _, err := src.Label(e.ctx, "other"); err == nil {
		t.Fatal("unknown label")
	}
	defs := engine.SettingDefinitions()
	keys := map[string]bool{}
	for _, d := range defs {
		if keys[d.Key] {
			t.Errorf("duplicate %s", d.Key)
		}
		keys[d.Key] = true
	}
	for _, k := range []string{"llm.live.model", "llm.background.engine", "llm.embeddings.model", "stt.model", "stt.gpu_devices", "llm.hf_token"} {
		if !keys[k] {
			t.Errorf("missing %s", k)
		}
	}
	if keys["stt.mmproj"] || keys["stt.remote_url"] || keys["llm.embeddings.mmproj"] {
		t.Error("label got fields that don't apply")
	}
}

// TestStackEndToEnd: install → assign → the resolver runs the engine, for an LLM label and the
// stt label alike, through NewStack (the wiring the llm module uses).
func TestStackEndToEnd(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	t.Setenv("FAKE_ENGINE", "1")
	e.set.Set(e.ctx, engine.KeyLlamaEnginePath, os.Args[0], settings.Scope{})
	e.set.Set(e.ctx, engine.KeyWhisperPath, os.Args[0], settings.Scope{})
	st := NewStack(e.deps, e.set)
	st.Detector.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, io.EOF }
	st.Resolver.HealthInterval = 20 * time.Millisecond
	st.Resolver.Interval = 50 * time.Millisecond
	st.Manager.ProgressEvery = time.Millisecond
	mux := http.NewServeMux()
	allow := func(http.ResponseWriter, *http.Request) bool { return true }
	st.Mount(mux, allow)
	e.q.Start(e.ctx)
	if err := st.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Resolver.StopAll(context.Background()) })
	api := httptest.NewServer(mux)
	defer api.Close()

	call := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if _, err := st.Resolver.Resolve(e.ctx, "live"); err != engine.ErrNotConfigured {
		t.Fatalf("fresh install: %v", err)
	}
	for _, body := range []string{`{"catalog_id":"tiny","assign":["live","background"]}`, `{"catalog_id":"whisper-tiny","assign":["stt"]}`} {
		code, out := call("POST", "/v1/models/install", body)
		if code != http.StatusAccepted {
			t.Fatalf("install %d %v", code, out)
		}
		id := int64(out["install"].(map[string]any)["id"].(float64))
		e.waitInstall(id, InstallDone)
	}
	deadline := time.Now().Add(10 * time.Second)
	var live, bg, stt engine.Endpoint
	for {
		var err1, err2, err3 error
		live, err1 = st.Resolver.Resolve(e.ctx, "live")
		bg, err2 = st.Resolver.Resolve(e.ctx, "background")
		stt, err3 = st.Resolver.Resolve(e.ctx, "stt")
		if err1 == nil && err2 == nil && err3 == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %v / %v / %v", err1, err2, err3)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if live.Engine != bg.Engine || !live.Vision || live.ContextLength != 2048 || stt.Kind != engine.KindWhisper || stt.Engine == live.Engine {
		t.Fatalf("live %+v bg %+v stt %+v", live, bg, stt)
	}
	code, out := call("GET", "/v1/models/labels", "")
	if code != 200 || len(out["labels"].([]any)) != 4 || len(out["engines"].([]any)) != 2 {
		t.Fatalf("labels %d %v", code, out)
	}
	code, out = call("GET", "/v1/models/installed", "")
	if code != 200 || len(out["models"].([]any)) != 3 {
		t.Fatalf("installed %d %v", code, out)
	}
	code, out = call("GET", "/v1/models/catalog", "")
	if code != 200 || len(out["models"].([]any)) != 3 {
		t.Fatalf("catalog %d %v", code, out)
	}
	code, out = call("GET", "/v1/models/hf/acme/Tiny-GGUF", "")
	if code != 200 || out["revision"] != "rev1" || len(out["files"].([]any)) != 2 {
		t.Fatalf("hf %d %v", code, out)
	}
	if code, _ = call("GET", "/v1/models/hf/acme/none", ""); code != 404 {
		t.Fatalf("hf missing %d", code)
	}
	code, out = call("GET", "/v1/hardware", "")
	if code != 200 || out["hardware"] == nil || out["builds"] == nil {
		t.Fatalf("hardware %d %v", code, out)
	}
	// Turn background to its own context: a second LLM engine; then delete the model with force.
	code, _ = call("PUT", "/v1/models/labels", `{"background":{"context":4096}}`)
	if code != 200 {
		t.Fatalf("put labels %d", code)
	}
	if code, _ = call("PUT", "/v1/models/labels", `{"background":{"context":"x"}}`); code != 422 {
		t.Fatalf("bad put %d", code)
	}
	if code, _ = call("DELETE", "/v1/models/installed/tiny", ""); code != 409 {
		t.Fatalf("delete in use %d", code)
	}
	if code, _ = call("DELETE", "/v1/models/installed/tiny?force=true", ""); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if _, err := st.Resolver.Resolve(e.ctx, "live"); err != engine.ErrNotConfigured {
		t.Fatalf("after delete: %v", err)
	}
	if code, _ = call("GET", "/v1/models/installs/999", ""); code != 404 {
		t.Fatalf("missing install %d", code)
	}

	// Guards apply to every route.
	deny := http.NewServeMux()
	st.Mount(deny, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	})
	rec := httptest.NewRecorder()
	deny.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models/catalog", nil))
	if rec.Code != 401 {
		t.Fatalf("guard %d", rec.Code)
	}
	none := http.NewServeMux()
	(&API{Manager: st.Manager, Resolver: st.Resolver}).Mount(none, nil)
	rec = httptest.NewRecorder()
	none.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/hardware", nil))
	if rec.Code != 403 {
		t.Fatalf("nil guard %d", rec.Code)
	}
}

// In-binary voice models: a directory-shaped TTS model (archive) and a single-file speaker
// model, both from direct URLs, installed, resolved for the voice module, and deleted.
func TestVoiceModels(t *testing.T) {
	e := newEnv(t)
	kokoro := tarGz(t, map[string]string{
		"kokoro-x/model.onnx": "onnx", "kokoro-x/voices.bin": "v", "kokoro-x/tokens.txt": "t",
		"kokoro-x/espeak-ng-data/phontab": "p",
	})
	spk := blob(70<<10, 9)
	e.hub.files["/sherpa/kokoro-x.tar.gz"] = kokoro
	e.hub.files["/sherpa/spk.onnx"] = spk
	cat := []Entry{
		{ID: "kokoro-x", Kind: KindTTS, URL: e.srv.URL + "/sherpa/kokoro-x.tar.gz", Archive: "tar.gz", File: "kokoro-x.tar.gz",
			Size: int64(len(kokoro)), SHA256: sum(kokoro)},
		{ID: "spk", Kind: KindSpeaker, URL: e.srv.URL + "/sherpa/spk.onnx", File: "spk.onnx", Size: int64(len(spk)), SHA256: sum(spk)},
	}
	b, _ := json.Marshal(cat)
	old := catalogData
	catalogData = b
	t.Cleanup(func() { catalogData = old })
	e.start()

	if _, ok := e.mgr.ModelPath(e.ctx, "tts"); ok {
		t.Fatal("tts before install")
	}
	ti, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "kokoro-x", Assign: []string{"tts"}})
	if err != nil || ti.EngineKind != "" || ti.EngineFlavour != "" {
		t.Fatalf("%+v %v", ti, err)
	}
	si, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "spk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "spk", Assign: []string{"live"}}); err == nil {
		t.Fatal("speaker model assigned to an LLM label")
	}
	e.waitInstall(ti.ID, InstallDone)
	e.waitInstall(si.ID, InstallDone)

	tts, ok := e.mgr.ModelPath(e.ctx, "tts")
	dir := filepath.Join(e.home, "models", "kokoro-x")
	if !ok || tts.Path != dir || tts.ID != "kokoro-x" || !fileExists(filepath.Join(dir, "model.onnx")) || !fileExists(filepath.Join(dir, "espeak-ng-data", "phontab")) {
		t.Fatalf("tts %+v", tts)
	}
	if e.set.String(e.ctx, "tts.model", settings.Scope{}) != "kokoro-x" {
		t.Fatal("tts not assigned")
	}
	// speaker: unassigned, found as the only installed speaker model.
	sp, ok := e.mgr.ModelPath(e.ctx, "speaker")
	if !ok || sp.Path != filepath.Join(e.home, "models", "spk", "spk.onnx") {
		t.Fatalf("speaker %+v", sp)
	}
	if _, err := os.Stat(filepath.Join(e.home, "models", ".partial", "kokoro-x")); !os.IsNotExist(err) {
		t.Fatal("archive left behind")
	}
	if st := e.mgr.VoiceStatus(e.ctx); len(st) != 2 || st[0].Problem != "" || st[1].Problem != "" {
		t.Fatalf("voice status %+v", st)
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"speaker": {"model": "kokoro-x"}}); err == nil {
		t.Fatal("tts model accepted for speaker")
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"tts": {"gpu_devices": "0"}}); err == nil {
		t.Fatal("voice label took an engine field")
	}
	// An absolute tts path is the Kokoro directory (what ModelPath hands sherpa); a file inside
	// it is refused, since synthesis would look for model*.onnx under it.
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"tts": {"model": dir}}); err != nil {
		t.Fatalf("tts directory path refused: %v", err)
	}
	if tts, ok := e.mgr.ModelPath(e.ctx, "tts"); !ok || tts.Path != dir {
		t.Fatalf("tts by path %+v", tts)
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"tts": {"model": filepath.Join(dir, "model.onnx")}}); err == nil {
		t.Fatal("tts file path accepted")
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"speaker": {"model": dir}}); err == nil {
		t.Fatal("speaker directory path accepted")
	}
	if err := e.mgr.UpdateLabels(e.ctx, map[string]map[string]any{"tts": {"model": "kokoro-x"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.mgr.Delete(e.ctx, "kokoro-x", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("tts directory kept")
	}
	if _, ok := e.mgr.ModelPath(e.ctx, "tts"); ok {
		t.Fatal("tts after delete")
	}
	if f := FitFor(engine.Hardware{}, KindTTS, 300<<20, 0, 0); f.Verdict != "in_binary" {
		t.Fatalf("%+v", f)
	}
}

// An explicit gpu_backend on an install applies to the labels it assigns, so they run the
// build that was fetched instead of their auto-detected flavour (found in an end-to-end run:
// a cpu install left the labels on cuda and pulled the CUDA build too).
func TestInstallExplicitBackendAppliesToLabels(t *testing.T) {
	e := newEnv(t)
	e.testCatalog()
	e.start()
	inst, _, err := e.mgr.Install(e.ctx, InstallRequest{CatalogID: "tiny", Assign: []string{"live"}, GPUBackend: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	if v := e.set.String(e.ctx, "llm.live.gpu_backend", settings.Scope{}); v != "cpu" {
		t.Fatalf("llm.live.gpu_backend = %q, want cpu", v)
	}
	if v := e.set.String(e.ctx, "llm.background.gpu_backend", settings.Scope{}); v == "cpu" {
		t.Fatal("an unassigned label was changed")
	}
	e.waitInstall(inst.ID, InstallDone)
}
