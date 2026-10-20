//go:build transformers

package llm

// Real Transformers check (decision 2026-10-19-transformers-engine; llm 01 §3.11): in a
// throwaway jarvisd home, the engine fetch job installs the pinned transformers + torch set (uv,
// the hash-locked set for this platform), SmolLM2-135M-Instruct is downloaded as a Hugging Face
// directory and assigned to live with dir_engine=transformers, and a chat and a streamed turn go
// through jarvisd's own chat service (thinking off, as cc sends it), with tokens/s logged.
//
//	JARVIS_ENGINE_TEST_HOME=/some/cache go test -tags transformers -run TestRealTransformers -v -timeout 30m ./internal/modules/llm/
//
// JARVIS_ENGINE_TEST_HOME keeps the engine and the model between runs (default: a temp dir).
// JARVIS_TRANSFORMERS_TEST_BACKEND is the label's gpu_backend: auto (MPS on Apple Silicon, the
// default), metal or cpu. JARVIS_TRANSFORMERS_TEST_MODEL names another model directory;
// JARVIS_TRANSFORMERS_TEST_DTYPE sets its dtype.

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/models"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/engines"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// smolFiles is HuggingFaceTB/SmolLM2-135M-Instruct (270 MB in bfloat16): what transformers loads.
var smolFiles = []string{"config.json", "generation_config.json", "model.safetensors", "tokenizer.json",
	"tokenizer_config.json", "special_tokens_map.json"}

func TestRealTransformers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	home := os.Getenv("JARVIS_ENGINE_TEST_HOME")
	if home == "" {
		home = t.TempDir()
	}
	for _, f := range []string{"jarvis.db", "jarvis.db-wal", "jarvis.db-shm"} {
		os.Remove(filepath.Join(home, f))
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err := db.Open(ctx, filepath.Join(home, "jarvis.db"))
	must(err)
	defer d.Close()
	must(db.Migrate(ctx, d, queue.MigrationModule, queue.Migrations()))
	must(db.Migrate(ctx, d, "llm", Migrations()))
	set, err := settings.New(d, "llm", models.SettingDefinitions(), quietLog())
	must(err)
	must(set.Migrate(ctx))
	q := queue.New(d, quietLog())
	q.PollInterval = 100 * time.Millisecond
	st := models.NewStack(module.Deps{Config: config.Config{Home: home}, DB: d, Log: quietLog(), Queue: q}, set)
	st.Resolver.HealthInterval = time.Second
	st.Resolver.Interval = 2 * time.Second
	q.Start(ctx)
	must(st.Start(ctx))
	defer st.Resolver.StopAll(context.Background())

	if why := engine.UnavailableOn(engine.KindTransformers, engine.Host(), st.Detector.Hardware(ctx, true)); why != "" {
		t.Skip(why)
	}
	dir := os.Getenv("JARVIS_TRANSFORMERS_TEST_MODEL")
	if dir == "" {
		dir = filepath.Join(home, "models", "SmolLM2-135M-Instruct")
		for _, f := range smolFiles {
			fetchHF(ctx, t, "HuggingFaceTB/SmolLM2-135M-Instruct", "main", f, filepath.Join(dir, f))
		}
	}
	upd := map[string]any{"model": dir, "context": float64(2048), "dir_engine": "transformers"}
	if s := os.Getenv("JARVIS_TRANSFORMERS_TEST_BACKEND"); s != "" {
		upd["gpu_backend"] = s
	}
	if s := os.Getenv("JARVIS_TRANSFORMERS_TEST_DTYPE"); s != "" {
		upd["dtype"] = s
	}
	must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{"live": upd}))

	// The engine install (about 0.2 GB of wheels) and the model load happen behind Resolve.
	started := time.Now()
	var ep engine.Endpoint
	for {
		ep, err = st.Resolver.Resolve(ctx, "live")
		if err == nil {
			break
		}
		for _, i := range st.Resolver.Instances() {
			if i.State == engines.Failed || ctx.Err() != nil {
				t.Logf("%s %s %s\n%s", i.Name, i.State, i.LastError, strings.Join(i.Output, "\n"))
				t.Fatalf("live: %v", err)
			}
		}
		if ctx.Err() != nil {
			t.Fatalf("live: %v", err)
		}
		time.Sleep(time.Second)
	}
	t.Logf("live ready after %s on %s (%s), model %q", time.Since(started).Round(time.Second), ep.Engine, ep.BaseURL, ep.Model)
	for _, i := range st.Resolver.Instances() {
		t.Logf("engine %s flavour=%s labels=%v args=%v", i.Name, i.Flavour, i.Labels, i.Args)
	}
	if ep.Kind != engine.KindTransformers || !ep.NoReasoningBudget {
		t.Fatalf("endpoint %+v", ep)
	}

	// jarvisd's chat service, as cc calls it: thinking off (chat_template_kwargs only; the
	// server would refuse reasoning_budget with 422).
	svc := NewService(ServiceConfig{Resolver: stackResolver{st.Resolver}, Log: quietLog()})
	t0 := time.Now()
	r, err := svc.Chat(ctx, ChatRequest{Label: LabelLive, Messages: []Message{msg("user", "What is the capital of France? Answer in one word.")},
		Temperature: ptr(0.0), MaxTokens: ptr(32), ReasoningBudget: ptr(0)})
	must(err)
	t.Logf("chat (%s): %q, usage %+v", time.Since(t0).Round(time.Millisecond), r.Content, r.Usage)
	if strings.TrimSpace(r.Content) == "" {
		t.Fatal("empty chat answer")
	}

	// A streamed turn: the deltas must arrive as they are generated, not in one piece.
	t0 = time.Now()
	frames, err := svc.Stream(ctx, ChatRequest{Label: LabelLive, Messages: []Message{msg("user", "Write about 100 words on lighthouses.")},
		Temperature: ptr(0.0), MaxTokens: ptr(160), ReasoningBudget: ptr(0)})
	must(err)
	var first, last time.Duration
	deltas := 0
	var done Frame
	for f := range frames {
		switch {
		case f.Delta != "":
			if deltas == 0 {
				first = time.Since(t0)
			}
			last = time.Since(t0)
			deltas++
		case f.Done:
			done = f
		case f.Err != "":
			t.Fatalf("stream: %s", f.Err)
		}
	}
	if !done.Done || deltas < 10 || last-first < 50*time.Millisecond {
		t.Fatalf("stream: done %v, %d deltas over %s", done.Done, deltas, last-first)
	}
	tokens := done.Usage.CompletionTokens
	if tokens == 0 {
		tokens = deltas // no usage chunk: about one token per delta
	}
	t.Logf("stream: %d tokens (usage %d) in %d deltas, first after %s, %.1f tokens/s after it; %q…",
		tokens, done.Usage.CompletionTokens, deltas, first.Round(time.Millisecond), float64(tokens-1)/(last-first).Seconds(),
		firstN(done.Content, 80))

	// Tool calling: SmolLM2 has no response template in transformers serve, so a call stays in
	// the text (llm 01 §3.11); logged, not asserted.
	r, err = svc.Chat(ctx, ChatRequest{Label: LabelLive, Messages: []Message{msg("user", "What's the weather in Paris right now?")},
		Tools: []Tool{{Type: "function", Function: ToolFunction{Name: "get_weather", Description: "Current weather for a city",
			Parameters: []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}}},
		Temperature: ptr(0.0), MaxTokens: ptr(64), ReasoningBudget: ptr(0)})
	must(err)
	t.Logf("tool call: %d tool_calls, content %q", len(r.ToolCalls), firstN(r.Content, 120))
}

// fetchHF downloads one file of a Hugging Face repo at rev to dst, unless it is there.
func fetchHF(ctx context.Context, t *testing.T, repo, rev, file, dst string) {
	t.Helper()
	if _, err := os.Stat(dst); err == nil {
		return
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	url := "https://huggingface.co/" + repo + "/resolve/" + rev + "/" + file
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %s", url, resp.Status)
	}
	f, err := os.Create(dst + ".part")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if err := os.Rename(dst+".part", dst); err != nil {
		t.Fatal(err)
	}
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
