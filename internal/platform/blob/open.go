package blob

import (
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Backends are chosen by a store URL (JARVIS_BLOB_STORE): "file:///var/lib/jarvis/blobs", or
// a bare path, is the built-in FS. Another provider (s3://bucket/prefix, …) registers an
// Opener for its scheme and implements Store; callers never change, and blobtest.Run proves
// the new backend behaves like FS.

// Opener opens a backend from its parsed store URL.
type Opener func(u *url.URL, log *slog.Logger) (Store, error)

var (
	openersMu sync.RWMutex
	openers   = map[string]Opener{}
)

// Register adds a backend for a URL scheme. It panics on a duplicate (a programming error).
func Register(scheme string, open Opener) {
	openersMu.Lock()
	defer openersMu.Unlock()
	scheme = strings.ToLower(scheme)
	if _, dup := openers[scheme]; dup {
		panic("blob: backend registered twice: " + scheme)
	}
	openers[scheme] = open
}

func init() {
	Register("file", func(u *url.URL, log *slog.Logger) (Store, error) {
		if u.Host != "" && u.Host != "localhost" {
			return nil, fmt.Errorf("blob: file URL with a remote host %q", u.Host)
		}
		p := u.Path
		if len(p) >= 3 && p[0] == '/' && p[2] == ':' { // file:///C:/jarvis/blobs on Windows
			p = p[1:]
		}
		return NewFS(filepath.FromSlash(p), log)
	})
}

// Open opens the store named by spec: a URL whose scheme has a registered backend, or a
// plain filesystem path (the default FS).
func Open(spec string, log *slog.Logger) (Store, error) {
	if spec == "" {
		return nil, fmt.Errorf("blob: no store configured")
	}
	u, err := url.Parse(spec)
	if err != nil || u.Scheme == "" || len(u.Scheme) == 1 { // no scheme, or a Windows drive letter
		return NewFS(spec, log)
	}
	openersMu.RLock()
	open, ok := openers[strings.ToLower(u.Scheme)]
	openersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("blob: no backend for %q (have: %s)", u.Scheme, strings.Join(Schemes(), ", "))
	}
	return open(u, log)
}

// Schemes lists the registered backends.
func Schemes() []string {
	openersMu.RLock()
	defer openersMu.RUnlock()
	out := make([]string, 0, len(openers))
	for s := range openers {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
