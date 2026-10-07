package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// flatDir writes a flat release directory the way the install scripts' --base-url serves it:
// the archive for this platform, another platform's, SHA256SUMS and (unless noSig) its
// signature.
func flatDir(t *testing.T, tag string, s testSigner, noSig bool) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	for _, platform := range []string{Platform(), "plan9-mips"} {
		name := ArchiveName(tag, platform)
		b := makeArchive(t, name, fakeBinary(t))
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	fmt.Fprintf(&sums, "%s  install-notes.txt\n", strings.Repeat("1", 64))
	if err := os.WriteFile(filepath.Join(dir, SumsName), []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if !noSig {
		sig := s.sign([]byte(sums.String()), "jarvisd "+tag+" SHA256SUMS", true)
		if err := os.WriteFile(filepath.Join(dir, SigName), sig, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFlatBaseStage(t *testing.T) {
	t.Setenv("JARVISD_FAKE_VERSION", "v1.1.0")
	s := newTestSigner(t)
	dir := flatDir(t, "v1.1.0", s, false)
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	fileURL := "file://" + filepath.ToSlash(dir)
	if !strings.HasPrefix(fileURL, "file:///") {
		fileURL = "file:///" + filepath.ToSlash(dir) // Windows: file:///C:/...
	}
	for name, base := range map[string]string{"http": srv.URL + "/", "path": dir, "file url": fileURL} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			in := newInstall(t)
			src := Source{Base: base, UserAgent: "test"}
			// The newest from SHA256SUMS, and by tag.
			for _, target := range []string{"", "v1.1.0"} {
				p, err := Resolve(ctx, StageOptions{Current: "v1.0.0", Target: target, Source: src})
				if err != nil || p.Release.Tag != "v1.1.0" || p.Archive.Size == 0 {
					t.Fatalf("resolve %q: %+v %v", target, p, err)
				}
			}
			if _, err := Resolve(ctx, StageOptions{Current: "v1.0.0", Target: "v1.2.0", Source: src}); !errors.Is(err, ErrNoRelease) ||
				!strings.Contains(err.Error(), "holds v1.1.0") {
				t.Fatalf("unknown tag: %v", err)
			}
			o := StageOptions{Paths: in.paths, Current: "v1.0.0", Target: "v1.1.0", Source: src, Keys: []PublicKey{s.pub}}
			if _, err := Stage(ctx, o); err != nil {
				t.Fatal(err)
			}
			if _, err := Swap(ctx, in.paths, SwapOptions{Keys: o.Keys}); err != nil {
				t.Fatal(err)
			}
			if readString(t, in.paths.Prev()) != "old binary" {
				t.Fatal("not swapped")
			}
		})
	}
}

func TestFlatBaseRefusals(t *testing.T) {
	t.Setenv("JARVISD_FAKE_VERSION", "v1.1.0")
	ctx := context.Background()
	s, other := newTestSigner(t), newTestSigner(t)
	for name, c := range map[string]struct {
		dir  string
		keys []PublicKey
		want string
	}{
		"unsigned":  {flatDir(t, "v1.1.0", s, true), []PublicKey{s.pub}, "only installs signed releases"},
		"wrong key": {flatDir(t, "v1.1.0", s, false), []PublicKey{other.pub}, "SHA256SUMS"},
		"empty":     {t.TempDir(), []PublicKey{s.pub}, "release directory"},
	} {
		t.Run(name, func(t *testing.T) {
			in := newInstall(t)
			_, err := Stage(ctx, StageOptions{Paths: in.paths, Current: "v1.0.0", Source: Source{Base: c.dir}, Keys: c.keys})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if readString(t, in.paths.Exe) != "old binary" {
				t.Fatal("executable touched")
			}
		})
	}
}

// A GitHub source never reads local files, whatever a release's asset URLs say.
func TestGitHubSourceNoLocalFiles(t *testing.T) {
	if _, ok := (Source{}).local("/etc/passwd"); ok {
		t.Fatal("a GitHub source reads local paths")
	}
}

func TestArchiveTag(t *testing.T) {
	for name, want := range map[string]string{
		"jarvisd-v1.2.3-linux-amd64.tar.gz":       "v1.2.3",
		"jarvisd-v1.2.3-rc.1-darwin-arm64.tar.gz": "v1.2.3-rc.1",
		"jarvisd-v0.0.0-ci-windows-amd64.zip":     "v0.0.0-ci",
		"jarvisd-v1.2.3-windows-amd64.tar.gz":     "", // Windows ships .zip
		"jarvisd-v1.2.3-linux-amd64.zip":          "",
		"jarvisd-linux-amd64.tar.gz":              "",
		"jarvis-node-v1.2.3-linux-amd64.tar.gz":   "",
		"install.sh":                              "",
	} {
		got, ok := archiveTag(name)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v, want %q", name, got, ok, want)
		}
	}
}

func TestCheckDisk(t *testing.T) {
	in := newInstall(t)
	if err := os.WriteFile(in.paths.Exe, make([]byte, 10<<20), 0o755); err != nil {
		t.Fatal(err)
	}
	old := diskFree
	t.Cleanup(func() { diskFree = old })
	free := map[string]uint64{}
	diskFree = func(p string) uint64 { return free[p] }

	// Unknown free space: not checked.
	if err := CheckDisk(in.paths, 50<<20); err != nil {
		t.Fatal(err)
	}
	free[in.paths.Home], free[filepath.Dir(in.paths.Exe)] = 1<<40, 1<<40
	if err := CheckDisk(in.paths, 50<<20); err != nil {
		t.Fatal(err)
	}
	// The data directory needs the archive ×3 plus the databases.
	free[in.paths.Home] = 150 << 20
	err := CheckDisk(in.paths, 50<<20)
	if !errors.Is(err, ErrDiskFull) || !strings.Contains(err.Error(), in.paths.Home) || !strings.Contains(err.Error(), "151 MB") {
		t.Fatalf("home: %v", err)
	}
	// The binary's directory needs two binaries.
	free[in.paths.Home] = 1 << 40
	free[filepath.Dir(in.paths.Exe)] = 15 << 20
	if err := CheckDisk(in.paths, 0); !errors.Is(err, ErrDiskFull) || !strings.Contains(err.Error(), "20 MB") {
		t.Fatalf("bin dir: %v", err)
	}
}

// The install scripts verify a fresh install against the same key the binary trusts.
func TestScriptsTrustTheProjectKey(t *testing.T) {
	re := regexp.MustCompile(`RW[A-Za-z0-9+/]{54}`)
	for _, f := range []string{"install.sh", "install.ps1"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "scripts", f))
		if err != nil {
			t.Fatal(err)
		}
		keys := re.FindAllString(string(b), -1)
		if len(keys) != 1 || keys[0] != ProjectPublicKey {
			t.Errorf("%s: minisign keys %q, want only %s", f, keys, ProjectPublicKey)
		}
		if strings.Contains(string(b), "JARVISD_MINISIGN_PUBKEY") {
			t.Errorf("%s: the trusted key must not be overridable", f)
		}
	}
}
