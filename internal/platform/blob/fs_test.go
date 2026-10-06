package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newFS(t *testing.T) *FS {
	t.Helper()
	s, err := NewFS(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(t *testing.T, s *FS, key string) ([]byte, Info) {
	t.Helper()
	rc, info, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get %q: %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b, info
}

func TestRoundTrip(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	data := []byte("\x89PNG\nnot really\n")
	n, err := s.Put(ctx, "recipes/42/cover.png", bytes.NewReader(data), "image/png")
	if err != nil || n != int64(len(data)) {
		t.Fatalf("Put = %d, %v", n, err)
	}
	got, info := get(t, s, "recipes/42/cover.png")
	if !bytes.Equal(got, data) {
		t.Fatalf("content = %q", got)
	}
	if info.Key != "recipes/42/cover.png" || info.Size != int64(len(data)) || info.ContentType != "image/png" || info.ModTime.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	st, err := s.Stat(ctx, "recipes/42/cover.png")
	if err != nil || st.Size != info.Size || st.ContentType != "image/png" {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
}

func TestEmptyObject(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	if _, err := s.Put(ctx, "empty", strings.NewReader(""), ""); err != nil {
		t.Fatal(err)
	}
	got, info := get(t, s, "empty")
	if len(got) != 0 || info.Size != 0 || info.ContentType != "" {
		t.Fatalf("got %q %+v", got, info)
	}
}

func TestOverwrite(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	if _, err := s.Put(ctx, "k", strings.NewReader("a much longer first value"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(ctx, "k", strings.NewReader("short"), "application/json"); err != nil {
		t.Fatal(err)
	}
	got, info := get(t, s, "k")
	if string(got) != "short" || info.ContentType != "application/json" || info.Size != 5 {
		t.Fatalf("got %q %+v", got, info)
	}
}

func TestInvalidKeys(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	bad := []string{
		"", "/abs", "/", "..", "../escape", "a/../../b", "a/..", ".", "./a", "a/./b",
		"a//b", "a/", "a\\b", "..\\x", "C:\\x", "c:x", "a\x00b", ".hidden", "a/.tmp-123",
	}
	for _, k := range bad {
		t.Run(fmt.Sprintf("%q", k), func(t *testing.T) {
			if _, err := s.Put(ctx, k, strings.NewReader("x"), ""); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Put err = %v, want ErrInvalidKey", err)
			}
			if _, _, err := s.Get(ctx, k); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Get err = %v", err)
			}
			if _, err := s.Stat(ctx, k); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Stat err = %v", err)
			}
			if err := s.Delete(ctx, k); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Delete err = %v", err)
			}
		})
	}
	// Nothing escaped the root.
	parent := filepath.Dir(s.Root())
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Fatalf("files outside root: %v", entries)
	}
}

func TestNotFound(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	if _, _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get err = %v", err)
	}
	if _, err := s.Stat(ctx, "a/b/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat err = %v", err)
	}
	// A "directory" key is not an object.
	if _, err := s.Put(ctx, "dir/file", strings.NewReader("x"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, "dir"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get dir err = %v", err)
	}
}

func TestDeleteIdempotent(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	if _, err := s.Put(ctx, "a/b", strings.NewReader("x"), ""); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := s.Delete(ctx, "a/b"); err != nil {
			t.Fatalf("Delete #%d: %v", i, err)
		}
	}
	if err := s.Delete(ctx, "never/existed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "a/b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat after delete = %v", err)
	}
}

func TestList(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	keys := []string{"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png", "ocr/x.jpg", "top"}
	for _, k := range keys {
		if _, err := s.Put(ctx, k, strings.NewReader(k), "t/"+k); err != nil {
			t.Fatal(err)
		}
	}
	// A stray temp file from a crashed writer must not be listed.
	if err := os.WriteFile(filepath.Join(s.Root(), "recipes", "1", ".tmp-crash"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"":            {"ocr/x.jpg", "recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png", "top"},
		"recipes/1/":  {"recipes/1/a.png", "recipes/1/b.png"},
		"recipes/1":   {"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png"},
		"recipes/":    {"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png"},
		"ocr/x":       {"ocr/x.jpg"},
		"missing/":    nil,
		"recipes/1/a": {"recipes/1/a.png"},
	}
	for prefix, want := range cases {
		got, err := s.List(ctx, prefix)
		if err != nil {
			t.Fatalf("List %q: %v", prefix, err)
		}
		var gotKeys []string
		for _, info := range got {
			gotKeys = append(gotKeys, info.Key)
			if info.Size != int64(len(info.Key)) || info.ContentType != "t/"+info.Key {
				t.Errorf("List %q: bad info %+v", prefix, info)
			}
		}
		if fmt.Sprint(gotKeys) != fmt.Sprint(want) {
			t.Errorf("List %q = %v, want %v", prefix, gotKeys, want)
		}
	}
	if _, err := s.List(ctx, "../"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("List ../ err = %v", err)
	}
}

func TestPutCancelledLeavesOldValue(t *testing.T) {
	s := newFS(t)
	if _, err := s.Put(context.Background(), "k", strings.NewReader("old"), ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "k", strings.NewReader("new"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put err = %v", err)
	}
	if got, _ := get(t, s, "k"); string(got) != "old" {
		t.Fatalf("got %q", got)
	}
	entries, _ := os.ReadDir(s.Root())
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}

// Concurrent writers to one key, with concurrent readers: every read sees one writer's
// complete payload, and the content type always matches it.
func TestConcurrentWritersNoTornReads(t *testing.T) {
	s, ctx := newFS(t), context.Background()
	const writers, size, rounds = 8, 256 << 10, 5
	payload := func(i int) []byte { return bytes.Repeat([]byte{byte('a' + i)}, size) }

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				if _, err := s.Put(ctx, "shared/key", bytes.NewReader(payload(i)), fmt.Sprintf("w/%c", 'a'+i)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	var rwg sync.WaitGroup
	for range 4 {
		rwg.Add(1)
		go func() {
			defer rwg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				rc, info, err := s.Get(ctx, "shared/key")
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					t.Error(err)
					return
				}
				b, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					t.Error(err)
					return
				}
				if len(b) != size || info.Size != size || !bytes.Equal(b, bytes.Repeat(b[:1], size)) {
					t.Errorf("torn read: len=%d info=%+v", len(b), info)
					return
				}
				if info.ContentType != "w/"+string(b[:1]) {
					t.Errorf("content type %q for payload %q", info.ContentType, b[:1])
					return
				}
			}
		}()
	}
	wg.Wait()
	close(done)
	rwg.Wait()

	got, info := get(t, s, "shared/key")
	if len(got) != size || !bytes.Equal(got, bytes.Repeat(got[:1], size)) || info.ContentType != "w/"+string(got[:1]) {
		t.Fatalf("final object torn: len=%d %+v", len(got), info)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Root(), "shared"))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}
