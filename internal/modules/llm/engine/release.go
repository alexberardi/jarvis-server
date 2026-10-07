package engine

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/engines"
)

// Flavour is a llama.cpp build variant: which GPU runtime the llama-server binary targets.
type Flavour string

const (
	FlavourCPU    Flavour = "cpu"
	FlavourCUDA   Flavour = "cuda"
	FlavourROCm   Flavour = "rocm"
	FlavourVulkan Flavour = "vulkan"
	FlavourMetal  Flavour = "metal"
)

// AllFlavours lists every flavour name, for validation and the admin UI.
var AllFlavours = []Flavour{FlavourCPU, FlavourCUDA, FlavourROCm, FlavourVulkan, FlavourMetal}

// ParseFlavour accepts a flavour name; "" and "auto" return "" (detect).
func ParseFlavour(s string) (Flavour, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || s == "auto" {
		return "", nil
	}
	for _, f := range AllFlavours {
		if string(f) == s {
			return f, nil
		}
	}
	return "", fmt.Errorf("unknown GPU backend %q (want auto, cpu, cuda, rocm, vulkan or metal)", s)
}

// GPUBackend is the engines package backend that sets the flavour's visibility variable.
func (f Flavour) GPUBackend() engines.GPUBackend {
	switch f {
	case FlavourCUDA:
		return engines.CUDA
	case FlavourROCm:
		return engines.ROCm
	case FlavourVulkan:
		return engines.Vulkan
	case FlavourMetal:
		return engines.Metal
	}
	return ""
}

// Kind is an engine program. jarvisd runs two, through one code path: llama-server (LLM,
// vision, embeddings) and whisper-server (speech-to-text, D7). Both are ggml programs with the
// same GPU backends, release layout and /health semantics.
type Kind string

const (
	KindLlama   Kind = "llama-server"
	KindWhisper Kind = "whisper-server"
)

// Kinds lists the engine kinds.
var Kinds = []Kind{KindLlama, KindWhisper}

// Release pins one kind's upstream build (01 §3.6). Bumping it means replacing Assets with the
// new release's names and sha256 digests (GitHub's API lists both: `digest` per asset).
type Release struct {
	Kind  Kind
	Build string // release tag
	// DefaultBaseURL is where assets are downloaded from: <base>/<Build>/<asset name>. The
	// settings llm.engine_base_url / stt.engine_base_url override it (a mirror, a test server).
	DefaultBaseURL string
	Assets         map[Platform]map[Flavour][]Asset
}

// Asset is one release file. A flavour may need several (CUDA ships its runtime separately);
// all are extracted into the same directory.
type Asset struct {
	Name   string
	Size   int64
	SHA256 string
}

// Platform is a GOOS/GOARCH pair.
type Platform struct{ OS, Arch string }

// Host is the platform jarvisd runs on.
func Host() Platform { return Platform{runtime.GOOS, runtime.GOARCH} }

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// Releases holds the pinned build of each kind.
//
// llama.cpp: CUDA is 12.x on x64 (it runs on any driver from the 12 series up) and 13.x where
// that is the only build (arm64). Upstream ships ROCm for x64 only; AMD elsewhere uses Vulkan.
//
// whisper.cpp ships far fewer binaries: CPU for linux and windows, CUDA for windows. There is
// no upstream macOS (Metal) or linux GPU build; those hosts use a binary the operator installs
// (e.g. Homebrew's whisper-cpp), named by the stt.engine_path setting, until jarvis hosts its
// own builds behind stt.engine_base_url.
var Releases = map[Kind]Release{
	KindLlama: {
		Kind: KindLlama, Build: "b11457",
		DefaultBaseURL: "https://github.com/ggml-org/llama.cpp/releases/download",
		Assets: map[Platform]map[Flavour][]Asset{
			{"linux", "amd64"}: {
				FlavourCPU:    {{"llama-b11457-bin-ubuntu-x64.tar.gz", 17733249, "210eaa41a16e0d24fa44071cb62f95702ef903c84d86506d6482ac919bcee078"}},
				FlavourVulkan: {{"llama-b11457-bin-ubuntu-vulkan-x64.tar.gz", 31679935, "cb528b7f75e466f5113685d8aca7a9966a5dac3192f2e12bd4f96d7505fbea39"}},
				FlavourCUDA: {
					{"llama-b11457-bin-ubuntu-cuda-12.8-x64.tar.gz", 171684958, "3d8d3d3f512a5395a674c61c0ec25210a87accaf679678d4f5a482d5920b9b75"},
					{"cudart-llama-b11457-bin-ubuntu-cuda-12.8-x64.tar.gz", 594377393, "e3af9df400a4ed9612627346a03439e2bc443be95a26ada3f7c65c291832144a"},
				},
				FlavourROCm: {{"llama-b11457-bin-ubuntu-rocm-10.0-x64.tar.gz", 243948112, "ccc0e8a463febaa8001052dcb25c4372074406aa51d6f68a02086f0eabaaa875"}},
			},
			{"linux", "arm64"}: {
				FlavourCPU:    {{"llama-b11457-bin-ubuntu-arm64.tar.gz", 13725320, "f1df5992bd17702a5c40b3081a65d33f4ed5bfc963f6a8fc4dbf0be78a2b1e48"}},
				FlavourVulkan: {{"llama-b11457-bin-ubuntu-vulkan-arm64.tar.gz", 24887630, "5fb7ea8da121af1c6a96a3c67ea0a8c8b9dcd256ab2c8cfacd8a9c7ded3d64a5"}},
				FlavourCUDA: {
					{"llama-b11457-bin-ubuntu-cuda-13.4-arm64.tar.gz", 147667505, "b23dc2f61e357b48d1c3ad45159086c688265105bc69e317b2a56cf8ca194b1c"},
					{"cudart-llama-b11457-bin-ubuntu-cuda-13.4-arm64.tar.gz", 552521502, "6e66c26a9a007d5721330c1f3de6e30eb4e6cb22bb990cb921c5de96bed29086"},
				},
			},
			{"darwin", "arm64"}: {
				// Upstream macOS arm64 builds are Metal builds; "cpu" runs the same binary with -ngl 0.
				FlavourMetal: {{"llama-b11457-bin-macos-arm64.tar.gz", 12007659, "e234070cbde0c8b0d30f79fa08ff8246abe22c72acb752619349b8d9c46f7839"}},
				FlavourCPU:   {{"llama-b11457-bin-macos-arm64.tar.gz", 12007659, "e234070cbde0c8b0d30f79fa08ff8246abe22c72acb752619349b8d9c46f7839"}},
			},
			{"darwin", "amd64"}: {
				FlavourCPU: {{"llama-b11457-bin-macos-x64.tar.gz", 11525345, "f807f15aa0d4d9947b9befd06b3ff4bd8ddd30775d7b4b4505c8d3c5f33f0014"}},
			},
			{"windows", "amd64"}: {
				FlavourCPU:    {{"llama-b11457-bin-win-cpu-x64.zip", 19436652, "ed20ed40e10c04d0853d8acf8700f0e6c1bb8212f0cc70c3a0da13dcbbc1b802"}},
				FlavourVulkan: {{"llama-b11457-bin-win-vulkan-x64.zip", 33377746, "d01301582c711a69b9747b5984710d6ca99e57d95f680d3d33753cec570b4cb6"}},
				FlavourCUDA: {
					{"llama-b11457-bin-win-cuda-12.4-x64.zip", 264528387, "c901dbb473c9472288e38ef06bf85e4e13d7de6bb24732b03dbe8aa05fcc483c"},
					{"cudart-llama-bin-win-cuda-12.4-x64.zip", 391443627, "8c79a9b226de4b3cacfd1f83d24f962d0773be79f1e7b75c6af4ded7e32ae1d6"},
				},
				FlavourROCm: {{"llama-b11457-bin-win-rocm-10.0-x64.zip", 257005600, "babe5e15ad2ccc966c549a087b07f56bcfec4a701c66e869eae3d123f5099f82"}},
			},
			{"windows", "arm64"}: {
				FlavourCPU:    {{"llama-b11457-bin-win-cpu-arm64.zip", 12264648, "fab992671bd26ba4117da4f70f330fe1b9fe69eb8ce952281cabf4c5c5f2a17e"}},
				FlavourVulkan: {{"llama-b11457-bin-win-vulkan-arm64.zip", 25918589, "189d661d7a83b9a50fb10a6b701bb7b6d8beeb4c3efd0f108224590e4476029d"}},
			},
		},
	},
	KindWhisper: {
		Kind: KindWhisper, Build: "b5454",
		DefaultBaseURL: "https://github.com/ggml-org/whisper.cpp/releases/download",
		Assets: map[Platform]map[Flavour][]Asset{
			{"linux", "amd64"}: {
				FlavourCPU: {{"whisper-bin-ubuntu-x64.tar.gz", 10364195, "a72becf15d7917f990f6313867a52638b82b7f9ef237fb0c980dac56a135781c"}},
			},
			{"linux", "arm64"}: {
				FlavourCPU: {{"whisper-bin-ubuntu-arm64.tar.gz", 4608377, "6b95ebfc60447df48e70ef00a73bdc3f41679ed2d01ef465827206a2ff325149"}},
			},
			{"windows", "amd64"}: {
				FlavourCPU:  {{"whisper-bin-x64.zip", 8928640, "6ba69e3482d7826214f90a6a9c84ca07782aec1e1d0c6a7c30c994fd5d816ccb"}},
				FlavourCUDA: {{"whisper-bin-win-cuda-12.4.0-x64.zip", 684913404, "afef0b881c500958921c3f5523b50e59ee2ec9b6f5cbd25b324c51ed308a957a"}},
			},
			{"windows", "arm64"}: {
				FlavourCPU: {{"whisper-bin-win-cpu-arm64.zip", 4371193, "28c37e7b598c3d9bbfef94f3bd67f2ee6f12b7c86f3da5edcf5e4308d615ff6d"}},
			},
		},
	},
}

// BinaryName is the kind's executable file name on a platform.
func (k Kind) BinaryName(p Platform) string {
	if p.OS == "windows" {
		return string(k) + ".exe"
	}
	return string(k)
}

// AssetsFor returns a kind's assets for a flavour on a platform, or an error naming the
// flavours that platform does have.
func AssetsFor(k Kind, p Platform, f Flavour) ([]Asset, error) {
	r, ok := Releases[k]
	if !ok {
		return nil, fmt.Errorf("unknown engine kind %q", k)
	}
	m, ok := r.Assets[p]
	if !ok {
		return nil, fmt.Errorf("no %s build for %s", k, p)
	}
	a, ok := m[f]
	if !ok {
		return nil, fmt.Errorf("no %s %s build for %s (available: %s)", f, k, p, strings.Join(FlavoursFor(k, p), ", "))
	}
	return a, nil
}

// FlavoursFor lists the flavours a kind has builds for on a platform, in AllFlavours order.
func FlavoursFor(k Kind, p Platform) []string {
	var out []string
	for _, f := range AllFlavours {
		if _, ok := Releases[k].Assets[p][f]; ok {
			out = append(out, string(f))
		}
	}
	return out
}

// AssetsSize is the total download size of a flavour's assets (0 if there is no build).
func AssetsSize(k Kind, p Platform, f Flavour) int64 {
	a, _ := AssetsFor(k, p, f)
	var n int64
	for _, x := range a {
		n += x.Size
	}
	return n
}
