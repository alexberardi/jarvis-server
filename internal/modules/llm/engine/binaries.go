package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
)

// Binaries manages engine builds on disk under <home>/engines/<kind>/<build>-<flavour>/:
// which are installed, and fetching a missing one (download, sha256 check, extract).
type Binaries struct {
	Dir      string // <home>/engines
	Platform Platform
	Client   *http.Client
	Log      *slog.Logger
	// BaseURL returns the release base URL for a kind ("" = the release default).
	BaseURL func(Kind) string
	// Override returns an operator-installed binary to use instead of a download, e.g.
	// Homebrew's whisper-server on macOS ("" = none).
	Override func(Kind) string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewBinaries returns the store rooted at <home>/engines.
func NewBinaries(home string, log *slog.Logger) *Binaries {
	return &Binaries{Dir: filepath.Join(home, "engines"), Platform: Host(), Client: http.DefaultClient, Log: log}
}

// InstallDir is where a flavour of a kind lives.
func (b *Binaries) InstallDir(k Kind, f Flavour) string {
	return filepath.Join(b.Dir, string(k), Releases[k].Build+"-"+string(f))
}

const completeMarker = ".jarvis-complete"

// Path returns the binary to run for a kind and flavour, if one is available: the operator
// override, else an installed build.
func (b *Binaries) Path(k Kind, f Flavour) (string, bool) {
	if b.Override != nil {
		if p := b.Override(k); p != "" {
			if _, err := os.Stat(p); err == nil {
				return p, true
			}
		}
	}
	dir := b.InstallDir(k, f)
	if _, err := os.Stat(filepath.Join(dir, completeMarker)); err != nil {
		return "", false
	}
	p := filepath.Join(dir, k.BinaryName(b.Platform))
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// Installed lists the installed flavours of a kind with their binaries.
func (b *Binaries) Installed(k Kind) map[Flavour]string {
	out := map[Flavour]string{}
	for _, f := range AllFlavours {
		if _, err := os.Stat(filepath.Join(b.InstallDir(k, f), completeMarker)); err != nil {
			continue
		}
		if p, ok := b.Path(k, f); ok {
			out[f] = p
		}
	}
	return out
}

// InstalledEngine describes one build on disk, for the hardware API.
type InstalledEngine struct {
	Kind    Kind    `json:"kind"`
	Build   string  `json:"build"`
	Flavour Flavour `json:"flavour"`
	Path    string  `json:"path"`
	Pinned  bool    `json:"pinned"` // the build this jarvisd version runs
}

// List reports every engine build on disk, including old builds no longer pinned.
func (b *Binaries) List() []InstalledEngine {
	var out []InstalledEngine
	for _, k := range Kinds {
		entries, _ := os.ReadDir(filepath.Join(b.Dir, string(k)))
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			build, fl, ok := strings.Cut(e.Name(), "-")
			if !ok {
				continue
			}
			dir := filepath.Join(b.Dir, string(k), e.Name())
			if _, err := os.Stat(filepath.Join(dir, completeMarker)); err != nil {
				continue
			}
			out = append(out, InstalledEngine{Kind: k, Build: build, Flavour: Flavour(fl),
				Path: filepath.Join(dir, k.BinaryName(b.Platform)), Pinned: build == Releases[k].Build})
		}
	}
	return out
}

func (b *Binaries) lock(key string) *sync.Mutex {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.locks == nil {
		b.locks = map[string]*sync.Mutex{}
	}
	if b.locks[key] == nil {
		b.locks[key] = &sync.Mutex{}
	}
	return b.locks[key]
}

// Fetch installs a kind's flavour if it is missing and returns its binary. progress gets
// bytes downloaded so far out of the flavour's total. Concurrent fetches of the same build
// wait for one another; downloads resume from their .part files after an interruption.
func (b *Binaries) Fetch(ctx context.Context, k Kind, f Flavour, progress func(done, total int64)) (string, error) {
	assets, err := AssetsFor(k, b.Platform, f)
	if err != nil {
		return "", err
	}
	l := b.lock(string(k) + "/" + string(f))
	l.Lock()
	defer l.Unlock()
	dir := b.InstallDir(k, f)
	if _, err := os.Stat(filepath.Join(dir, completeMarker)); err == nil {
		if p := filepath.Join(dir, k.BinaryName(b.Platform)); fileExists(p) {
			return p, nil
		}
	}
	base := ""
	if b.BaseURL != nil {
		base = b.BaseURL(k)
	}
	if base == "" {
		base = Releases[k].DefaultBaseURL
	}
	base = strings.TrimRight(base, "/")
	var total, before int64
	for _, a := range assets {
		total += a.Size
	}
	dl := filepath.Join(b.Dir, string(k), ".download")
	var files []string
	for _, a := range assets {
		dest := filepath.Join(dl, a.Name)
		files = append(files, dest)
		if fileExists(dest) {
			before += a.Size
			if progress != nil {
				progress(before, total)
			}
			continue
		}
		start := before
		err := Download{
			URL: base + "/" + Releases[k].Build + "/" + a.Name, Dest: dest, Size: a.Size, SHA256: a.SHA256,
			Client: b.Client,
			Progress: func(done int64) {
				if progress != nil {
					progress(start+done, total)
				}
			},
		}.Run(ctx)
		if err != nil {
			return "", fmt.Errorf("fetch %s %s: %w", k, f, err)
		}
		before += a.Size
	}
	tmp := filepath.Join(b.Dir, string(k), ".tmp-"+filepath.Base(dir)+"-"+randHex(4))
	defer os.RemoveAll(tmp)
	for _, file := range files {
		if err := extract(file, tmp); err != nil {
			return "", fmt.Errorf("fetch %s %s: %w", k, f, err)
		}
	}
	bin := filepath.Join(tmp, k.BinaryName(b.Platform))
	if !fileExists(bin) {
		return "", fmt.Errorf("fetch %s %s: %s not found in the release archive", k, f, k.BinaryName(b.Platform))
	}
	if err := os.Chmod(bin, 0o755); err != nil {
		return "", err
	}
	meta, _ := json.Marshal(map[string]any{"kind": k, "build": Releases[k].Build, "flavour": f,
		"assets": assets, "installed_at": time.Now().UTC()})
	if err := os.WriteFile(filepath.Join(tmp, completeMarker), meta, 0o644); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	for _, file := range files {
		os.Remove(file)
	}
	if b.Log != nil {
		b.Log.Info("engine installed", "kind", k, "build", Releases[k].Build, "flavour", f, "dir", dir)
	}
	return filepath.Join(dir, k.BinaryName(b.Platform)), nil
}

// Remove deletes an installed build (any version).
func (b *Binaries) Remove(k Kind, build string, f Flavour) error {
	dir := filepath.Join(b.Dir, string(k), build+"-"+string(f))
	if !within(b.Dir, dir) {
		return fs.ErrInvalid
	}
	return os.RemoveAll(dir)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// FetchJobType is the queue job that installs an engine build on its own (when a label is
// switched to a flavour that isn't on disk). Model installs fetch their engine inline.
const FetchJobType = "llm.engine.fetch"

type fetchPayload struct {
	Kind    Kind    `json:"kind"`
	Flavour Flavour `json:"flavour"`
}

// FetchDedupKey is the dedup key of a build's fetch job.
func FetchDedupKey(k Kind, f Flavour) string {
	return "llm.engine:" + string(k) + ":" + Releases[k].Build + "-" + string(f)
}

// RegisterJobs registers the fetch job. onDone runs after a successful fetch (the resolver
// uses it to start engines that were waiting for the binary).
func (b *Binaries) RegisterJobs(q *queue.Queue, onDone func()) {
	q.Register(FetchJobType, queue.Handler{
		Concurrency: 1,
		MaxAttempts: 5,
		Lease:       time.Hour,
		Run: func(ctx context.Context, job queue.Job) ([]byte, error) {
			var p fetchPayload
			if err := json.Unmarshal(job.Payload, &p); err != nil {
				return nil, queue.Permanent(err)
			}
			path, err := b.Fetch(ctx, p.Kind, p.Flavour, nil)
			if err != nil {
				var he *HTTPError
				if errors.As(err, &he) && he.Permanent() {
					return nil, queue.Permanent(err)
				}
				return nil, err
			}
			if onDone != nil {
				onDone()
			}
			return json.Marshal(map[string]string{"path": path})
		},
	})
}

// EnqueueFetch queues a fetch of a build (deduplicated).
func EnqueueFetch(ctx context.Context, q *queue.Queue, k Kind, f Flavour) (int64, error) {
	payload, _ := json.Marshal(fetchPayload{Kind: k, Flavour: f})
	id, err := q.Enqueue(ctx, FetchJobType, payload, queue.Options{DedupKey: FetchDedupKey(k, f)})
	if errors.Is(err, queue.ErrDuplicate) {
		err = nil
	}
	return id, err
}
