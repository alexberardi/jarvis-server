package engine

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  -t 8  ", []string{"-t", "8"}},
		{`--chat-template-kwargs '{"enable_thinking": false}' --reasoning-budget 0`,
			[]string{"--chat-template-kwargs", `{"enable_thinking": false}`, "--reasoning-budget", "0"}},
		{`--x "a \"b\" c" d\ e`, []string{"--x", `a "b" c`, "d e"}},
		{`''`, []string{""}},
	}
	for _, c := range cases {
		got, err := SplitArgs(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("SplitArgs(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{`'open`, `"open`, `trail\`} {
		if _, err := SplitArgs(bad); err == nil {
			t.Errorf("SplitArgs(%q) accepted", bad)
		}
	}
}

// G9 (01 §9): label settings to engine argv, including prod's two labels.
func TestEngineArgv(t *testing.T) {
	model := "/m/Qwen3.8-27B-UD-Q4_K_M.gguf"
	cases := []struct {
		name string
		c    LabelConfig
		f    Flavour
		want string
	}{
		{"prod live", LabelConfig{Kind: KindLlama, Model: "qwen3.8-27b", ModelPath: model, MMProjPath: "/m/mmproj-F16.gguf",
			Context: 13312, Parallel: 4, GPULayers: 999, GPUDevices: "1", KVCacheType: "f16", FlashAttn: "auto",
			ExtraArgs: `--chat-template-kwargs '{"enable_thinking":false}' --reasoning-budget 0`}, FlavourCUDA,
			`-m /m/Qwen3.8-27B-UD-Q4_K_M.gguf --host 127.0.0.1 --port 9000 --alias qwen3.8-27b --no-webui -c 13312 -np 4 -ngl 999 --jinja --mmproj /m/mmproj-F16.gguf --chat-template-kwargs {"enable_thinking":false} --reasoning-budget 0`},
		{"prod background", LabelConfig{Kind: KindLlama, Model: "qwen3.8-27b", ModelPath: model, Context: 131072, Parallel: 1,
			GPULayers: 999, GPUDevices: "0", KVCacheType: "q8_0", FlashAttn: "on"}, FlavourCUDA,
			`-m /m/Qwen3.8-27B-UD-Q4_K_M.gguf --host 127.0.0.1 --port 9000 --alias qwen3.8-27b --no-webui -c 131072 -np 1 -ngl 999 --jinja -ctk q8_0 -ctv q8_0 -fa on`},
		{"two-gpu split", LabelConfig{Kind: KindLlama, Model: "big", ModelPath: "/m/big.gguf", Context: 8192, Parallel: 1, GPULayers: 999,
			GPUDevices: "0, 1", SplitMode: "layer", TensorSplit: "1, 1"}, FlavourCUDA,
			`-m /m/big.gguf --host 127.0.0.1 --port 9000 --alias big --no-webui -c 8192 -np 1 -ngl 999 --jinja -sm layer -ts 1,1`},
		{"metal", LabelConfig{Kind: KindLlama, Model: "qwen3-8b", ModelPath: "/m/q.gguf", Context: 16384, Parallel: 2, GPULayers: 999}, FlavourMetal,
			`-m /m/q.gguf --host 127.0.0.1 --port 9000 --alias qwen3-8b --no-webui -c 16384 -np 2 -ngl 999 --jinja`},
		{"cpu drops gpu flags", LabelConfig{Kind: KindLlama, Model: "x", ModelPath: "/m/x.gguf", Context: 4096, Parallel: 1, GPULayers: 999,
			GPUDevices: "1", TensorSplit: "1,1"}, FlavourCPU,
			`-m /m/x.gguf --host 127.0.0.1 --port 9000 --alias x --no-webui -c 4096 -np 1 -ngl 0 --jinja`},
		{"embeddings on a gpu build", LabelConfig{Kind: KindLlama, Model: "all-minilm-l6-v2", ModelPath: "/m/minilm.gguf", Context: 512, Parallel: 4,
			GPULayers: 0, Embedding: true}, FlavourCUDA,
			`-m /m/minilm.gguf --host 127.0.0.1 --port 9000 --alias all-minilm-l6-v2 --no-webui -c 512 -np 4 -ngl 0 -dev none --embedding --pooling mean -b 2048 -ub 2048`},
		{"whisper gpu", LabelConfig{Kind: KindWhisper, Model: "whisper-large-v3-turbo", ModelPath: "/m/ggml-large-v3-turbo.bin", GPULayers: 999,
			GPUDevices: "1", ExtraArgs: "-t 8"}, FlavourCUDA,
			`-m /m/ggml-large-v3-turbo.bin --host 127.0.0.1 --port 9000 -t 8`},
		{"whisper cpu", LabelConfig{Kind: KindWhisper, Model: "w", ModelPath: "/m/w.bin", GPULayers: 999, FlashAttn: "off"}, FlavourCPU,
			`-m /m/w.bin --host 127.0.0.1 --port 9000 --no-gpu -nfa`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := keyFor(c.c, c.f, "/e/bin")
			args, err := k.args(9000, c.c.Model)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(args, " "); got != c.want {
				t.Errorf("argv\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestInstanceKeySharing(t *testing.T) {
	base := LabelConfig{Label: LabelLive, Kind: KindLlama, Model: "a", ModelPath: "/m/a.gguf", Context: 8192, Parallel: 2, GPULayers: 999, GPUDevices: "0"}
	bg := base
	bg.Label, bg.Model = LabelBackground, "/m/a.gguf" // label and model ref don't matter
	if keyFor(base, FlavourCUDA, "/e/b").hash() != keyFor(bg, FlavourCUDA, "/e/b").hash() {
		t.Error("same path/settings/placement must share")
	}
	for name, mut := range map[string]func(*LabelConfig){
		"context":   func(c *LabelConfig) { c.Context = 4096 },
		"placement": func(c *LabelConfig) { c.GPUDevices = "1" },
		"kv":        func(c *LabelConfig) { c.KVCacheType = "q8_0" },
		"mmproj":    func(c *LabelConfig) { c.MMProjPath = "/m/p.gguf" },
		"path":      func(c *LabelConfig) { c.ModelPath = "/m/b.gguf" },
	} {
		o := base
		mut(&o)
		if keyFor(base, FlavourCUDA, "/e/b").hash() == keyFor(o, FlavourCUDA, "/e/b").hash() {
			t.Errorf("%s change must not share", name)
		}
	}
	if keyFor(base, FlavourCUDA, "/e/b").hash() == keyFor(base, FlavourVulkan, "/e/v").hash() {
		t.Error("flavour change must not share")
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://api.openai.com/v1/chat/completions": "https://api.openai.com/v1",
		"https://api.openai.com/v1/":                 "https://api.openai.com/v1",
		" http://host:8080/v1 ":                      "http://host:8080/v1",
	} {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
