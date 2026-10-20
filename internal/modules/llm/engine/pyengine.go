package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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
// MLX (mlx_lm.server) is the first kind; vLLM and Transformers are meant to follow as further
// entries of PyEngines.

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
	// installed with --require-hashes (wheels only).
	Requirements string
	// Platforms maps each platform the engine runs on to its accelerator, which is the
	// install's flavour.
	Platforms map[Platform]Flavour
	// Needs says what the engine needs, for the hosts it can't run on.
	Needs string
	// Module is run as `python -I -m Module`; Check is a program that must exit 0 after
	// install (`python -I -c Check`).
	Module, Check string
	// Args are the server's flags after the module: the model, the loopback port and the
	// most tokens a reply may take when the request doesn't say (ctx, 0 = engine default).
	Args func(model string, port, ctx int) []string
	// HealthPath answers 200 once the model is loaded.
	HealthPath string
	// RequestModel is the "model" a request names the loaded model by.
	RequestModel string
	// Env is added to the server's environment.
	Env map[string]string
	// Short prefixes the engine instance's name.
	Short string
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
		Platforms:    map[Platform]Flavour{{"darwin", "arm64"}: FlavourMetal},
		Needs:        "Apple Silicon (darwin/arm64, macOS 14 or later)",
		Module:       "mlx_lm.server",
		Check:        "import mlx.core, mlx_lm.server",
		Args: func(model string, port, ctx int) []string {
			p := strconv.Itoa(port)
			a := []string{"--model", model, "--host", "127.0.0.1", "--port", p,
				"--allowed-origins", "http://127.0.0.1:" + p}
			if ctx > 0 {
				a = append(a, "--max-tokens", strconv.Itoa(ctx))
			}
			return a
		},
		HealthPath:   "/health",
		RequestModel: "default_model",
		Env:          map[string]string{"HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "HF_HUB_DISABLE_TELEMETRY": "1"},
		Short:        "mlx",
	},
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
	if f != acc {
		return fmt.Errorf("%s runs on %s only, not %s", e.Kind, acc, f)
	}
	return nil
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

// DirEngine is the engine for a model that is a directory (Hugging Face layout: config,
// safetensors, tokenizer) on p: the first Python engine that runs there.
func DirEngine(p Platform) (Kind, error) {
	var kinds []Kind
	for k := range PyEngines {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	var why []string
	for _, k := range kinds {
		if _, ok := PyEngines[k].Platforms[p]; ok {
			return k, nil
		}
		why = append(why, Unavailable(k, p))
	}
	return "", fmt.Errorf("no engine for a model directory runs here: %s", strings.Join(why, "; "))
}

// pythonIn is a Python engine install's interpreter.
func pythonIn(dir string) string { return filepath.Join(dir, "venv", "bin", "python") }

// fetchPython installs a Python engine (see the top of this file). progress covers uv's
// download; the Python and package steps are logged.
func (b *Binaries) fetchPython(ctx context.Context, e PyEngine, f Flavour, progress func(done, total int64)) (string, error) {
	if err := e.supports(b.Platform, f); err != nil {
		return "", err
	}
	l := b.lock(string(e.Kind) + "/" + string(f))
	l.Lock()
	defer l.Unlock()
	dir := b.InstallDir(e.Kind, f)
	py := pythonIn(dir)
	if fileExists(filepath.Join(dir, completeMarker)) && fileExists(py) {
		return py, nil
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
	if err := os.WriteFile(req, []byte(e.Requirements), 0o644); err != nil {
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
	steps := []struct {
		name string
		bin  string
		args []string
	}{
		{"python", uv, []string{"python", "install", "--no-bin", e.Python}}, // no python3.x shims in ~/.local/bin
		{"venv", uv, []string{"venv", "--python", e.Python, filepath.Join(dir, "venv")}},
		{"packages", uv, []string{"pip", "install", "--python", py, "--require-hashes", "--only-binary", ":all:", "-r", req}},
		{"check", py, []string{"-I", "-c", e.Check}},
	}
	for _, s := range steps {
		if b.Log != nil {
			b.Log.Info("engine install step", "kind", e.Kind, "version", e.Version, "step", s.name)
		}
		cmd := exec.CommandContext(ctx, s.bin, s.args...)
		cmd.Env, cmd.Dir = env, dir
		if out, err := cmd.CombinedOutput(); err != nil {
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
