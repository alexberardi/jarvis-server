package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// rangeServer serves files with Range support. cutAfter > 0 drops the connection after that
// many body bytes, once per file, to simulate an interrupted download.
type rangeServer struct {
	files    map[string][]byte
	noRange  bool
	cutAfter int
	cut      map[string]bool
	requests atomic.Int32
	ranges   []string
	auth     string
}

func (s *rangeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	if s.auth != "" && r.Header.Get("Authorization") != s.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	data, ok := s.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	start := 0
	if rg := r.Header.Get("Range"); rg != "" && !s.noRange {
		s.ranges = append(s.ranges, rg)
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
		if n >= len(data) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		start = n
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", n, len(data)-1, len(data)))
		w.Header().Set("Content-Length", strconv.Itoa(len(data)-n))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	}
	body := data[start:]
	if s.cutAfter > 0 && !s.cut[r.URL.Path] && len(body) > s.cutAfter {
		if s.cut == nil {
			s.cut = map[string]bool{}
		}
		s.cut[r.URL.Path] = true
		w.Write(body[:s.cutAfter])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	w.Write(body)
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

func TestDownloadResumes(t *testing.T) {
	data := payload(3 << 20)
	srv := &rangeServer{files: map[string][]byte{"/f.gguf": data}, cutAfter: 1 << 20}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "models", "f.gguf")
	var last int64
	d := Download{URL: ts.URL + "/f.gguf", Dest: dest, Size: int64(len(data)), SHA256: sum(data),
		Progress: func(n int64) { last = n }}
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("first attempt should fail mid-way")
	}
	if fileExists(dest) {
		t.Fatal("half a file at the destination")
	}
	if st, err := os.Stat(dest + ".part"); err != nil || st.Size() != 1<<20 {
		t.Fatalf("part: %v %v", st, err)
	}
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) || last != int64(len(data)) {
		t.Fatalf("content mismatch / progress %d", last)
	}
	if len(srv.ranges) != 1 || srv.ranges[0] != "bytes=1048576-" {
		t.Fatalf("ranges %v", srv.ranges)
	}
	if fileExists(dest+".part") || fileExists(dest+".part.sha256") {
		t.Fatal("leftovers")
	}
}

func TestDownloadHashStateResume(t *testing.T) {
	// The interrupted attempt saves the sha256 state; the resume trusts it only when the part
	// file's size matches.
	data := payload(2 << 20)
	srv := &rangeServer{files: map[string][]byte{"/f": data}, cutAfter: 1 << 20}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "f")
	d := Download{URL: ts.URL + "/f", Dest: dest, Size: int64(len(data)), SHA256: sum(data)}
	_ = d.Run(context.Background())
	if !fileExists(dest + ".part.sha256") {
		t.Fatal("no saved hash state")
	}
	// Corrupt the saved state's offset: the resume must rehash instead of trusting it.
	os.WriteFile(dest+".part.sha256", []byte(`{"offset":5,"state":"AAAA"}`), 0o644)
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadNoRangeSupport(t *testing.T) {
	data := payload(1 << 20)
	srv := &rangeServer{files: map[string][]byte{"/f": data}, noRange: true}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "f")
	os.WriteFile(dest+".part", data[:1000], 0o644) // stale partial
	if err := (Download{URL: ts.URL + "/f", Dest: dest, Size: int64(len(data)), SHA256: sum(data)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !bytes.Equal(got, data) {
		t.Fatal("200 answer must restart from zero")
	}
}

func TestDownloadAlreadyComplete(t *testing.T) {
	data := payload(4096)
	srv := &rangeServer{files: map[string][]byte{"/f": data}}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "f")
	os.WriteFile(dest+".part", data, 0o644)
	if err := (Download{URL: ts.URL + "/f", Dest: dest, Size: int64(len(data)), SHA256: sum(data)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.requests.Load() != 0 {
		t.Fatal("a complete part needs no request")
	}
	// Unknown size: a 416 for the next byte means done.
	dest2 := filepath.Join(t.TempDir(), "g")
	os.WriteFile(dest2+".part", data, 0o644)
	if err := (Download{URL: ts.URL + "/f", Dest: dest2, SHA256: sum(data)}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadChecksumMismatch(t *testing.T) {
	data := payload(4096)
	ts := httptest.NewServer(&rangeServer{files: map[string][]byte{"/f": data}})
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "f")
	err := (Download{URL: ts.URL + "/f", Dest: dest, Size: 4096, SHA256: strings.Repeat("0", 64)}).Run(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("err = %v", err)
	}
	if fileExists(dest) || fileExists(dest+".part") {
		t.Fatal("bad bytes kept")
	}
	err = (Download{URL: ts.URL + "/f", Dest: dest, Size: 9999}).Run(context.Background())
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("size mismatch: %v", err)
	}
}

func TestDownloadHTTPErrors(t *testing.T) {
	ts := httptest.NewServer(&rangeServer{files: map[string][]byte{"/f": payload(10)}, auth: "Bearer tok"})
	defer ts.Close()
	dest := filepath.Join(t.TempDir(), "f")
	var he *HTTPError
	err := (Download{URL: ts.URL + "/f", Dest: dest}).Run(context.Background())
	if !errors.As(err, &he) || he.Status != 401 || !he.Permanent() {
		t.Fatalf("err = %v", err)
	}
	err = (Download{URL: ts.URL + "/f", Dest: dest, Header: http.Header{"Authorization": {"Bearer tok"}}}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = (Download{URL: ts.URL + "/missing", Dest: dest + "2", Header: http.Header{"Authorization": {"Bearer tok"}}}).Run(context.Background())
	if !errors.As(err, &he) || he.Status != 404 {
		t.Fatalf("err = %v", err)
	}
}

type tarEntry struct {
	name, body, link string
	dir              bool
	mode             int64
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.dir:
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		default:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
			if h.Mode == 0 {
				h.Mode = 0o644
			}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(b))
	}
	zw.Close()
	return buf.Bytes()
}

// withRelease swaps in a test release for a kind.
func withRelease(t *testing.T, k Kind, p Platform, assets map[Flavour][]Asset) {
	t.Helper()
	old := Releases[k]
	r := old
	r.Build = "b1"
	r.Assets = map[Platform]map[Flavour][]Asset{p: assets}
	Releases[k] = r
	t.Cleanup(func() { Releases[k] = old })
}

func TestBinariesFetch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar symlinks need privileges on windows; windows builds ship as zip")
	}
	plat := Platform{"linux", "amd64"}
	main := makeTarGz(t, []tarEntry{
		{name: "llama-b1/", dir: true},
		{name: "llama-b1/llama-server", body: "#!bin", mode: 0o755},
		{name: "llama-b1/libllama.so.0.6.0", body: "lib"},
		{name: "llama-b1/libllama.so.0", link: "libllama.so.0.6.0"},
	})
	cudart := makeTarGz(t, []tarEntry{{name: "cudart-x/libcudart.so.12", body: "rt"}})
	withRelease(t, KindLlama, plat, map[Flavour][]Asset{
		FlavourCUDA: {{"main.tar.gz", int64(len(main)), sum(main)}, {"cudart.tar.gz", int64(len(cudart)), sum(cudart)}},
	})
	srv := &rangeServer{files: map[string][]byte{"/rel/b1/main.tar.gz": main, "/rel/b1/cudart.tar.gz": cudart}}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	b := &Binaries{Dir: filepath.Join(t.TempDir(), "engines"), Platform: plat, Client: ts.Client(),
		BaseURL: func(Kind) string { return ts.URL + "/rel/" }}
	if _, ok := b.Path(KindLlama, FlavourCUDA); ok {
		t.Fatal("installed before fetch")
	}
	var lastDone, lastTotal int64
	p, err := b.Fetch(context.Background(), KindLlama, FlavourCUDA, func(d, tot int64) { lastDone, lastTotal = d, tot })
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(b.Dir, "llama-server", "b1-cuda", "llama-server")
	if p != want {
		t.Fatalf("path %s, want %s", p, want)
	}
	if lastDone != lastTotal || lastTotal != int64(len(main)+len(cudart)) {
		t.Fatalf("progress %d/%d", lastDone, lastTotal)
	}
	dir := filepath.Dir(p)
	if !fileExists(filepath.Join(dir, "libcudart.so.12")) {
		t.Fatal("runtime not beside the binary")
	}
	if l, err := os.Readlink(filepath.Join(dir, "libllama.so.0")); err != nil || l != "libllama.so.0.6.0" {
		t.Fatalf("symlink %q %v", l, err)
	}
	if st, _ := os.Stat(p); st.Mode()&0o100 == 0 {
		t.Fatal("not executable")
	}
	if got, ok := b.Path(KindLlama, FlavourCUDA); !ok || got != p {
		t.Fatal("Path after fetch")
	}
	if inst := b.Installed(KindLlama); inst[FlavourCUDA] != p {
		t.Fatalf("installed %v", inst)
	}
	if l := b.List(); len(l) != 1 || !l[0].Pinned || l[0].Flavour != FlavourCUDA {
		t.Fatalf("list %+v", l)
	}
	n := srv.requests.Load()
	if _, err := b.Fetch(context.Background(), KindLlama, FlavourCUDA, nil); err != nil || srv.requests.Load() != n {
		t.Fatal("second fetch should be a no-op")
	}
	// The override wins over the download.
	override := filepath.Join(t.TempDir(), "my-llama-server")
	os.WriteFile(override, nil, 0o755)
	b.Override = func(Kind) string { return override }
	if got, _ := b.Path(KindLlama, FlavourCUDA); got != override {
		t.Fatal("override ignored")
	}
	if err := b.Remove(KindLlama, "b1", FlavourCUDA); err != nil || len(b.Installed(KindLlama)) != 0 {
		t.Fatal("remove")
	}
}

func TestBinariesFetchWindowsZipAndErrors(t *testing.T) {
	plat := Platform{"windows", "amd64"}
	zipData := makeZip(t, map[string]string{"Release/whisper-server.exe": "exe", "Release/whisper.dll": "dll"})
	evil := makeTarGz(t, []tarEntry{{name: "../../escape", body: "x"}})
	evilLink := makeTarGz(t, []tarEntry{{name: "d/a", body: "x"}, {name: "d/l", link: "../../../etc/passwd"}})
	withRelease(t, KindWhisper, plat, map[Flavour][]Asset{
		FlavourCPU:    {{"w.zip", int64(len(zipData)), sum(zipData)}},
		FlavourVulkan: {{"evil.tar.gz", int64(len(evil)), sum(evil)}},
		FlavourROCm:   {{"evil2.tar.gz", int64(len(evilLink)), sum(evilLink)}},
		FlavourCUDA:   {{"bad.zip", 3, strings.Repeat("0", 64)}},
	})
	ts := httptest.NewServer(&rangeServer{files: map[string][]byte{
		"/engines-whisper-b1/w.zip": zipData, "/engines-whisper-b1/evil.tar.gz": evil,
		"/engines-whisper-b1/evil2.tar.gz": evilLink, "/engines-whisper-b1/bad.zip": []byte("abc")}})
	defer ts.Close()
	b := &Binaries{Dir: filepath.Join(t.TempDir(), "engines"), Platform: plat, Client: ts.Client(),
		BaseURL: func(Kind) string { return ts.URL }}
	p, err := b.Fetch(context.Background(), KindWhisper, FlavourCPU, nil)
	if err != nil || filepath.Base(p) != "whisper-server.exe" || !fileExists(filepath.Join(filepath.Dir(p), "whisper.dll")) {
		t.Fatalf("%s %v", p, err)
	}
	// "../../escape" is cleaned to a name inside the destination, never written outside it.
	if _, err := b.Fetch(context.Background(), KindWhisper, FlavourVulkan, nil); err == nil {
		t.Fatal("archive without the binary accepted")
	}
	if fileExists(filepath.Join(filepath.Dir(b.Dir), "escape")) || fileExists(filepath.Join(b.Dir, "escape")) {
		t.Fatal("traversal wrote outside the install dir")
	}
	if _, err := b.Fetch(context.Background(), KindWhisper, FlavourROCm, nil); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink escape: %v", err)
	}
	if _, err := b.Fetch(context.Background(), KindWhisper, FlavourCUDA, nil); !errors.Is(err, ErrChecksum) {
		t.Fatalf("checksum: %v", err)
	}
	if _, err := b.Fetch(context.Background(), KindWhisper, FlavourMetal, nil); err == nil {
		t.Fatal("no build")
	}
	if _, ok := b.Path(KindWhisper, FlavourVulkan); ok {
		t.Fatal("failed fetch left an install")
	}
}

func TestCommonRoot(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"llama-b1/", "llama-b1/a", "llama-b1/b"}, "llama-b1"},
		{[]string{"llama-b1/a", "llama-b1/sub/b"}, "llama-b1"},
		{[]string{"a.dll", "b.exe"}, ""},
		{[]string{"x/a", "y/b"}, ""},
		{[]string{"x/a", "README"}, ""},
		{[]string{"onlydir/"}, ""},
	} {
		if got := commonRoot(c.names); got != c.want {
			t.Errorf("%v → %q, want %q", c.names, got, c.want)
		}
	}
}

func TestExtractTarBz2(t *testing.T) {
	bz, err := exec.LookPath("bzip2")
	if err != nil {
		t.Skip("bzip2 not installed")
	}
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "m.tar")
	gz := makeTarGz(t, []tarEntry{{name: "kokoro/model.onnx", body: "onnx"}, {name: "kokoro/espeak-ng-data/x", body: "x"}})
	zr, _ := gzip.NewReader(bytes.NewReader(gz))
	raw, _ := io.ReadAll(zr)
	os.WriteFile(tarPath, raw, 0o644)
	if out, err := exec.Command(bz, tarPath).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	dest := filepath.Join(dir, "out")
	if err := Extract(tarPath+".bz2", dest); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dest, "model.onnx")) || !fileExists(filepath.Join(dest, "espeak-ng-data", "x")) {
		t.Fatal("not extracted")
	}
}
