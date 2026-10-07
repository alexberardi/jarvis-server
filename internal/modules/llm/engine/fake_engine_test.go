package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fake engine is this test binary re-executed with FAKE_ENGINE=1 in its environment. It
// accepts llama-server and whisper-server command lines and serves just enough of each:
//
//	llama:   GET /health, POST /v1/chat/completions, POST /v1/embeddings (Bearer LLAMA_API_KEY)
//	whisper: GET /health, POST /inference
//
// Knobs: FAKE_ENGINE_LOG (directory: each start writes <pid>.json with argv and env),
// FAKE_ENGINE_LOADING (duration /health answers 503 first), FAKE_ENGINE_LIST_DEVICES (printed
// for --list-devices).
func TestMain(m *testing.M) {
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
		for _, k := range []string{"LLAMA_API_KEY", "CUDA_VISIBLE_DEVICES", "GGML_VK_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES"} {
			if v, ok := os.LookupEnv(k); ok {
				env[k] = v
			}
		}
		b, _ := json.Marshal(fakeStart{Args: args, Env: env})
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid())), b, 0o644)
	}
	if _, err := os.Stat(flag("-m")); err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to load model:", err)
		os.Exit(1)
	}
	loading, _ := time.ParseDuration(os.Getenv("FAKE_ENGINE_LOADING"))
	start := time.Now()
	key := os.Getenv("LLAMA_API_KEY")
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
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"hi from %s"},"finish_reason":"stop"}],"model":%q}`,
			filepath.Base(flag("-m")), flag("--alias"))
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
