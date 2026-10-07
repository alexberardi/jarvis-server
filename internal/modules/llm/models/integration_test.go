//go:build llamaserver

package models

// Real-engine integration test: downloads the pinned llama.cpp and whisper.cpp CPU builds (the
// whisper ones from this repo's engines-whisper-<build> release) and tiny models from GitHub and
// Hugging Face, installs them through the model manager, and runs a completion, an embedding
// and a transcription of whisper.cpp's jfk.wav through Resolve.
//
//	JARVIS_ENGINE_TEST_HOME=/some/cache go test -tags llamaserver -run TestRealEngines -v ./internal/modules/llm/models/
//
// JARVIS_ENGINE_TEST_HOME keeps downloads between runs (default: a temp dir).
// JARVIS_ENGINE_TEST_GPU=1 also runs live and stt on the detected GPU flavour's builds.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestRealEngines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	home := os.Getenv("JARVIS_ENGINE_TEST_HOME")
	if home == "" {
		home = t.TempDir()
	}
	os.Remove(filepath.Join(home, "jarvis.db"))
	os.Remove(filepath.Join(home, "jarvis.db-wal"))
	os.Remove(filepath.Join(home, "jarvis.db-shm"))
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()))
	must(db.Migrate(ctx, d, "llm", os.DirFS("../migrations")))
	set, err := settings.New(d, "llm", SettingDefinitions(), quiet())
	must(err)
	must(set.Migrate(ctx))
	q := queue.New(d, quiet())
	q.PollInterval = 50 * time.Millisecond
	st := NewStack(module.Deps{Config: config.Config{Home: home}, DB: d, Log: quiet(), Queue: q}, set)
	st.Resolver.HealthInterval = 200 * time.Millisecond
	st.Resolver.Interval = time.Second
	q.Start(ctx)
	must(st.Start(ctx))
	defer st.Resolver.StopAll(context.Background())

	hw := st.Detector.Hardware(ctx, true)
	t.Logf("hardware: flavour=%s devices=%+v sources=%v", hw.Flavour, hw.Devices, hw.Sources)

	wait := func(id int64) {
		t.Helper()
		for {
			inst, err := st.Store.GetInstall(ctx, id)
			must(err)
			switch inst.State {
			case InstallDone:
				t.Logf("install %s done (%d bytes, note %q)", inst.ModelID, inst.BytesTotal, inst.Note)
				return
			case InstallFailed, InstallCancelled:
				t.Fatalf("install %s: %s %s", inst.ModelID, inst.State, inst.Error)
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	// CPU builds: deterministic on any box (and the CI-sized download).
	for _, req := range []InstallRequest{
		{Repo: "ggml-org/tiny-llamas", File: "stories15M-q4_0.gguf", Assign: []string{"live", "background"}, GPUBackend: "cpu"},
		{CatalogID: "all-minilm-l6-v2", Assign: []string{"embeddings"}, GPUBackend: "cpu"},
		{Repo: "ggerganov/whisper.cpp", File: "ggml-tiny.en-q5_1.bin", Assign: []string{"stt"}, GPUBackend: "cpu"},
	} {
		inst, _, err := st.Manager.Install(ctx, req)
		must(err)
		wait(inst.ID)
	}
	for _, l := range []string{"live", "background", "embeddings", "stt"} {
		must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{l: {"gpu_backend": "cpu"}}))
	}
	must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{"live": {"context": float64(256)}, "background": {"context": float64(256)}}))

	resolve := func(label string) engine.Endpoint {
		t.Helper()
		for {
			ep, err := st.Resolver.Resolve(ctx, label)
			if err == nil {
				return ep
			}
			if ctx.Err() != nil {
				for _, i := range st.Resolver.Instances() {
					t.Logf("%s %s %s\n%s", i.Name, i.State, i.LastError, strings.Join(i.Output, "\n"))
				}
				t.Fatalf("%s: %v", label, err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	post := func(ep engine.Endpoint, path, ctype string, body io.Reader) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL+path, body)
		req.Header.Set("Content-Type", ctype)
		if ep.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+ep.APIKey)
		}
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s%s: %d %s", ep.BaseURL, path, resp.StatusCode, b)
		}
		return string(b)
	}

	live, bg := resolve("live"), resolve("background")
	if live.Engine != bg.Engine {
		t.Errorf("live and background should share: %s vs %s", live.Engine, bg.Engine)
	}
	out := post(live, "/completions", "application/json", strings.NewReader(`{"prompt":"Once upon a time","max_tokens":16}`))
	var comp struct {
		Choices []struct{ Text string } `json:"choices"`
	}
	json.Unmarshal([]byte(out), &comp)
	if len(comp.Choices) == 0 || comp.Choices[0].Text == "" {
		t.Fatalf("completion: %s", out)
	}
	t.Logf("completion: %q", comp.Choices[0].Text)

	emb := resolve("embeddings")
	out = post(emb, "/embeddings", "application/json", strings.NewReader(`{"input":"hello world"}`))
	var er struct {
		Data []struct{ Embedding []float64 } `json:"data"`
	}
	json.Unmarshal([]byte(out), &er)
	if len(er.Data) != 1 || len(er.Data[0].Embedding) != 384 {
		t.Fatalf("embedding: %.200s", out)
	}
	t.Logf("embedding: %d dims", len(er.Data[0].Embedding))

	speech := fetchJFK(ctx, t, home)
	transcribe := func(ep engine.Endpoint) string {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "jfk.wav")
		fw.Write(speech)
		mw.WriteField("response_format", "json")
		mw.Close()
		out := post(ep, "/inference", mw.FormDataContentType(), &body)
		var tr struct{ Text string }
		json.Unmarshal([]byte(out), &tr)
		if !strings.Contains(strings.ToLower(tr.Text), "ask not what your country") {
			t.Fatalf("transcription of jfk.wav: %s", out)
		}
		return strings.TrimSpace(tr.Text)
	}
	stt := resolve("stt")
	t.Logf("cpu transcription: %q", transcribe(stt))
	for _, i := range st.Resolver.Instances() {
		t.Logf("engine %s labels=%v args=%v", i.Name, i.Labels, i.Args)
	}

	// JARVIS_ENGINE_TEST_GPU=1: also run live on the detected GPU build (fetched on demand by
	// the resolver, as when a user switches gpu_backend), leaving background on the CPU one.
	if os.Getenv("JARVIS_ENGINE_TEST_GPU") == "1" && hw.Flavour != engine.FlavourCPU {
		must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{"live": {"gpu_backend": string(hw.Flavour)}}))
		gpu := resolve("live")
		if gpu.Engine == bg.Engine {
			t.Fatal("GPU live still shares the CPU engine")
		}
		out := post(gpu, "/completions", "application/json", strings.NewReader(`{"prompt":"Once upon a time","max_tokens":16}`))
		t.Logf("%s completion: %.200s", hw.Flavour, out)
		logGPU(t, st, gpu.Engine, "")

		// whisper-server on the GPU build too (our CI's CUDA/Vulkan/Metal builds).
		must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{"stt": {"gpu_backend": string(hw.Flavour)}}))
		gstt := resolve("stt")
		if gstt.Engine == stt.Engine {
			t.Fatal("GPU stt still runs the CPU engine")
		}
		t.Logf("%s transcription: %q", hw.Flavour, transcribe(gstt))
		if !logGPU(t, st, gstt.Engine, gpuLogName[hw.Flavour]) {
			t.Errorf("whisper-server output never mentions %s", gpuLogName[hw.Flavour])
		}
	}
}

// gpuLogName is how ggml names each GPU backend in its log lines.
var gpuLogName = map[engine.Flavour]string{engine.FlavourCUDA: "CUDA", engine.FlavourVulkan: "Vulkan",
	engine.FlavourMetal: "Metal", engine.FlavourROCm: "ROCm"}

// logGPU logs an engine's GPU-related output lines and reports whether any mentions want.
func logGPU(t *testing.T, st *Stack, name, want string) bool {
	t.Helper()
	found := false
	for _, i := range st.Resolver.Instances() {
		if i.Name != name {
			continue
		}
		for _, l := range i.Output {
			if strings.Contains(l, "CUDA") || strings.Contains(l, "offload") || strings.Contains(l, "Vulkan") ||
				strings.Contains(l, "Metal") || strings.Contains(l, "ROCm") || strings.Contains(l, "backend") {
				t.Logf("  %s", l)
				found = found || (want != "" && strings.Contains(l, want))
			}
		}
	}
	return found
}

// fetchJFK returns whisper.cpp's 11 s jfk.wav sample (16 kHz mono), cached under home.
func fetchJFK(ctx context.Context, t *testing.T, home string) []byte {
	t.Helper()
	p := filepath.Join(home, "jfk.wav")
	if b, err := os.ReadFile(p); err == nil {
		return b
	}
	url := "https://raw.githubusercontent.com/ggml-org/whisper.cpp/" + engine.Releases[engine.KindWhisper].Build + "/samples/jfk.wav"
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%s: %d %v", url, resp.StatusCode, err)
	}
	os.WriteFile(p, b, 0o644)
	return b
}
