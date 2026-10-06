package blob

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS stores each object as one file under its root, at the key's path.
//
// File format: a one-line JSON header ({"content_type":"…"}\n) followed by the raw
// content. Keeping the metadata in the same file means a single rename publishes content
// and content type together, so concurrent writers can never pair one writer's bytes with
// another's content type. Writes go to a ".tmp-*" file in the target directory, are
// fsynced, then renamed over the key.
//
// Empty directories left behind by Delete are not pruned; that keeps Delete from racing a
// concurrent Put into the same directory.
type FS struct {
	root string
	log  *slog.Logger
}

var _ Store = (*FS)(nil)

// maxHeader bounds the header line so a corrupt file can't make reads buffer unboundedly.
const maxHeader = 4096

type header struct {
	ContentType string `json:"content_type"`
}

// NewFS returns a store rooted at dir, creating it if needed. A nil logger uses slog.Default.
func NewFS(dir string, log *slog.Logger) (*FS, error) {
	if log == nil {
		log = slog.Default()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("blob: root %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("blob: create root: %w", err)
	}
	return &FS{root: abs, log: log.With("component", "blob")}, nil
}

// Root is the directory the store writes under.
func (s *FS) Root() string { return s.root }

func (s *FS) path(key string) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	rel := filepath.FromSlash(key)
	if !filepath.IsLocal(rel) { // belt and braces, and catches Windows reserved names
		return "", fmt.Errorf("%w %q: not a local path", ErrInvalidKey, key)
	}
	return filepath.Join(s.root, rel), nil
}

// Put implements Store.
func (s *FS) Put(ctx context.Context, key string, r io.Reader, contentType string) (int64, error) {
	p, err := s.path(key)
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	hdr, _ := json.Marshal(header{ContentType: contentType})
	if _, err := tmp.Write(append(hdr, '\n')); err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	n, err := io.Copy(tmp, ctxReader{ctx, r})
	if err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	if err := tmp.Sync(); err != nil {
		return 0, fmt.Errorf("blob: put %q: sync: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return 0, fmt.Errorf("blob: put %q: %w", key, err)
	}
	ok = true
	syncDir(dir)
	s.log.Debug("blob put", "key", key, "size", n, "content_type", contentType)
	return n, nil
}

// Get implements Store.
func (s *FS) Get(ctx context.Context, key string) (io.ReadCloser, Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, Info{}, err
	}
	p, err := s.path(key)
	if err != nil {
		return nil, Info{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, Info{}, notFound(key, err)
	}
	br := bufio.NewReader(f)
	info, err := readInfo(f, br, key)
	if err != nil {
		_ = f.Close()
		return nil, Info{}, err
	}
	return readCloser{br, f}, info, nil
}

// Stat implements Store.
func (s *FS) Stat(ctx context.Context, key string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	p, err := s.path(key)
	if err != nil {
		return Info{}, err
	}
	return s.statPath(p, key)
}

func (s *FS) statPath(p, key string) (Info, error) {
	f, err := os.Open(p)
	if err != nil {
		return Info{}, notFound(key, err)
	}
	defer f.Close()
	return readInfo(f, bufio.NewReaderSize(f, 512), key)
}

// Delete implements Store.
func (s *FS) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: delete %q: %w", key, err)
	}
	return nil
}

// List implements Store. It walks only the directory the prefix pins down, so listing
// "recipes/42/" doesn't read the rest of the store.
func (s *FS) List(ctx context.Context, prefix string) ([]Info, error) {
	start := s.root
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		dirKey := prefix[:i]
		p, err := s.path(dirKey)
		if err != nil {
			return nil, fmt.Errorf("blob: list %q: %w", prefix, err)
		}
		start = p
	}
	var out []Info
	err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != start {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // temp files
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		info, err := s.statPath(p, key)
		if errors.Is(err, ErrNotFound) {
			return nil // deleted mid-walk
		}
		if err != nil {
			return err
		}
		out = append(out, info)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("blob: list %q: %w", prefix, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// readInfo consumes the header line from br and fills in Info from it and f's stat.
func readInfo(f *os.File, br *bufio.Reader, key string) (Info, error) {
	st, err := f.Stat()
	if err != nil {
		return Info{}, fmt.Errorf("blob: stat %q: %w", key, err)
	}
	if st.IsDir() {
		return Info{}, fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	line, err := readLine(br)
	if err != nil {
		return Info{}, fmt.Errorf("blob: %q: corrupt header: %w", key, err)
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return Info{}, fmt.Errorf("blob: %q: corrupt header: %w", key, err)
	}
	return Info{
		Key:         key,
		Size:        st.Size() - int64(len(line)) - 1,
		ContentType: h.ContentType,
		ModTime:     st.ModTime(),
	}, nil
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf bytes.Buffer
	for buf.Len() <= maxHeader {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == '\n' {
			return buf.Bytes(), nil
		}
		buf.WriteByte(b)
	}
	return nil, errors.New("header too long")
}

func notFound(key string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotFound, key)
	}
	return fmt.Errorf("blob: open %q: %w", key, err)
}

// syncDir makes a rename durable. Best effort: it isn't supported everywhere (Windows).
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

type readCloser struct {
	io.Reader
	io.Closer
}

// ctxReader stops a copy once ctx is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
