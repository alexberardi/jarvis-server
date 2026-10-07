package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseListDevices(t *testing.T) {
	// Real output from this box's Vulkan build, plus CUDA, ROCm and Metal lines and noise.
	out := `ggml_vulkan: Found 1 Vulkan devices:
ggml_vulkan: 0 = NVIDIA GeForce RTX 3080 Ti (NVIDIA) | uma: 0 | fp16: 1
Available devices:
  Vulkan0: NVIDIA GeForce RTX 3080 Ti (12288 MiB, 10071 MiB free)
  Vulkan1: llvmpipe (LLVM 17.0.6, 256 bits) (32000 MiB, 32000 MiB free)
  CUDA0: NVIDIA GeForce RTX 3090 (24135 MiB, 23700 MiB free)
  CUDA1: NVIDIA GeForce RTX 3090 (24135 MiB, 1200 MiB free)
  ROCm0: AMD Radeon RX 7900 XTX (24560 MiB, 24000 MiB free)
  MTL0: Apple M2 Max (49152 MiB, 49151 MiB free)
  SYCL0: Intel Arc (8000 MiB, 8000 MiB free)
`
	devs := ParseListDevices(out)
	if len(devs) != 6 {
		t.Fatalf("got %d devices: %+v", len(devs), devs)
	}
	want := []Device{
		{Backend: FlavourVulkan, Index: 0, ID: "Vulkan0", Name: "NVIDIA GeForce RTX 3080 Ti", TotalMB: 12288, FreeMB: 10071},
		{Backend: FlavourVulkan, Index: 1, ID: "Vulkan1", Name: "llvmpipe (LLVM 17.0.6, 256 bits)", TotalMB: 32000, FreeMB: 32000, Integrated: true},
		{Backend: FlavourCUDA, Index: 0, ID: "CUDA0", Name: "NVIDIA GeForce RTX 3090", TotalMB: 24135, FreeMB: 23700},
		{Backend: FlavourCUDA, Index: 1, ID: "CUDA1", Name: "NVIDIA GeForce RTX 3090", TotalMB: 24135, FreeMB: 1200},
		{Backend: FlavourROCm, Index: 0, ID: "ROCm0", Name: "AMD Radeon RX 7900 XTX", TotalMB: 24560, FreeMB: 24000},
		{Backend: FlavourMetal, Index: 0, ID: "MTL0", Name: "Apple M2 Max", TotalMB: 49152, FreeMB: 49151},
	}
	for i := range want {
		if devs[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, devs[i], want[i])
		}
	}
	if d := ParseListDevices("Available devices:\n  (none)\n"); len(d) != 0 {
		t.Errorf("cpu build: %+v", d)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	devs := ParseNvidiaSMI("0, NVIDIA GeForce RTX 3090, 24576, 400\n1, NVIDIA GeForce RTX 3090, 24576, 23000\ngarbage\n")
	if len(devs) != 2 || devs[1].Index != 1 || devs[1].TotalMB != 24576 || devs[1].FreeMB != 23000 || devs[0].Backend != FlavourCUDA {
		t.Fatalf("%+v", devs)
	}
}

func TestParseROCmSMI(t *testing.T) {
	out := `{"card0": {"Card Series": "AMD Radeon Graphics", "GFX Version": "gfx1036", "VRAM Total Memory (B)": "536870912", "VRAM Total Used Memory (B)": "0"},
	 "card1": {"Card Series": "Radeon RX 7900 XTX", "GFX Version": "gfx1100", "VRAM Total Memory (B)": "25753026560", "VRAM Total Used Memory (B)": "1073741824"},
	 "system": {"Driver version": "6.8"}}`
	devs := ParseROCmSMI(out)
	if len(devs) != 2 {
		t.Fatalf("%+v", devs)
	}
	if !devs[0].Integrated || devs[1].Integrated || devs[1].Index != 1 || devs[1].TotalMB != 24560 || devs[1].FreeMB != 23536 {
		t.Fatalf("%+v", devs)
	}
}

func TestParseVulkanInfo(t *testing.T) {
	out := `Devices:
========
GPU0:
	apiVersion         = 1.3.274
	deviceType         = PHYSICAL_DEVICE_TYPE_INTEGRATED_GPU
	deviceName         = AMD Radeon Graphics (RADV RAPHAEL_MENDOCINO)
GPU1:
	apiVersion         = 1.3.274
	deviceType         = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU
	deviceName         = AMD Radeon RX 7900 XTX (RADV NAVI31)
GPU2:
	deviceType         = PHYSICAL_DEVICE_TYPE_CPU
	deviceName         = llvmpipe (LLVM 17.0.6, 256 bits)
`
	devs := ParseVulkanInfo(out)
	if len(devs) != 3 || !devs[0].Integrated || devs[1].Integrated || !devs[2].Integrated || devs[1].Index != 1 ||
		devs[1].Name != "AMD Radeon RX 7900 XTX (RADV NAVI31)" {
		t.Fatalf("%+v", devs)
	}
}

func fakeRunner(outputs map[string]string) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		k := name
		if len(args) > 0 {
			k += " " + args[0]
		}
		for prefix, out := range outputs {
			if strings.HasPrefix(k, prefix) {
				return []byte(out), nil
			}
		}
		return nil, errors.New("not found")
	}
}

func TestDetector(t *testing.T) {
	t.Run("nvidia before any engine", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "amd64"}, Run: fakeRunner(map[string]string{
			"nvidia-smi": "0, NVIDIA GeForce RTX 3090, 24576, 400\n1, NVIDIA GeForce RTX 3090, 24576, 23000\n",
		})}
		h := d.Hardware(context.Background(), false)
		if h.Flavour != FlavourCUDA || len(h.Devices) != 2 || h.Sources[0] != "nvidia-smi" {
			t.Fatalf("%+v", h)
		}
		p := Propose(h)
		if p[LabelLive].Devices != "0" || p[LabelBackground].Devices != "1" || p[LabelLive].Flavour != FlavourCUDA || p[LabelEmbeddings].Devices != "" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("installed engine wins and dedupes", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "amd64"},
			Binaries: func() map[Flavour]string {
				return map[Flavour]string{FlavourCUDA: "/e/llama-server", FlavourCPU: "/e/cpu"}
			},
			Run: fakeRunner(map[string]string{
				"/e/llama-server --list-devices": "Available devices:\n  CUDA0: NVIDIA GeForce RTX 3080 Ti (12288 MiB, 10071 MiB free)\n",
				"nvidia-smi":                     "0, NVIDIA GeForce RTX 3080 Ti, 12288, 9000\n",
			})}
		h := d.Hardware(context.Background(), false)
		if len(h.Devices) != 1 || h.Devices[0].FreeMB != 10071 || len(h.Sources) != 2 {
			t.Fatalf("%+v", h)
		}
		p := Propose(h)
		if p[LabelLive].Devices != "0" || p[LabelBackground].Devices != "0" {
			t.Fatalf("one GPU: both labels on it: %+v", p)
		}
	})
	t.Run("amd igpu + discrete on linux x64 proposes rocm", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "amd64"}, Run: fakeRunner(map[string]string{
			"rocm-smi": `{"card0": {"Card Series": "iGPU", "GFX Version": "gfx1036", "VRAM Total Memory (B)": "536870912", "VRAM Total Used Memory (B)": "0"},
			 "card1": {"Card Series": "RX 7900", "GFX Version": "gfx1100", "VRAM Total Memory (B)": "25753026560", "VRAM Total Used Memory (B)": "0"}}`,
		})}
		h := d.Hardware(context.Background(), false)
		if h.Flavour != FlavourROCm || len(h.Devices) != 1 || len(h.Ignored) != 1 || Propose(h)[LabelLive].Devices != "1" {
			t.Fatalf("%+v", h)
		}
	})
	t.Run("amd on linux arm64 falls to vulkan", func(t *testing.T) {
		d := &Detector{Platform: Platform{"linux", "arm64"}, Run: fakeRunner(map[string]string{
			"vulkaninfo": "GPU0:\n deviceType = PHYSICAL_DEVICE_TYPE_DISCRETE_GPU\n deviceName = Radeon\n",
		})}
		if h := d.Hardware(context.Background(), false); h.Flavour != FlavourVulkan {
			t.Fatalf("%+v", h)
		}
	})
	t.Run("apple silicon", func(t *testing.T) {
		d := &Detector{Platform: Platform{"darwin", "arm64"}, Run: fakeRunner(map[string]string{
			"sysctl -n": "68719476736\n", // both sysctl calls: the brand falls back to the number, fine for a test
		})}
		h := d.Hardware(context.Background(), false)
		if h.Flavour != FlavourMetal || len(h.Devices) != 1 || h.Devices[0].TotalMB != 45875 {
			t.Fatalf("%+v", h)
		}
		if p := Propose(h); p[LabelLive].Flavour != FlavourMetal || p[LabelLive].Devices != "" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("nothing", func(t *testing.T) {
		d := &Detector{Platform: Platform{"windows", "amd64"}, Run: fakeRunner(nil)}
		h := d.Hardware(context.Background(), false)
		if h.Flavour != FlavourCPU || len(h.Devices) != 0 {
			t.Fatalf("%+v", h)
		}
	})
	t.Run("cached until refresh", func(t *testing.T) {
		calls := 0
		d := &Detector{Platform: Platform{"linux", "amd64"}, Run: func(context.Context, string, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("no")
		}}
		d.Hardware(context.Background(), false)
		n := calls
		d.Hardware(context.Background(), false)
		if calls != n {
			t.Fatal("not cached")
		}
		d.Hardware(context.Background(), true)
		if calls == n {
			t.Fatal("refresh ignored")
		}
	})
}

func TestReleases(t *testing.T) {
	for k, r := range Releases {
		for p, m := range r.Assets {
			for f, assets := range m {
				for _, a := range assets {
					if len(a.SHA256) != 64 || a.Size <= 0 || a.Name == "" {
						t.Errorf("%s %s %s: bad asset %+v", k, p, f, a)
					}
				}
			}
		}
	}
	// Every jarvisd target has a llama-server build; darwin/arm64 is Metal (PLAN §3.3).
	for _, p := range []Platform{{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}} {
		if _, err := AssetsFor(KindLlama, p, FlavourCPU); err != nil {
			t.Error(err)
		}
	}
	if _, err := AssetsFor(KindLlama, Platform{"darwin", "arm64"}, FlavourMetal); err != nil {
		t.Error(err)
	}
	// whisper-server builds come from our CI: Metal on macOS, CUDA (+ runtime) and Vulkan on linux.
	if _, err := AssetsFor(KindWhisper, Platform{"darwin", "arm64"}, FlavourMetal); err != nil {
		t.Error(err)
	}
	if got := FlavoursFor(KindWhisper, Platform{"linux", "amd64"}); strings.Join(got, ",") != "cpu,cuda,vulkan" {
		t.Errorf("whisper linux/amd64 flavours %v", got)
	}
	if a, _ := AssetsFor(KindWhisper, Platform{"linux", "amd64"}, FlavourCUDA); len(a) != 2 || !strings.HasPrefix(a[1].Name, "cudart-") {
		t.Errorf("whisper cuda assets %+v", a)
	}
	if r := Releases[KindWhisper]; r.DefaultBaseURL+"/"+r.Tag() != "https://github.com/alexberardi/jarvis-server/releases/download/engines-whisper-"+r.Build {
		t.Errorf("whisper release URL %s/%s", r.DefaultBaseURL, r.Tag())
	}
	if r := Releases[KindLlama]; r.Tag() != r.Build {
		t.Errorf("llama tag %q", r.Tag())
	}
	if _, err := AssetsFor(KindLlama, Platform{"linux", "amd64"}, FlavourMetal); err == nil || !strings.Contains(err.Error(), "available: cpu, cuda, rocm, vulkan") {
		t.Errorf("err = %v", err)
	}
	if AssetsSize(KindLlama, Platform{"linux", "amd64"}, FlavourCUDA) != 171684958+594377393 {
		t.Error("cuda size")
	}
	if f, err := ParseFlavour(" Auto "); f != "" || err != nil {
		t.Error(f, err)
	}
	if _, err := ParseFlavour("opencl"); err == nil {
		t.Error("opencl accepted")
	}
}
