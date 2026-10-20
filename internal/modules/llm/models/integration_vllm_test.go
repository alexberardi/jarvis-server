//go:build vllm

package models

// Real vLLM check (decision 2026-10-19-vllm-engine; llm 01 §3.12), on a linux/amd64 box with an
// NVIDIA GPU: installs the pinned vllm through the engine fetch job (uv, the hash-locked set,
// several GB), downloads Qwen3-0.6B as a Hugging Face directory, assigns it to live and runs a
// chat, a streamed turn (tokens/s) and a tool call through Resolve, then logs the GPU memory.
//
//	JARVIS_ENGINE_TEST_HOME=/some/cache go test -tags vllm -run TestRealVLLM -v -timeout 40m ./internal/modules/llm/models/
//
// JARVIS_ENGINE_TEST_HOME keeps the engine and the model between runs (default: a temp dir).
// JARVIS_VLLM_TEST_MODEL names another model directory; JARVIS_VLLM_TEST_GPU_MEMORY sets the
// label's gpu_memory_utilization (default: sized to the model); JARVIS_VLLM_TEST_EXTRA its
// extra_args (e.g. --enforce-eager).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// qwen3Files is Qwen/Qwen3-0.6B at a pinned revision: what vLLM loads.
var qwen3Files = []string{"config.json", "generation_config.json", "model.safetensors", "tokenizer.json",
	"tokenizer_config.json", "vocab.json", "merges.txt"}

const qwen3Rev = "c1899de289a04d12100db370d81485cdf75e47ca"

func TestRealVLLM(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
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
	must(db.Migrate(ctx, d, "llm", os.DirFS("../migrations")))
	set, err := settings.New(d, "llm", SettingDefinitions(), quiet())
	must(err)
	must(set.Migrate(ctx))
	q := queue.New(d, quiet())
	q.PollInterval = 100 * time.Millisecond
	st := NewStack(module.Deps{Config: config.Config{Home: home}, DB: d, Log: quiet(), Queue: q}, set)
	st.Resolver.HealthInterval = time.Second
	st.Resolver.Interval = 2 * time.Second
	q.Start(ctx)
	must(st.Start(ctx))
	defer st.Resolver.StopAll(context.Background())

	hw := st.Detector.Hardware(ctx, true)
	t.Logf("hardware: flavour=%s driver=%s devices=%+v", hw.Flavour, hw.NVIDIADriver, hw.Devices)
	if why := engine.UnavailableOn(engine.KindVLLM, engine.Host(), hw); why != "" {
		t.Skip(why)
	}

	dir := os.Getenv("JARVIS_VLLM_TEST_MODEL")
	if dir == "" {
		dir = filepath.Join(home, "models", "Qwen3-0.6B")
		for _, f := range qwen3Files {
			fetchHF(ctx, t, "Qwen/Qwen3-0.6B", qwen3Rev, f, filepath.Join(dir, f))
		}
	}
	upd := map[string]any{"model": dir, "context": float64(4096)}
	if s := os.Getenv("JARVIS_VLLM_TEST_GPU_MEMORY"); s != "" {
		f, err := strconv.ParseFloat(s, 64)
		must(err)
		upd["gpu_memory_utilization"] = f
	}
	if s := os.Getenv("JARVIS_VLLM_TEST_EXTRA"); s != "" {
		upd["extra_args"] = s
	}
	must(st.Manager.UpdateLabels(ctx, map[string]map[string]any{"live": upd, "background": {"engine": "shared"}}))

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
	t.Logf("live ready after %s on %s (%s)", time.Since(started).Round(time.Second), ep.Engine, ep.BaseURL)
	for _, i := range st.Resolver.Instances() {
		t.Logf("engine %s labels=%v args=%v", i.Name, i.Labels, i.Args)
	}

	post := func(body string) *http.Response {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.BaseURL+"/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ep.APIKey)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("%d %s", resp.StatusCode, b)
		}
		return resp
	}
	type msg struct {
		Content   string            `json:"content"`
		Reasoning string            `json:"reasoning_content"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	}
	chat := func(body string) (msg, map[string]int) {
		t.Helper()
		resp := post(body)
		defer resp.Body.Close()
		var out struct {
			Choices []struct{ Message msg } `json:"choices"`
			Usage   map[string]int          `json:"usage"`
		}
		must(json.NewDecoder(resp.Body).Decode(&out))
		if len(out.Choices) == 0 {
			t.Fatal("no choices")
		}
		return out.Choices[0].Message, out.Usage
	}

	// A chat turn as cc sends one (thinking off through the template kwarg, LD8).
	m, _ := chat(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"What is the capital of France? One word."}],
		"max_tokens":64,"temperature":0,"chat_template_kwargs":{"enable_thinking":false}}`, ep.Model))
	t.Logf("chat: %q", m.Content)
	if !strings.Contains(strings.ToLower(m.Content), "paris") || strings.Contains(m.Content, "<think>") {
		t.Errorf("chat answer %q", m.Content)
	}

	// A streamed turn: tokens/s from the usage chunk.
	t0 := time.Now()
	resp := post(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Write about 200 words on lighthouses."}],
		"max_tokens":256,"temperature":0,"stream":true,"stream_options":{"include_usage":true},
		"chat_template_kwargs":{"enable_thinking":false}}`, ep.Model))
	var first time.Duration
	chunks, completion := 0, 0
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || line == "[DONE]" {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		if len(c.Choices) > 0 && c.Choices[0].Delta.Content != "" {
			if chunks == 0 {
				first = time.Since(t0)
			}
			chunks++
		}
		if c.Usage != nil {
			completion = c.Usage.CompletionTokens
		}
	}
	resp.Body.Close()
	gen := time.Since(t0) - first
	if chunks < 10 || completion == 0 {
		t.Fatalf("stream: %d chunks, %d tokens", chunks, completion)
	}
	t.Logf("stream: %d tokens in %d chunks, first token after %s, %.1f tokens/s after it",
		completion, chunks, first.Round(time.Millisecond), float64(completion-1)/gen.Seconds())

	// A tool call: the model's parser (hermes for Qwen3) returns tool_calls, not text.
	m, _ = chat(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"What's the weather in Paris right now?"}],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"Current weather for a city",
		"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}],
		"tool_choice":"auto","max_tokens":256,"temperature":0,"chat_template_kwargs":{"enable_thinking":false}}`, ep.Model))
	t.Logf("tool call: %s (content %q)", m.ToolCalls, m.Content)
	if len(m.ToolCalls) != 1 || !strings.Contains(string(m.ToolCalls[0]), "get_weather") {
		t.Errorf("no tool call: %+v", m)
	}

	// Thinking on: the reasoning parser keeps <think> out of the content.
	m, _ = chat(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Is 17 prime? Answer yes or no."}],
		"max_tokens":512,"temperature":0}`, ep.Model))
	t.Logf("thinking: content %q, reasoning %d chars", m.Content, len(m.Reasoning))
	if strings.Contains(m.Content, "<think>") {
		t.Errorf("think block in content: %q", m.Content)
	}

	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-compute-apps=pid,process_name,used_memory",
		"--format=csv,noheader").CombinedOutput(); err == nil {
		t.Logf("GPU memory by process:\n%s", out)
	}
	if out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=memory.used,memory.total", "--format=csv,noheader").CombinedOutput(); err == nil {
		t.Logf("GPU memory: %s", strings.TrimSpace(string(out)))
	}
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
