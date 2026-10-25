package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The fake engine is this test binary re-executed with FAKE_ENGINE=1 in its environment. It
// accepts llama-server and whisper-server command lines and serves just enough of each:
//
//	llama:   GET /health, POST /v1/chat/completions, POST /v1/embeddings (Bearer LLAMA_API_KEY)
//	whisper: GET /health, POST /inference
//	python:  `-I -c CODE` exits 0 (FAKE_PY_FAIL=1: 1); `-I -m mlx_lm.server --model DIR …` serves
//	         GET /health and POST /v1/chat/completions with no key, as mlx_lm.server does
//	         (vLLM's `-m vllm.entrypoints.openai.api_server`: Bearer VLLM_API_KEY when set;
//	         Transformers' `-m transformers.cli.transformers serve DIR …`: 400 for a "model" other
//	         than DIR and 422 for a reasoning_budget field, as transformers serve does)
//
// With FAKE_UV=1 it is uv instead (runFakeUV).
//
// Knobs: FAKE_ENGINE_LOG (directory: each start writes <pid>.json with argv and env),
// FAKE_ENGINE_LOADING (duration /health answers 503 first), FAKE_ENGINE_LIST_DEVICES (printed
// for --list-devices), FAKE_ENGINE_OOM=1 (exits 1 after a CUDA out-of-memory line).
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_UV") == "1" {
		os.Exit(runFakeUV(os.Args[1:]))
	}
	if os.Getenv("FAKE_ENGINE") == "1" {
		runFakeEngine(os.Args[1:])
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeStart struct {
	Args []string          `json:"args"`
	Env  map[string]string `json:"env"`
}

func runFakeEngine(args []string) {
	if len(args) > 0 && args[0] == "--list-devices" {
		fmt.Println("Available devices:")
		fmt.Print(os.Getenv("FAKE_ENGINE_LIST_DEVICES"))
		return
	}
	python := len(args) > 0 && args[0] == "-I"
	if python && len(args) > 1 && args[1] == "-c" {
		if p := os.Getenv("FAKE_PY_CHECK_LOG"); p != "" {
			os.WriteFile(p, []byte("HF_HUB_OFFLINE="+os.Getenv("HF_HUB_OFFLINE")), 0o644)
		}
		if os.Getenv("FAKE_PY_FAIL") == "1" {
			fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'mlx_lm'")
			os.Exit(1)
		}
		return
	}
	flag := func(names ...string) string {
		for i := 0; i < len(args)-1; i++ {
			for _, n := range names {
				if args[i] == n {
					return args[i+1]
				}
			}
		}
		return ""
	}
	has := func(n string) bool {
		for _, a := range args {
			if a == n {
				return true
			}
		}
		return false
	}
	if dir := os.Getenv("FAKE_ENGINE_LOG"); dir != "" {
		env := map[string]string{}
		for _, k := range []string{"LLAMA_API_KEY", "CUDA_VISIBLE_DEVICES", "GGML_VK_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES", "HF_HUB_OFFLINE",
			"VLLM_API_KEY", "VLLM_CACHE_ROOT", "VLLM_USE_FLASHINFER_SAMPLER"} {
			if v, ok := os.LookupEnv(k); ok {
				env[k] = v
			}
		}
		b, _ := json.Marshal(fakeStart{Args: args, Env: env})
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid())), b, 0o644)
	}
	model := flag("-m")
	serve := python && flag("-m") == "transformers.cli.transformers"
	if python {
		model = flag("--model")
	}
	if serve {
		model = flag("serve")
	}
	if _, err := os.Stat(model); err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to load model:", err)
		os.Exit(1)
	}
	if os.Getenv("FAKE_ENGINE_OOM") == "1" {
		// llama.cpp on a CUDA card another engine already fills.
		fmt.Fprintln(os.Stderr, "ggml_backend_cuda_buffer_type_alloc_buffer: allocating 5120.00 MiB on device 0: cudaMalloc failed: out of memory")
		fmt.Fprintln(os.Stderr, "llama_model_load: error loading model: unable to allocate CUDA0 buffer")
		os.Exit(1)
	}
	if os.Getenv("FAKE_ENGINE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	loading, _ := time.ParseDuration(os.Getenv("FAKE_ENGINE_LOADING"))
	start := time.Now()
	key := os.Getenv("LLAMA_API_KEY")
	if python {
		key = os.Getenv("VLLM_API_KEY")
	}
	l, err := net.Listen("tcp", flag("--host")+":"+flag("--port"))
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < loading {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"status":"loading model"}`)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if has("--embedding") {
			http.Error(w, "embedding-only server", http.StatusNotImplemented)
			return
		}
		if serve {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if m, ok := body["model"]; ok && m != model {
				http.Error(w, fmt.Sprintf(`{"detail":"Server is pinned to '%s'; requested '%v'."}`, model, m), http.StatusBadRequest)
				return
			}
			if _, ok := body["reasoning_budget"]; ok {
				http.Error(w, `{"detail":"Unexpected fields in the request: {'reasoning_budget'}"}`, http.StatusUnprocessableEntity)
				return
			}
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"hi from %s"},"finish_reason":"stop"}],"model":%q}`,
			filepath.Base(model), flag("--alias"))
	})
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		fmt.Fprint(w, `{"data":[{"embedding":[0.6,0.8],"index":0}]}`)
	})
	mux.HandleFunc("POST /inference", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"text":"hello world"}`)
	})
	_ = http.Serve(l, mux)
}
