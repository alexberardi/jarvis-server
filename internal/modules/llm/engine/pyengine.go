package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/sysinfo"
)

// Python engines (decision 2026-10-19-mlx-engine): an OpenAI-compatible inference server that
// runs from a self-contained Python environment under the engines dir. jarvisd stays pure Go;
// Python appears only as this downloaded engine, on hosts whose platform gate passes.
//
// Install (Binaries.Fetch): uv, a pinned and sha256-checked release like llama-server's,
// installs a uv-managed CPython into the engine's directory (no system Python, no Homebrew),
// makes a venv beside it and installs the engine's hash-locked package set. The directory is
// marked complete only after an import check passes. Supervision is the resolver's, as for
// llama-server: `python -I -m <module>` on a loopback port, health-checked and restarted.
//
// MLX (mlx_lm.server) was the first kind, vLLM (vllm.go) the second, Transformers
// (transformers.go) the third. An engine may run on several accelerators of one platform
// (Transformers: MPS or CPU on a Mac); each platform and accelerator has its own package set.

// KindMLX is mlx_lm.server: MLX models (Hugging Face directories) on Apple Silicon.
const KindMLX Kind = "mlx-lm"

// KindUV is uv, the tool that builds Python engines' environments. It is a pinned release
// like an engine build, but not an engine, so it isn't in Kinds.
const KindUV Kind = "uv"

// PyEngine pins one Python engine kind.
type PyEngine struct {
	Kind Kind
	// Version is the server package's pinned version; it names the install directory.
	Version string
	// Python is the interpreter version uv installs.
	Python string
	// Requirements is the whole package set as a `uv pip compile --generate-hashes` lock,
	// installed with --require-hashes (wheels only), for every platform without its own in Sets.
	Requirements string
	// Platforms maps each platform the engine runs on to its accelerators, the default first;
	// the accelerator is the install's flavour.
	Platforms map[Platform][]Flavour
	// Sets is the package set of a platform's accelerator where it isn't Requirements (and
	// DownloadBytes, DiskBytes), keyed by SetKey.
	Sets map[string]PySet
	// DirFallback: the engine runs a model directory only when no other engine runs there or
	// the label's dir_engine names it (Transformers runs everywhere, MLX and vLLM do better).
	DirFallback bool
	// Needs says what the engine needs, for the hosts it can't run on.
	Needs string
	// Module is run as `python -I -m Module`; Check is a program that must exit 0 after
	// install (`python -I -c Check`).
	Module, Check string
	// Gate says why the engine can't run on this hardware ("" = it can), for what the
	// platform alone doesn't decide (nil = nothing).
	Gate func(hw Hardware) string
	// DownloadBytes is the package set's download and DiskBytes the most disk the install
	// takes while it runs (0 = small); an install with less free space doesn't start.
	DownloadBytes, DiskBytes int64
	// Args are the server's flags after the module.
	Args func(a PyArgs) []string
	// HealthPath answers 200 once the model is loaded.
	HealthPath string
	// RequestModel is the "model" a request names the loaded model by ("" = its path).
	RequestModel string
	// NoReasoningBudget: the server refuses llama-server's reasoning_budget request field.
	NoReasoningBudget bool
	// Env is added to the server's environment.
	Env map[string]string
	// APIKeyEnv is the variable the server reads an API key from: each instance gets a
	// random one, as llama-server does ("" = none, and the server binds 127.0.0.1 only).
	APIKeyEnv string
	// CacheEnv points each variable at <install dir>/cache/<subdir>, for the caches the
	// server writes at run time.
	CacheEnv map[string]string
	// Tune completes an instance key from the label and the hardware (nil = nothing).
	Tune func(c LabelConfig, hw Hardware, k *instanceKey) error
	// Short prefixes the engine instance's name.
	Short string
}

// PySet is one hash-locked package set of a Python engine.
type PySet struct {
	Requirements string
	// IndexURL is a wheel index uv reads besides PyPI (PyTorch's, for its CUDA builds; "" = none).
	IndexURL string
	// DownloadBytes is the set's download and DiskBytes the most disk its install takes.
	DownloadBytes, DiskBytes int64
}

// SetKey keys PyEngine.Sets: "os/arch/flavour".
func SetKey(p Platform, f Flavour) string { return p.String() + "/" + string(f) }

// Set is the package set the engine installs on p for f.
func (e PyEngine) Set(p Platform, f Flavour) PySet {
	if s, ok := e.Sets[SetKey(p, f)]; ok {
		return s
	}
	return PySet{Requirements: e.Requirements, DownloadBytes: e.DownloadBytes, DiskBytes: e.DiskBytes}
}

// PyArgs is what a Python engine's command line is built from.
type PyArgs struct {
	Model string
	Port  int
	// Flavour is the install's accelerator.
	Flavour Flavour
	// DType and Quantization are the label's (Transformers; "" = the engine's default).
	DType, Quantization string
	// Context is the label's context: the most tokens a reply may take when the request
	// doesn't say (mlx-lm), or the longest sequence (vLLM's --max-model-len). 0 = engine default.
	Context int
	// GPUMemory is the share of each card the engine preallocates (vLLM; 0 = not passed).
	GPUMemory float64
	// Devices is how many cards the engine spans.
	Devices int
	// ToolParser and ReasoningParser are the model's (vLLM).
	ToolParser, ReasoningParser string
}

//go:embed pyengines/mlx-lm.txt
var mlxRequirements string

// PyEngines holds the pinned Python engines.
//
// mlx_lm.server has no API key option, so it binds 127.0.0.1 only (like whisper-server) and
// its CORS allow-list is narrowed to its own origin (the default "*" would let any web page
// read its replies). A request naming another model would make it load that one, from the Hub
// if need be: requests name "default_model", and HF_HUB_OFFLINE keeps it from downloading.
// Its --max-tokens default is 512; it gets the label's context instead, as llama-server
// bounds a reply only by its context.
var PyEngines = map[Kind]PyEngine{
	KindMLX: {
		Kind: KindMLX, Version: "0.32.2", Python: "3.12",
		Requirements: mlxRequirements,
		Platforms:    map[Platform][]Flavour{{"darwin", "arm64"}: {FlavourMetal}},
		Needs:        "Apple Silicon (darwin/arm64, macOS 14 or later)",
		Module:       "mlx_lm.server",
		Check:        "import mlx.core, mlx_lm.server",
		Args: func(pa PyArgs) []string {
			p := strconv.Itoa(pa.Port)
			a := []string{"--model", pa.Model, "--host", "127.0.0.1", "--port", p,
				"--allowed-origins", "http://127.0.0.1:" + p}
			if pa.Context > 0 {
				a = append(a, "--max-tokens", strconv.Itoa(pa.Context))
			}
			return a
		},
		HealthPath:   "/health",
		RequestModel: "default_model",
		Env:          map[string]string{"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1"},
		Short:        "mlx",
	},
}

// pyErrorLine is the exception line that ends a Python traceback's root cause, bare or after
// vLLM's "(EngineCore pid=N) ERROR <time> [file:line] " prefix: why a Python engine exited
// (e.g. vLLM's "ValueError: Free memory on device cuda:0 … is less than desired GPU memory
// utilization"), which the generic error after it doesn't say.
var pyErrorLine = regexp.MustCompile(`(?:^|\] )((?:\w+\.)*\w*(?:Error|Exception): .*)`)

// errorLine is the engines.Spec ErrorLine of a kind (nil = none).
func errorLine(k Kind) *regexp.Regexp {
	if _, ok := k.Python(); ok {
		return pyErrorLine
	}
	return nil
}

// outputTail is the engines.Spec OutputTail of a kind: a Python traceback (vLLM's EngineCore
// one runs past 100 lines) needs more than llama-server's last lines (0 = the default).
func outputTail(k Kind) int {
	if _, ok := k.Python(); ok {
		return 200
	}
	return 0
}

// Python returns a kind's Python engine, if it is one.
func (k Kind) Python() (PyEngine, bool) {
	e, ok := PyEngines[k]
	return e, ok
}

// Build is the pinned build of a kind: a release's build tag or a Python engine's version.
func Build(k Kind) string {
	if e, ok := PyEngines[k]; ok {
		return e.Version
	}
	return Releases[k].Build
}

// supports says whether the engine runs on p with flavour f, and if not, why.
func (e PyEngine) supports(p Platform, f Flavour) error {
	acc, ok := e.Platforms[p]
	if !ok {
		return fmt.Errorf("%s needs %s; this host is %s", e.Kind, e.Needs, p)
	}
	if !slices.Contains(acc, f) {
		if len(acc) == 1 {
			return fmt.Errorf("%s runs on %s only, not %s", e.Kind, acc[0], f)
		}
		return fmt.Errorf("%s runs on %s here, not %s", e.Kind, flavourList(acc), f)
	}
	return nil
}

// flavourFor is the accelerator a label runs the engine on p with: its gpu_backend, or the
// platform's default when that is "" (auto).
func (e PyEngine) flavourFor(p Platform, backend Flavour) (Flavour, error) {
	if backend == "" {
		if acc := e.Platforms[p]; len(acc) > 0 {
			backend = acc[0]
		}
	}
	return backend, e.supports(p, backend)
}

func flavourList(fs []Flavour) string {
	s := make([]string, len(fs))
	for i, f := range fs {
		s[i] = string(f)
	}
	return strings.Join(s, " or ")
}

// Unavailable says why a kind can't be installed on p ("" = it can).
func Unavailable(k Kind, p Platform) string {
	if e, ok := PyEngines[k]; ok {
		if _, ok := e.Platforms[p]; !ok {
			return e.supports(p, "").Error()
		}
		return ""
	}
	if len(FlavoursFor(k, p)) == 0 {
		return fmt.Sprintf("no %s build for %s", k, p)
	}
	return ""
}

// UnavailableOn is Unavailable on detected hardware: a Python engine's Gate as well.
func UnavailableOn(k Kind, p Platform, hw Hardware) string {
	if why := Unavailable(k, p); why != "" {
		return why
	}
	if e, ok := PyEngines[k]; ok && e.Gate != nil {
		return e.Gate(hw)
	}
	return ""
}

// DirEngineAuto is the dir_engine setting's default: the platform's engine for a directory.
const DirEngineAuto = "auto"

// DirEngine is the engine for a model that is a directory (Hugging Face layout: config,
// safetensors, tokenizer) on p: the one choice names ("" or auto = the first Python engine
// that runs there, an engine with DirFallback only when no other does).
func DirEngine(p Platform, choice string) (Kind, error) {
	if choice = strings.ToLower(strings.TrimSpace(choice)); choice != "" && choice != DirEngineAuto {
		k := Kind(choice)
		if _, ok := PyEngines[k]; !ok {
			return "", fmt.Errorf("dir_engine %q: not an engine for a model directory (%s)", choice, strings.Join(DirEngineChoices(), ", "))
		}
		if why := Unavailable(k, p); why != "" {
			return "", errors.New(why)
		}
		return k, nil
	}
	var kinds []Kind
	for k := range PyEngines {
		kinds = append(kinds, k)
	}
	slices.SortFunc(kinds, func(a, b Kind) int {
		if fa, fb := PyEngines[a].DirFallback, PyEngines[b].DirFallback; fa != fb {
			if fa {
				return 1
			}
			return -1
		}
		return strings.Compare(string(a), string(b))
	})
	var why []string
	for _, k := range kinds {
		if _, ok := PyEngines[k].Platforms[p]; ok {
			return k, nil
		}
		why = append(why, Unavailable(k, p))
	}
	return "", fmt.Errorf("no engine for a model directory runs here: %s", strings.Join(why, "; "))
}

// DirEngineChoices are the dir_engine setting's values: auto and every Python engine.
func DirEngineChoices() []string {
	out := []string{DirEngineAuto}
	for k := range PyEngines {
		out = append(out, string(k))
	}
	slices.Sort(out[1:])
	return out
}

// pythonIn is a Python engine install's interpreter on p.
func pythonIn(p Platform, dir string) string {
	if p.OS == "windows" {
		return filepath.Join(dir, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(dir, "venv", "bin", "python")
}

// installPoll is how often a package install's progress is measured.
var installPoll = 5 * time.Second

// fetchPython installs a Python engine (see the top of this file). progress covers uv's
// download, then the package step as bytes on disk out of DiskBytes; the steps are logged.
func (b *Binaries) fetchPython(ctx context.Context, e PyEngine, f Flavour, progress func(done, total int64)) (string, error) {
	if err := e.supports(b.Platform, f); err != nil {
		return "", err
	}
	l := b.lock(string(e.Kind) + "/" + string(f))
	l.Lock()
	defer l.Unlock()
	dir := b.InstallDir(e.Kind, f)
	py := pythonIn(b.Platform, dir)
	if fileExists(filepath.Join(dir, completeMarker)) && fileExists(py) {
		return py, nil
	}
	set := e.Set(b.Platform, f)
	if err := b.checkDisk(e, set); err != nil {
		return "", err
	}
	uv, err := b.Fetch(ctx, KindUV, FlavourCPU, progress)
	if err != nil {
		return "", fmt.Errorf("fetch %s: uv: %w", e.Kind, err)
	}
	// Start clean: a half-built env from an interrupted install is not reused. uv's cache
	// (downloaded Python and wheels) is, so a retry doesn't download them again.
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	req := filepath.Join(dir, "requirements.txt")
	if err := os.WriteFile(req, []byte(set.Requirements), 0o644); err != nil {
		return "", err
	}
	cache := filepath.Join(b.Dir, string(e.Kind), ".uv-cache")
	env := pythonInstallEnv(map[string]string{
		"UV_PYTHON_INSTALL_DIR": filepath.Join(dir, "python"),
		"UV_CACHE_DIR":          cache,
		"UV_PYTHON_PREFERENCE":  "only-managed", // never a system or Homebrew Python
		"UV_NO_CONFIG":          "1",            // no uv.toml from the user's home
		"UV_NO_PROGRESS":        "1",
	})
	pip := []string{"pip", "install", "--python", py, "--require-hashes", "--only-binary", ":all:"}
	if set.IndexURL != "" {
		// PyPI and the extra index: a package's pinned version from whichever has it.
		pip = append(pip, "--extra-index-url", set.IndexURL, "--index-strategy", "unsafe-best-match")
	}
	steps := []struct {
		name string
		bin  string
		args []string
	}{
		{"python", uv, []string{"python", "install", "--no-bin", e.Python}}, // no python3.x shims in ~/.local/bin
		{"venv", uv, []string{"venv", "--python", e.Python, filepath.Join(dir, "venv")}},
		{"packages", uv, append(pip, "-r", req)},
		{"check", py, []string{"-I", "-c", e.Check}},
	}
	for _, s := range steps {
		if b.Log != nil {
			b.Log.Info("engine install step", "kind", e.Kind, "version", e.Version, "step", s.name)
		}
		cmd := exec.CommandContext(ctx, s.bin, s.args...)
		cmd.Env, cmd.Dir = env, dir
		if s.name == "check" {
			// The engine's own environment: importing transformers' CLI calls the Hub unless
			// HF_HUB_OFFLINE is set.
			cmd.Env = slices.Clone(env)
			for k, v := range e.Env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
		}
		var stop func()
		if s.name == "packages" && set.DiskBytes > 0 {
			stop = b.watchInstall(e, set, []string{cache, dir}, progress)
		}
		out, err := cmd.CombinedOutput()
		if stop != nil {
			stop()
		}
		if err != nil {
			return "", fmt.Errorf("fetch %s %s: %s: %w: %s", e.Kind, e.Version, s.name, err, tail(string(out), 2000))
		}
	}
	meta, _ := json.Marshal(map[string]any{"kind": e.Kind, "version": e.Version, "flavour": f, "python": e.Python,
		"uv": Releases[KindUV].Build, "installed_at": time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(dir, completeMarker), meta, 0o644); err != nil {
		return "", err
	}
	ok = true
	os.RemoveAll(cache)
	if b.Log != nil {
		b.Log.Info("engine installed", "kind", e.Kind, "version", e.Version, "flavour", f, "dir", dir)
	}
	return py, nil
}

// checkDisk refuses an install the engines dir hasn't the space for.
func (b *Binaries) checkDisk(e PyEngine, set PySet) error {
	if set.DiskBytes == 0 {
		return nil
	}
	free := b.DiskFree
	if free == nil {
		free = sysinfo.DiskFree
	}
	os.MkdirAll(b.Dir, 0o755)
	if have := free(b.Dir); have > 0 && have < uint64(set.DiskBytes) {
		return fmt.Errorf("fetch %s %s: needs about %s free in %s (a %s download); %s free",
			e.Kind, e.Version, gb(set.DiskBytes), b.Dir, gb(set.DownloadBytes), gb(int64(have)))
	}
	return nil
}

// watchInstall reports the package step's bytes on disk under dirs to progress (out of
// DiskBytes) and to the log, until the returned stop is called.
func (b *Binaries) watchInstall(e PyEngine, set PySet, dirs []string, progress func(done, total int64)) (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(installPoll)
		defer t.Stop()
		logged := time.Now()
		for {
			select {
			case <-done:
				return
			case <-t.C:
			}
			var n int64
			for _, d := range dirs {
				n += dirSize(d)
			}
			n = min(n, set.DiskBytes)
			if progress != nil {
				progress(n, set.DiskBytes)
			}
			if b.Log != nil && time.Since(logged) >= 30*time.Second {
				logged = time.Now()
				b.Log.Info("engine install progress", "kind", e.Kind, "version", e.Version,
					"on_disk", gb(n), "of_about", gb(set.DiskBytes))
			}
		}
	}()
	return func() { close(done); <-finished }
}

// dirSize is the bytes of the regular files under dir.
func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// gb formats bytes as gigabytes, e.g. "3.9 GB".
func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// pythonInstallEnv is jarvisd's environment without the variables that would point uv or
// Python somewhere else (a user's venv, conda, PYTHONPATH, a pip config), plus set.
func pythonInstallEnv(set map[string]string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, mine := set[k]; mine {
			continue
		}
		drop := false
		for _, p := range []string{"UV_", "PYTHON", "PIP_", "VIRTUAL_ENV", "CONDA_"} {
			if strings.HasPrefix(k, p) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// tail is the end of s, at most n bytes.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
