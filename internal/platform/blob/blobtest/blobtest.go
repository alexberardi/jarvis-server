// Package blobtest is the conformance suite every blob.Store backend must pass: FS today, and
// any provider added later (S3, …) runs the same tests against its own store.
package blobtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
)

// Run runs the suite. newStore returns an empty store for each test.
func Run(t *testing.T, newStore func(t *testing.T) blob.Store) {
	for name, test := range map[string]func(*testing.T, blob.Store){
		"RoundTrip": roundTrip, "EmptyObject": emptyObject, "Overwrite": overwrite,
		"InvalidKeys": invalidKeys, "NotFound": notFound, "DeleteIdempotent": deleteIdempotent,
		"List": list, "CancelledPutKeepsOld": cancelledPut, "ConcurrentWriters": concurrentWriters,
	} {
		t.Run(name, func(t *testing.T) { test(t, newStore(t)) })
	}
}

func get(t *testing.T, s blob.Store, key string) ([]byte, blob.Info) {
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

func put(t *testing.T, s blob.Store, key, body, ct string) {
	t.Helper()
	if _, err := s.Put(context.Background(), key, strings.NewReader(body), ct); err != nil {
		t.Fatalf("Put %q: %v", key, err)
	}
}

func roundTrip(t *testing.T, s blob.Store) {
	ctx := context.Background()
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
	if st, err := s.Stat(ctx, "recipes/42/cover.png"); err != nil || st.Size != info.Size || st.ContentType != "image/png" {
		t.Fatalf("Stat = %+v, %v", st, err)
	}
}

func emptyObject(t *testing.T, s blob.Store) {
	put(t, s, "empty", "", "")
	if got, info := get(t, s, "empty"); len(got) != 0 || info.Size != 0 || info.ContentType != "" {
		t.Fatalf("got %q %+v", got, info)
	}
}

func overwrite(t *testing.T, s blob.Store) {
	put(t, s, "k", "a much longer first value", "text/plain")
	put(t, s, "k", "short", "application/json")
	if got, info := get(t, s, "k"); string(got) != "short" || info.ContentType != "application/json" || info.Size != 5 {
		t.Fatalf("got %q %+v", got, info)
	}
}

func invalidKeys(t *testing.T, s blob.Store) {
	ctx := context.Background()
	for _, k := range []string{
		"", "/abs", "/", "..", "../escape", "a/../../b", "a/..", ".", "./a", "a/./b",
		"a//b", "a/", "a\\b", "..\\x", "C:\\x", "c:x", "a\x00b", ".hidden", "a/.tmp-123",
	} {
		if _, err := s.Put(ctx, k, strings.NewReader("x"), ""); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("Put %q err = %v, want ErrInvalidKey", k, err)
		}
		if _, _, err := s.Get(ctx, k); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("Get %q err = %v", k, err)
		}
		if _, err := s.Stat(ctx, k); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("Stat %q err = %v", k, err)
		}
		if err := s.Delete(ctx, k); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("Delete %q err = %v", k, err)
		}
	}
	if _, err := s.List(ctx, "../"); !errors.Is(err, blob.ErrInvalidKey) {
		t.Errorf("List ../ err = %v", err)
	}
}

func notFound(t *testing.T, s blob.Store) {
	ctx := context.Background()
	if _, _, err := s.Get(ctx, "nope"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Get err = %v", err)
	}
	if _, err := s.Stat(ctx, "a/b/nope"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Stat err = %v", err)
	}
	put(t, s, "dir/file", "x", "") // a "directory" key is not an object
	if _, _, err := s.Get(ctx, "dir"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Get dir err = %v", err)
	}
}

func deleteIdempotent(t *testing.T, s blob.Store) {
	ctx := context.Background()
	put(t, s, "a/b", "x", "")
	for i := range 2 {
		if err := s.Delete(ctx, "a/b"); err != nil {
			t.Fatalf("Delete #%d: %v", i, err)
		}
	}
	if err := s.Delete(ctx, "never/existed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "a/b"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Stat after delete = %v", err)
	}
}

func list(t *testing.T, s blob.Store) {
	for _, k := range []string{"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png", "ocr/x.jpg", "top"} {
		put(t, s, k, k, "t/"+k)
	}
	for prefix, want := range map[string][]string{
		"":            {"ocr/x.jpg", "recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png", "top"},
		"recipes/1/":  {"recipes/1/a.png", "recipes/1/b.png"},
		"recipes/1":   {"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png"},
		"recipes/":    {"recipes/1/a.png", "recipes/1/b.png", "recipes/10/c.png", "recipes/2/d.png"},
		"ocr/x":       {"ocr/x.jpg"},
		"missing/":    nil,
		"recipes/1/a": {"recipes/1/a.png"},
	} {
		got, err := s.List(context.Background(), prefix)
		if err != nil {
			t.Fatalf("List %q: %v", prefix, err)
		}
		var keys []string
		for _, info := range got {
			keys = append(keys, info.Key)
			if info.Size != int64(len(info.Key)) || info.ContentType != "t/"+info.Key {
				t.Errorf("List %q: bad info %+v", prefix, info)
			}
		}
		if fmt.Sprint(keys) != fmt.Sprint(want) {
			t.Errorf("List %q = %v, want %v", prefix, keys, want)
		}
	}
}

func cancelledPut(t *testing.T, s blob.Store) {
	put(t, s, "k", "old", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "k", strings.NewReader("new"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put err = %v", err)
	}
	if got, _ := get(t, s, "k"); string(got) != "old" {
		t.Fatalf("got %q", got)
	}
}

// Concurrent writers to one key with concurrent readers: every read sees one writer's whole
// payload, with that writer's content type.
func concurrentWriters(t *testing.T, s blob.Store) {
	ctx := context.Background()
	const writers, size, rounds = 6, 128 << 10, 4
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
	for range 3 {
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
				if errors.Is(err, blob.ErrNotFound) {
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
				if len(b) != size || info.Size != size || !bytes.Equal(b, bytes.Repeat(b[:1], size)) || info.ContentType != "w/"+string(b[:1]) {
					t.Errorf("torn read: len=%d info=%+v", len(b), info)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(done)
	rwg.Wait()
}
