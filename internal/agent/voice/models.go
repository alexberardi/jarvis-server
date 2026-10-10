package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Wake-word models: openWakeWord's feature models plus hey_jarvis, exactly the files
// openwakeword 0.6.0 downloads (and scripts/fetch-test-models.sh fetches for the golden
// tests), pinned by SHA-256. They are fetched once into the agent's cache directory
// (ModelsDir) unless --wake-models points at a directory that already has them.

const owwRelease = "https://github.com/dscripka/openWakeWord/releases/download/v0.5.1/"

// WakeModels maps file name -> SHA-256.
var WakeModels = map[string]string{
	"melspectrogram.onnx":  "ba2b0e0f8b7b875369a2c89cb13360ff53bac436f2895cced9f479fa65eb176f",
	"embedding_model.onnx": "70d164290c1d095d1d4ee149bc5e00543250a7316b59f31d056cff7bd3075c1f",
	"hey_jarvis_v0.1.onnx": "94a13cfe60075b132f6a472e7e462e8123ee70861bc3fb58434a73712ee0d2cb",
}

// CacheDir is <user cache dir>/jarvis-agent (~/.cache on Linux, ~/Library/Caches on macOS).
// The extracted onnxruntime and the wake models live under it; both can be re-created.
func CacheDir() (string, error) {
	d, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "jarvis-agent"), nil
}

// ModelsDir is where EnsureWakeModels puts the models by default.
func ModelsDir(cacheDir string) string { return filepath.Join(cacheDir, "openwakeword") }

// EnsureWakeModels makes sure dir holds every wake model with the pinned hash, downloading
// what's missing or wrong from baseURL ("" means openWakeWord's v0.5.1 release).
func EnsureWakeModels(ctx context.Context, dir, baseURL string, hc *http.Client) (fetched []string, err error) {
	return ensureModels(ctx, dir, baseURL, hc, WakeModels)
}

func ensureModels(ctx context.Context, dir, baseURL string, hc *http.Client, models map[string]string) (fetched []string, err error) {
	if baseURL == "" {
		baseURL = owwRelease
	}
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for name, want := range models {
		path := filepath.Join(dir, name)
		if got, err := fileSHA256(path); err == nil && got == want {
			continue
		}
		if err := download(ctx, hc, strings.TrimRight(baseURL, "/")+"/"+name, path, want); err != nil {
			return fetched, fmt.Errorf("wake model %s: %w", name, err)
		}
		fetched = append(fetched, name)
	}
	return fetched, nil
}

// CheckWakeModels reports the first missing or altered model in dir.
func CheckWakeModels(dir string) error {
	for name, want := range WakeModels {
		got, err := fileSHA256(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("wake model %s: %w", name, err)
		}
		if got != want {
			return fmt.Errorf("wake model %s in %s has sha256 %s, want %s", name, dir, got, want)
		}
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func download(ctx context.Context, hc *http.Client, url, path, want string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 64<<20)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("GET %s: sha256 %s, want %s", url, got, want)
	}
	return os.Rename(tmp.Name(), path)
}

// LocalTimezone is this machine's IANA zone name for node_context: $TZ, else the
// /etc/localtime symlink target (Linux and macOS), else "UTC".
func LocalTimezone() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" && !strings.HasPrefix(tz, "/") {
		return tz
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(target, "zoneinfo/"); ok && zone != "" {
			return zone
		}
	}
	if n := time.Local.String(); strings.Contains(n, "/") {
		return n
	}
	return "UTC"
}
