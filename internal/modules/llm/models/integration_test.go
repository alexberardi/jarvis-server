//go:build llamaserver

package models

// Real-engine integration test: downloads the pinned llama.cpp and whisper.cpp CPU builds and
// tiny models from GitHub and Hugging Face, installs them through the model manager, and runs
// a completion, an embedding and a transcription through Resolve.
//
//	JARVIS_ENGINE_TEST_HOME=/some/cache go test -tags llamaserver -run TestRealEngines -v ./internal/modules/llm/models/
//
// JARVIS_ENGINE_TEST_HOME keeps downloads between runs (default: a temp dir).

import (
	"bytes"
	"context"
	"encoding/binary"
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

	stt := resolve("stt")
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "silence.wav")
	fw.Write(silentWAV(16000))
	mw.WriteField("response_format", "json")
	mw.Close()
	out = post(stt, "/inference", mw.FormDataContentType(), &body)
	t.Logf("transcription of 1 s of silence: %s", strings.TrimSpace(out))
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
		for _, i := range st.Resolver.Instances() {
			if i.Name == gpu.Engine {
				for _, l := range i.Output {
					if strings.Contains(l, "CUDA") || strings.Contains(l, "offload") || strings.Contains(l, "Vulkan") || strings.Contains(l, "Metal") {
						t.Logf("  %s", l)
					}
				}
			}
		}
	}
}

// silentWAV is n samples of 16 kHz mono 16-bit silence.
func silentWAV(n int) []byte {
	var b bytes.Buffer
	data := n * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	b.Write(make([]byte, data))
	return b.Bytes()
}
