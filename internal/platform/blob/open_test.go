package blob_test

import (
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/blob/blobtest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// FS passes the backend conformance suite (any future backend runs the same).
func TestFSConformance(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store {
		s, err := blob.NewFS(t.TempDir(), quiet())
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	// file:///path on Unix and file:///C:/path on Windows; file://C:/path is tolerated.
	fileURL := func(p string) string {
		p = filepath.ToSlash(p)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return "file://" + p
	}
	specs := []string{filepath.Join(dir, "a"), fileURL(filepath.Join(dir, "b"))}
	if vol := filepath.VolumeName(dir); vol != "" {
		specs = append(specs, "file://"+filepath.ToSlash(filepath.Join(dir, "c")))
	}
	for _, spec := range specs {
		s, err := blob.Open(spec, quiet())
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		if _, ok := s.(*blob.FS); !ok {
			t.Fatalf("%s: %T", spec, s)
		}
	}
	if _, err := blob.Open("s3://bucket/x", quiet()); err == nil || !strings.Contains(err.Error(), "no backend for \"s3\"") {
		t.Fatalf("unknown scheme: %v", err)
	}
	if _, err := blob.Open("", quiet()); err == nil {
		t.Fatal("empty spec")
	}
	if _, err := blob.Open("file://nas/share", quiet()); err == nil {
		t.Fatal("remote file host")
	}
	// A registered backend is chosen by scheme.
	blob.Register("memtest", func(u *url.URL, _ *slog.Logger) (blob.Store, error) {
		return blob.NewFS(filepath.Join(dir, u.Host), nil)
	})
	if _, err := blob.Open("memtest://c", quiet()); err != nil {
		t.Fatal(err)
	}
}
