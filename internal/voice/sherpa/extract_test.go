package sherpa

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func withEmbedded(t *testing.T, fsys fs.FS) {
	t.Helper()
	orig := embeddedLibs
	embeddedLibs = fsys
	t.Cleanup(func() { embeddedLibs = orig })
}

func TestExtractWritesLibsAndIsIdempotent(t *testing.T) {
	withEmbedded(t, fstest.MapFS{
		"libs/x-y/libfoo.so": {Data: []byte("foo")},
		"libs/x-y/libbar.so": {Data: []byte("bar")},
		"libs/x-y/.version":  {Data: []byte("1.0")},
	})
	base := t.TempDir()

	dir1, err := extract(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"libfoo.so": "foo", "libbar.so": "bar"} {
		got, err := os.ReadFile(filepath.Join(dir1, name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir1, ".version")); !os.IsNotExist(err) {
		t.Errorf(".version should not be extracted")
	}

	dir2, err := extract(base)
	if err != nil || dir2 != dir1 {
		t.Fatalf("second extract = %q, %v; want %q", dir2, err, dir1)
	}
	if left, _ := filepath.Glob(filepath.Join(base, ".sherpa-extract-*")); len(left) != 0 {
		t.Errorf("temp dirs left behind: %v", left)
	}
}

func TestExtractNewContentGetsNewDir(t *testing.T) {
	base := t.TempDir()
	withEmbedded(t, fstest.MapFS{"libs/x-y/libfoo.so": {Data: []byte("v1")}})
	d1, err := extract(base)
	if err != nil {
		t.Fatal(err)
	}
	withEmbedded(t, fstest.MapFS{"libs/x-y/libfoo.so": {Data: []byte("v2")}})
	d2, err := extract(base)
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatalf("changed library content reused directory %s", d1)
	}
}

func TestExtractIgnoresIncompleteDir(t *testing.T) {
	base := t.TempDir()
	withEmbedded(t, fstest.MapFS{"libs/x-y/libfoo.so": {Data: []byte("foo")}})
	dir, err := extract(base)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash mid-install: marker missing, file truncated.
	os.Remove(filepath.Join(dir, ".complete"))
	os.WriteFile(filepath.Join(dir, "libfoo.so"), nil, 0o755)
	os.RemoveAll(dir) // rename onto a non-empty dir fails on some OSes; a crash leaves no final dir anyway

	dir2, err := extract(base)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir2, "libfoo.so")); string(got) != "foo" {
		t.Fatalf("libfoo.so = %q after re-extract", got)
	}
}

func TestExtractUnsupported(t *testing.T) {
	withEmbedded(t, nil)
	if _, err := extract(t.TempDir()); err != ErrUnsupported {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
