// Package blob is jarvisd's object storage, replacing SeaweedFS/MinIO (PLAN §3.2): recipe
// images, OCR uploads and phone-call audio.
//
// Store is S3-shaped (flat slash-separated keys, prefix listing, a content type per object)
// so an S3 backend can be added later without touching callers. FS is the default backend,
// rooted at a directory such as ~/.jarvis/blobs.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrNotFound is returned (wrapped) when a key has no object.
var ErrNotFound = errors.New("blob: not found")

// ErrInvalidKey is returned (wrapped) for keys that are empty, absolute, contain "..",
// backslashes, empty segments or other characters that could escape the store.
var ErrInvalidKey = errors.New("blob: invalid key")

// Info describes a stored object.
type Info struct {
	Key         string
	Size        int64 // bytes of content
	ContentType string
	ModTime     time.Time
}

// Store is an object store keyed by slash-separated paths.
type Store interface {
	// Put stores r under key, replacing any existing object atomically: readers see the
	// old object or the new one, never a partial write. It returns the bytes written.
	Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error)
	// Get opens the object. The caller must close the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, Info, error)
	// Stat describes the object without opening its content.
	Stat(ctx context.Context, key string) (Info, error)
	// Delete removes the object. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns every object whose key starts with prefix (a plain string prefix, as
	// in S3), sorted by key.
	List(ctx context.Context, prefix string) ([]Info, error)
}

// ValidateKey reports whether key is safe to use. Keys are slash-separated; each segment
// must be non-empty, must not start with "." (which rules out "." and ".." and keeps the
// store's own temp files out of the key space), and must not contain a backslash, colon
// or NUL.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w %q: absolute", ErrInvalidKey, key)
	}
	if i := strings.IndexAny(key, "\\:\x00"); i >= 0 {
		return fmt.Errorf("%w %q: forbidden character %q", ErrInvalidKey, key, key[i])
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" {
			return fmt.Errorf("%w %q: empty segment", ErrInvalidKey, key)
		}
		if strings.HasPrefix(seg, ".") {
			return fmt.Errorf("%w %q: segment %q starts with '.'", ErrInvalidKey, key, seg)
		}
	}
	return nil
}
