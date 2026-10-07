package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// contentTypes is set explicitly rather than taken from mime.TypeByExtension: on Windows that
// consults the registry, where .js is sometimes text/plain, and browsers refuse module scripts
// served that way (inventory §11.3).
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
}

// ContentType is the Content-Type served for a file name.
func ContentType(name string) string {
	if ct, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		return ct
	}
	return "application/octet-stream"
}

const (
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"
)

// Static serves the SPA.
type Static struct {
	ui          fs.FS // nil: serve placeholder for every path
	placeholder []byte
}

// NewStatic serves the SPA files in ui, or the placeholder page for every path when ui is nil.
func NewStatic(ui fs.FS, placeholder []byte) *Static {
	return &Static{ui: ui, placeholder: placeholder}
}

// ServeHTTP serves a file from the UI, with index.html for client-side routes. Vite's hashed
// assets/ are cached for a year; everything else revalidates (ETag), so an upgraded binary is
// picked up at once. A missing file under assets/ is a 404, never index.html: a stale page
// asking for an old chunk must not get HTML cached as immutable JavaScript.
func (s *Static) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		httpx.Error(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	if s.ui == nil {
		serveBytes(w, r, "placeholder.html", s.placeholder, cacheRevalidate)
		return
	}

	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if data, err := s.read(name); err == nil {
		cache := cacheRevalidate
		if strings.HasPrefix(name, "assets/") {
			cache = cacheImmutable
		}
		serveBytes(w, r, name, data, cache)
		return
	}
	if strings.HasPrefix(name, "assets/") {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	data, err := s.read("index.html")
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "admin UI has no index.html")
		return
	}
	serveBytes(w, r, "index.html", data, cacheRevalidate)
}

// read returns a regular file's contents. Names are /-separated io/fs paths: anything
// fs.ValidPath rejects ("..", empty elements) or that a Windows path could reinterpret
// (backslash, drive colon) is refused before it reaches the FS.
func (s *Static) read(name string) ([]byte, error) {
	if !fs.ValidPath(name) || strings.ContainsAny(name, `\:`) {
		return nil, fs.ErrNotExist
	}
	st, err := fs.Stat(s.ui, name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return fs.ReadFile(s.ui, name)
}

func serveBytes(w http.ResponseWriter, r *http.Request, name string, data []byte, cache string) {
	sum := sha256.Sum256(data)
	h := w.Header()
	h.Set("Content-Type", ContentType(name))
	h.Set("Cache-Control", cache)
	h.Set("ETag", `"`+hex.EncodeToString(sum[:12])+`"`)
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
