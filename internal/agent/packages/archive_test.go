package packages

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveURL(t *testing.T) {
	ok := map[[2]string]string{
		{"https://github.com/o/r", ""}:          "https://github.com/o/r/archive/HEAD.tar.gz",
		{"https://github.com/o/r.git", "v1.2"}:  "https://github.com/o/r/archive/v1.2.tar.gz",
		{"https://github.com/o/r/", "6e04cd0d"}: "https://github.com/o/r/archive/6e04cd0d.tar.gz",
		{" https://git.example/o/r ", "main"}:   "https://git.example/o/r/archive/main.tar.gz",
	}
	for in, want := range ok {
		if got, err := ArchiveURL(in[0], in[1]); err != nil || got != want {
			t.Errorf("%v = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range [][2]string{{"http://github.com/o/r", ""}, {"https://github.com/o", ""}, {"https://github.com/o/r/x", ""},
		{"https://u:p@github.com/o/r", ""}, {"https://github.com/o/r?x=1", ""}, {"file:///o/r", ""},
		{"https://github.com/o/r", "../x"}, {"https://github.com/o/r", "a b"}, {"https://github.com/o/r", "/abs"}} {
		if got, err := ArchiveURL(in[0], in[1]); err == nil {
			t.Errorf("%v accepted: %q", in, got)
		}
	}
}

type entry struct {
	name, body, link string
	typ              byte
}

func rawTarball(entries ...entry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Typeflag: e.typ, Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Linkname: e.link}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		_ = tw.WriteHeader(h)
		if e.typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "pkg")
	err := Extract(bytes.NewReader(rawTarball(
		entry{name: "repo-abc/", typ: tar.TypeDir},
		entry{name: "repo-abc/jarvis_package.yaml", body: "name: x", typ: tar.TypeReg},
		entry{name: "repo-abc/commands/x/command.py", body: "pass", typ: tar.TypeReg},
		entry{name: "repo-abc/escape", link: "/etc/passwd", typ: tar.TypeSymlink},
	)), dest)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "commands", "x", "command.py")); string(b) != "pass" {
		t.Fatalf("command.py = %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dest, "escape")); !os.IsNotExist(err) {
		t.Fatalf("a symlink was written: %v", err)
	}

	bad := map[string][]byte{
		"traversal":   rawTarball(entry{name: "repo/../../evil.py", body: "x", typ: tar.TypeReg}),
		"absolute":    rawTarball(entry{name: "repo//etc/x", body: "x", typ: tar.TypeReg}),
		"two tops":    rawTarball(entry{name: "a/x", body: "x", typ: tar.TypeReg}, entry{name: "b/y", body: "y", typ: tar.TypeReg}),
		"empty":       rawTarball(),
		"not gzipped": []byte("plain text"),
	}
	for name, b := range bad {
		if err := Extract(bytes.NewReader(b), filepath.Join(t.TempDir(), "pkg")); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
	// Nothing landed outside dest.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dest), "evil.py")); !os.IsNotExist(err) {
		t.Fatal("traversal wrote a file")
	}
	if err := Extract(bytes.NewReader(rawTarball(entry{name: "repo/a\\b", body: "x", typ: tar.TypeReg})), t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "not inside") {
		t.Fatalf("backslash = %v", err)
	}
}
