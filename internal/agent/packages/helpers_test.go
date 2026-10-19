package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// tarball is a GitHub-style archive: every file under one top directory.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "abc"}})
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top + "/", Mode: 0o755})
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		body := files[n]
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: top + "/" + n, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// repos serves archives at /{owner}/{repo}/archive/{ref}.tar.gz.
type repos struct {
	mu    sync.Mutex
	files map[string][]byte // "owner/repo@ref" -> tarball
	srv   *httptest.Server
}

func newRepos(t *testing.T) *repos {
	r := &repos{files: map[string][]byte{}}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(req.URL.Path, "/"), "/archive/", 2)
		if len(parts) != 2 {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		b, ok := r.files[parts[0]+"@"+strings.TrimSuffix(parts[1], ".tar.gz")]
		r.mu.Unlock()
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *repos) add(t *testing.T, repo, ref string, files map[string]string) string {
	r.mu.Lock()
	r.files[repo+"@"+ref] = tarball(t, strings.ReplaceAll(repo, "/", "-")+"-"+ref, files)
	r.mu.Unlock()
	return r.srv.URL + "/" + repo
}

// fakeRunner stands in for uv: "venv" makes the interpreter, "pip install" is recorded.
type fakeRunner struct {
	mu      sync.Mutex
	py      *Python
	calls   [][]string
	pipFail error
}

func (f *fakeRunner) run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string{name}, args...))
	switch args[0] {
	case "venv":
		p := f.py.Interpreter()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		return nil, os.WriteFile(p, []byte("#!python"), 0o755)
	case "pip":
		if f.pipFail != nil {
			return []byte("resolution failed: no such package"), f.pipFail
		}
	}
	return nil, nil
}

// fakeSidecar is a started sidecar: the packages its commands file listed, and which failed.
type fakeSidecar struct {
	packages []string
	failed   map[string]string
	closed   bool
}

func (s *fakeSidecar) Failed() map[string]string { return s.failed }
func (s *fakeSidecar) Close() error              { s.closed = true; return nil }

// fakeSidecars starts fakeSidecars from the real commands file; packages named in fail don't
// load.
type fakeSidecars struct {
	fail     map[string]string
	startErr error
	started  []*fakeSidecar
	live     Sidecar
	swaps    int
}

func (f *fakeSidecars) Start(_ context.Context, python, file string) (Sidecar, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	if _, err := os.Stat(python); err != nil {
		return nil, errors.New("no interpreter")
	}
	list, err := readList(file)
	if err != nil {
		return nil, err
	}
	sc := &fakeSidecar{failed: map[string]string{}}
	for _, e := range list {
		sc.packages = append(sc.packages, e.Name)
		if why, ok := f.fail[e.Name]; ok {
			sc.failed[e.Name] = why
		}
	}
	f.started = append(f.started, sc)
	return sc, nil
}

func (f *fakeSidecars) Swap(sc Sidecar) {
	f.swaps++
	if old, ok := f.live.(*fakeSidecar); ok && old != nil {
		old.closed = true
	}
	f.live = sc
}

func (f *fakeSidecars) livePackages() []string {
	if sc, ok := f.live.(*fakeSidecar); ok && sc != nil {
		return sc.packages
	}
	return nil
}

type listEntry struct {
	Name     string   `json:"name"`
	Root     string   `json:"root"`
	Commands []string `json:"commands"`
	Families []string `json:"families"`
	Managers []string `json:"managers"`
}

func readList(file string) ([]listEntry, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var v struct {
		Packages []listEntry `json:"packages"`
	}
	err = json.Unmarshal(b, &v)
	return v.Packages, err
}

// newTestManager is a Manager over a temp root, the fake uv and fake sidecars.
func newTestManager(t *testing.T, r *repos) (*Manager, *fakeRunner, *fakeSidecars) {
	t.Helper()
	root := t.TempDir()
	py := &Python{Root: root, UV: "uv", GOOS: "linux", GOARCH: "amd64"}
	fr := &fakeRunner{py: py}
	py.Run = fr.run
	sc := &fakeSidecars{}
	m := &Manager{Root: root, Python: py, HTTP: r.srv.Client(), GOOS: "linux", Sidecars: sc, Log: quiet}
	return m, fr, sc
}

// weather is a package like the Pantry's get_weather_meteo: a command and an agent.
func weather(version string) map[string]string {
	return map[string]string{
		"jarvis_package.yaml": `name: "get_weather_meteo"
display_name: "Weather (Open-Meteo)"
version: "` + version + `"
platforms: ["darwin", "linux"]
components:
  - type: command
    name: get_weather_meteo
    path: commands/get_weather/command.py
  - type: agent
    name: weather_context
    path: agents/weather_context/agent.py
packages:
  - name: httpx
  - name: tzdata
    version: "2024.1"
`,
		"commands/get_weather/command.py":    "# " + version,
		"commands/get_weather/__init__.py":   "",
		"agents/weather_context/agent.py":    "",
		"agents/weather_context/__init__.py": "",
	}
}
