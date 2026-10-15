package packages

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Without JARVIS_AGENT_UV, the pinned uv is downloaded once, checked against its checksum and
// kept under the packages root.
func TestPythonDownloadsThePinnedUV(t *testing.T) {
	archive := tarball(t, "uv-test", map[string]string{"uv": "#!uv", "uvx": "#!uvx"})
	sum := sha256.Sum256(archive)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/"+uvVersion+"/uv-test.tar.gz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()
	defer func(base string) { uvReleaseBase = base }(uvReleaseBase)
	uvReleaseBase = srv.URL + "/"
	uvAssets["test/ok"] = struct{ file, sha256 string }{"uv-test.tar.gz", hex.EncodeToString(sum[:])}
	uvAssets["test/bad"] = struct{ file, sha256 string }{"uv-test.tar.gz", strings.Repeat("0", 64)}
	defer delete(uvAssets, "test/ok")
	defer delete(uvAssets, "test/bad")

	root := t.TempDir()
	var ran []string
	p := &Python{Root: root, HTTP: srv.Client(), GOOS: "test", GOARCH: "bad",
		Run: func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return nil, nil
		}}
	if err := p.Install(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("bad checksum = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bin", "uv")); !os.IsNotExist(err) {
		t.Fatal("an unverified uv was kept")
	}
	p.GOARCH = "ok"
	ctx := context.Background()
	if err := p.Install(ctx, []string{"httpx"}); err != nil {
		t.Fatal(err)
	}
	uv := filepath.Join(root, "bin", "uv")
	if b, _ := os.ReadFile(uv); string(b) != "#!uv" {
		t.Fatalf("uv = %q", b)
	}
	if len(ran) != 2 || !strings.HasPrefix(ran[0], uv+" venv --python 3.12 ") || !strings.HasSuffix(ran[1], " httpx") {
		t.Fatalf("ran = %v", ran)
	}
	if err := p.Install(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if hits != 2 { // the bad and the good download; the third install reused bin/uv
		t.Fatalf("downloads = %d", hits)
	}

	p.GOOS, p.GOARCH = "plan9", "386"
	p.Root = t.TempDir()
	if err := p.Install(ctx, nil); err == nil || !strings.Contains(err.Error(), "aren't supported on plan9/386") {
		t.Fatalf("unsupported = %v", err)
	}
}

func TestFindInZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("uv-x86_64-pc-windows-msvc/uv.exe")
	_, _ = w.Write([]byte("MZ"))
	_ = zw.Close()
	if b, err := findInArchive(buf.Bytes(), "x.zip", "uv.exe"); err != nil || string(b) != "MZ" {
		t.Fatalf("zip = %q %v", b, err)
	}
	if _, err := findInArchive(buf.Bytes(), "x.zip", "uvx.exe"); err == nil {
		t.Fatal("found a missing file")
	}
}

// Every platform the agent ships on has a pinned uv.
func TestUVAssetsCoverTheAgentsPlatforms(t *testing.T) {
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64", "windows/amd64"} {
		a, ok := uvAssets[p]
		if !ok || len(a.sha256) != 64 {
			t.Errorf("%s: %+v", p, a)
		}
	}
	if w := (&Python{Root: "r", GOOS: "windows"}).Interpreter(); w != filepath.Join("r", "venv", "Scripts", "python.exe") {
		t.Fatalf("windows interpreter = %s", w)
	}
}
