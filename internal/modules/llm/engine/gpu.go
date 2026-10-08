package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// Device is one accelerator an engine can use.
type Device struct {
	// Backend is the flavour that drives it: cuda, rocm, vulkan or metal.
	Backend Flavour `json:"backend"`
	// Index is the device's position in the backend's own enumeration, which is what the
	// visibility variables (CUDA_VISIBLE_DEVICES, …) take.
	Index int    `json:"index"`
	ID    string `json:"id"` // llama.cpp's device name, e.g. "CUDA0", "Vulkan1", "MTL0"
	Name  string `json:"name"`
	// TotalMB and FreeMB are the device's memory. On Apple silicon (unified memory) TotalMB is
	// the share a model may use, about 70% of RAM, and FreeMB is the same figure.
	TotalMB    int64 `json:"total_mb"`
	FreeMB     int64 `json:"free_mb"`
	Integrated bool  `json:"integrated,omitempty"`
}

// Hardware is a detection result. Detection only proposes: settings decide.
type Hardware struct {
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Devices []Device `json:"devices"`
	// Ignored lists integrated or software devices that detection saw and excluded.
	Ignored []Device `json:"ignored,omitempty"`
	// Sources names the tools that answered, e.g. ["llama-server --list-devices", "nvidia-smi"].
	Sources []string `json:"sources"`
	// Flavour is the proposed engine build for this machine.
	Flavour Flavour `json:"flavour"`
	// RAMMB is the host's physical memory, what a model on the CPU is judged against (0 =
	// unknown).
	RAMMB      int64     `json:"ram_mb,omitempty"`
	DetectedAt time.Time `json:"detected_at"`
}

// Discrete returns the usable devices of one backend, largest first.
func (h Hardware) Discrete(b Flavour) []Device {
	var out []Device
	for _, d := range h.Devices {
		if d.Backend == b && !d.Integrated {
			out = append(out, d)
		}
	}
	slices.SortStableFunc(out, func(a, b Device) int { return int(b.TotalMB - a.TotalMB) })
	return out
}

// listDevicesLine matches llama-server --list-devices output:
//
//	CUDA0: NVIDIA GeForce RTX 3090 (24135 MiB, 23700 MiB free)
var listDevicesLine = regexp.MustCompile(`^\s*([A-Za-z]+?)(\d*):\s+(.+?)\s+\((\d+) MiB, (\d+) MiB free\)\s*$`)

// ParseListDevices parses `llama-server --list-devices`. Unknown backends (SYCL, OpenCL, RPC…)
// are skipped.
func ParseListDevices(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		m := listDevicesLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		var b Flavour
		switch strings.ToLower(m[1]) {
		case "cuda":
			b = FlavourCUDA
		case "rocm", "hip":
			b = FlavourROCm
		case "vulkan":
			b = FlavourVulkan
		case "metal", "mtl":
			b = FlavourMetal
		default:
			continue
		}
		idx, _ := strconv.Atoi(m[2])
		total, _ := strconv.ParseInt(m[4], 10, 64)
		free, _ := strconv.ParseInt(m[5], 10, 64)
		d := Device{Backend: b, Index: idx, ID: m[1] + m[2], Name: m[3], TotalMB: total, FreeMB: free}
		d.Integrated = isIntegratedName(d.Name)
		devs = append(devs, d)
	}
	return devs
}

// ParseNvidiaSMI parses `nvidia-smi --query-gpu=index,name,memory.total,memory.free
// --format=csv,noheader,nounits`.
func ParseNvidiaSMI(out string) []Device {
	var devs []Device
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ",")
		if len(f) < 4 {
			continue
		}
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		idx, err1 := strconv.Atoi(f[0])
		total, err2 := strconv.ParseInt(f[2], 10, 64)
		free, err3 := strconv.ParseInt(f[3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		devs = append(devs, Device{Backend: FlavourCUDA, Index: idx, ID: fmt.Sprintf("CUDA%d", idx),
			Name: f[1], TotalMB: total, FreeMB: free})
	}
	return devs
}

// apuArchs are the gfx targets of AMD APU iGPUs (ported from gpu_select.py).
var apuArchs = []string{"gfx1036", "gfx1037", "gfx1035", "gfx1103", "gfx1150", "gfx1151",
	"gfx90c", "gfx1013", "gfx103c", "gfx1033", "gfx1034"}

func isAPUArch(gfx string) bool {
	for _, a := range apuArchs {
		if strings.HasPrefix(gfx, a) {
			return true
		}
	}
	return false
}

func isIntegratedName(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "llvmpipe") || strings.Contains(n, "integrated") ||
		strings.Contains(n, "software rasterizer") || strings.Contains(n, "lavapipe")
}

// ParseROCmSMI parses `rocm-smi --showproductname --showmeminfo vram --json`. Cards are
// numbered in HIP order; APU gfx targets are marked integrated.
func ParseROCmSMI(out string) []Device {
	var raw map[string]map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		if strings.HasPrefix(k, "card") {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		ai, _ := strconv.Atoi(strings.TrimPrefix(a, "card"))
		bi, _ := strconv.Atoi(strings.TrimPrefix(b, "card"))
		return ai - bi
	})
	var devs []Device
	for i, k := range keys {
		c := raw[k]
		name := firstNonEmpty(c["Card Series"], c["Card series"], c["Card SKU"], c["Card Model"], "AMD GPU")
		total, _ := strconv.ParseInt(c["VRAM Total Memory (B)"], 10, 64)
		used, _ := strconv.ParseInt(c["VRAM Total Used Memory (B)"], 10, 64)
		gfx := firstNonEmpty(c["GFX Version"], c["gfx_version"])
		devs = append(devs, Device{Backend: FlavourROCm, Index: i, ID: fmt.Sprintf("ROCm%d", i), Name: name,
			TotalMB: total >> 20, FreeMB: (total - used) >> 20,
			Integrated: isAPUArch(gfx) || isIntegratedName(name)})
	}
	return devs
}

// ParseVulkanInfo parses `vulkaninfo --summary`: GPU<n> blocks with deviceType and deviceName.
// It knows no memory sizes (llama-server --list-devices fills those in once a Vulkan build is
// installed); non-discrete devices are marked integrated.
func ParseVulkanInfo(out string) []Device {
	var devs []Device
	var cur *Device
	gpuHdr := regexp.MustCompile(`^GPU(\d+):`)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if m := gpuHdr.FindStringSubmatch(line); m != nil {
			idx, _ := strconv.Atoi(m[1])
			devs = append(devs, Device{Backend: FlavourVulkan, Index: idx, ID: "Vulkan" + m[1], Integrated: true})
			cur = &devs[len(devs)-1]
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "deviceType":
			cur.Integrated = v != "PHYSICAL_DEVICE_TYPE_DISCRETE_GPU"
		case "deviceName":
			cur.Name = v
		}
	}
	return devs
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// Runner runs a detection tool and returns its stdout. Detection is subprocess-only: probing a
// GPU library in-process can crash jarvisd (01 §7.6).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil && !strings.ContainsAny(name, `/\`) {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...).Output()
}

// Detector finds GPUs. The primary source is `llama-server --list-devices` from each
// installed engine build (exactly what that build can use); vendor tools drive the flavour
// choice before any engine is installed.
type Detector struct {
	Platform Platform
	Run      Runner
	// Binaries lists installed llama-server binaries by flavour (nil = none installed).
	Binaries func() map[Flavour]string
	// Timeout bounds each tool; default 20s.
	Timeout time.Duration
	// Memory reports the host's physical RAM in bytes (nil or 0 = unknown).
	Memory func() uint64

	mu     sync.Mutex
	cached *Hardware
}

// NewDetector returns a detector for this host.
func NewDetector(binaries func() map[Flavour]string) *Detector {
	return &Detector{Platform: Host(), Run: execRunner, Binaries: binaries, Memory: sysinfo.TotalMemory}
}

// Hardware returns the last detection, detecting first if there is none or refresh is set.
func (d *Detector) Hardware(ctx context.Context, refresh bool) Hardware {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil || refresh {
		h := d.detect(ctx)
		d.cached = &h
	}
	return *d.cached
}

func (d *Detector) run(ctx context.Context, name string, args ...string) (string, bool) {
	t := d.Timeout
	if t <= 0 {
		t = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	out, err := d.Run(ctx, name, args...)
	if err != nil && len(out) == 0 {
		return "", false
	}
	return string(out), true
}

func (d *Detector) detect(ctx context.Context) Hardware {
	h := Hardware{OS: d.Platform.OS, Arch: d.Platform.Arch, DetectedAt: time.Now().UTC()}
	if d.Memory != nil {
		h.RAMMB = int64(d.Memory() >> 20)
	}
	add := func(src string, devs []Device) {
		if len(devs) == 0 {
			return
		}
		h.Sources = append(h.Sources, src)
		for _, dev := range devs {
			dup := slices.ContainsFunc(h.Devices, func(x Device) bool { return x.Backend == dev.Backend && x.Index == dev.Index }) ||
				slices.ContainsFunc(h.Ignored, func(x Device) bool { return x.Backend == dev.Backend && x.Index == dev.Index })
			if dup {
				continue
			}
			if dev.Integrated {
				h.Ignored = append(h.Ignored, dev)
			} else {
				h.Devices = append(h.Devices, dev)
			}
		}
	}

	// Installed engine builds first: they report memory for every backend alike.
	if d.Binaries != nil {
		bins := d.Binaries()
		for _, f := range AllFlavours {
			if p, ok := bins[f]; ok && f != FlavourCPU {
				if out, ok := d.run(ctx, p, "--list-devices"); ok {
					add("llama-server --list-devices ("+string(f)+")", ParseListDevices(out))
				}
			}
		}
	}

	if d.Platform.OS == "darwin" {
		if d.Platform.Arch == "arm64" && !slices.ContainsFunc(h.Devices, func(x Device) bool { return x.Backend == FlavourMetal }) {
			add("sysctl", d.appleSilicon(ctx))
		}
	} else {
		if out, ok := d.run(ctx, "nvidia-smi", "--query-gpu=index,name,memory.total,memory.free", "--format=csv,noheader,nounits"); ok {
			add("nvidia-smi", ParseNvidiaSMI(out))
		}
		if out, ok := d.run(ctx, "rocm-smi", "--showproductname", "--showmeminfo", "vram", "--json"); ok {
			add("rocm-smi", ParseROCmSMI(out))
		}
		if out, ok := d.run(ctx, "vulkaninfo", "--summary"); ok {
			add("vulkaninfo", ParseVulkanInfo(out))
		}
	}
	h.Flavour = proposeFlavour(d.Platform, h)
	return h
}

// appleSilicon reports the Metal device from sysctl: unified memory, ~70% usable for a model.
func (d *Detector) appleSilicon(ctx context.Context) []Device {
	out, ok := d.run(ctx, "sysctl", "-n", "hw.memsize")
	if !ok {
		return nil
	}
	mem, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return nil
	}
	name := "Apple silicon"
	if n, ok := d.run(ctx, "sysctl", "-n", "machdep.cpu.brand_string"); ok && strings.TrimSpace(n) != "" {
		name = strings.TrimSpace(n)
	}
	usable := mem * 7 / 10 >> 20
	return []Device{{Backend: FlavourMetal, Index: 0, ID: "MTL0", Name: name, TotalMB: usable, FreeMB: usable}}
}

// proposeFlavour picks the engine build: Metal on Apple silicon; CUDA for NVIDIA; ROCm for
// AMD where upstream ships it, else Vulkan; Vulkan for other discrete GPUs; else CPU.
func proposeFlavour(p Platform, h Hardware) Flavour {
	has := func(b Flavour) bool { return len(h.Discrete(b)) > 0 }
	avail := func(f Flavour) bool { _, ok := Releases[KindLlama].Assets[p][f]; return ok }
	switch {
	case p.OS == "darwin" && p.Arch == "arm64":
		return FlavourMetal
	case has(FlavourCUDA) && avail(FlavourCUDA):
		return FlavourCUDA
	case has(FlavourROCm) && avail(FlavourROCm):
		return FlavourROCm
	case (has(FlavourROCm) || has(FlavourVulkan)) && avail(FlavourVulkan):
		return FlavourVulkan
	}
	return FlavourCPU
}

// Placement is a proposed or effective device assignment for one label.
type Placement struct {
	Flavour Flavour `json:"gpu_backend"`
	Devices string  `json:"gpu_devices"` // e.g. "1" or "0,1"; "" = all visible / none
}

// Propose suggests placements for the live and background labels (01 §3.4): one GPU → both on
// it (the user shares one engine by giving both labels the same model); two or more → live on
// the largest card, background on the next; Apple → Metal; none → CPU.
func Propose(h Hardware) map[string]Placement {
	f := h.Flavour
	if f == "" {
		f = FlavourCPU
	}
	out := map[string]Placement{
		LabelLive:       {Flavour: f},
		LabelBackground: {Flavour: f},
		LabelEmbeddings: {Flavour: f},
	}
	devs := h.Discrete(f)
	if f == FlavourMetal || f == FlavourCPU || len(devs) == 0 {
		return out
	}
	out[LabelLive] = Placement{Flavour: f, Devices: strconv.Itoa(devs[0].Index)}
	bg := devs[0]
	if len(devs) > 1 {
		bg = devs[1]
	}
	out[LabelBackground] = Placement{Flavour: f, Devices: strconv.Itoa(bg.Index)}
	// Embeddings stay on the CPU by default (gpu_layers 0): MiniLM is tiny.
	return out
}
