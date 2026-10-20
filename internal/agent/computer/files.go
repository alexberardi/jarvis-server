package computer

import (
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// computer_files limits.
const (
	maxRead     = 1 << 20 // text read back
	maxWrite    = 5 << 20
	maxEntries  = 500
	jarvisDir   = "Documents/Jarvis"
	downloadDir = "Downloads"
)

// textExts are the files computer_files reads back as text (statements, exports).
var textExts = []string{".csv", ".txt", ".ofx", ".json"}

// errPath: a path outside the folders computer_files may use.
var errPath = errors.New("computer_files only uses Downloads and ~/Documents/Jarvis (and writes only in ~/Documents/Jarvis)")

func (t *Tools) home() (string, error) {
	if t.Home != "" {
		return t.Home, nil
	}
	return os.UserHomeDir()
}

// roots are the folders computer_files reads (Downloads, the Jarvis folder) and the one it
// writes (the Jarvis folder, created 0700), with their symlinks resolved.
func (t *Tools) roots() (read []string, write string, err error) {
	home, err := t.home()
	if err != nil {
		return nil, "", err
	}
	jd := filepath.Join(home, filepath.FromSlash(jarvisDir))
	if err := os.MkdirAll(jd, 0o700); err != nil {
		return nil, "", err
	}
	if write, err = filepath.EvalSymlinks(jd); err != nil {
		return nil, "", err
	}
	read = []string{write}
	dl := os.Getenv("XDG_DOWNLOAD_DIR")
	if dl == "" || t.Home != "" { // a given Home (tests) has its own Downloads
		dl = filepath.Join(home, downloadDir)
	}
	if r, err := filepath.EvalSymlinks(dl); err == nil {
		read = append(read, r)
	}
	return read, write, nil
}

// clean is path made absolute ("~/" is the home folder; a relative path is in the Jarvis
// folder), refusing "..".
func (t *Tools) clean(path string) (string, error) {
	home, err := t.home()
	if err != nil {
		return "", err
	}
	p := filepath.FromSlash(strings.TrimSpace(path))
	if p == "" {
		return "", errors.New("path is required")
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		if part == ".." {
			return "", errPath
		}
	}
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~"+string(filepath.Separator)):
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p) && (filepath.VolumeName(p) != "" || strings.HasPrefix(p, string(filepath.Separator))):
		return "", errPath // Windows: "\etc\x" or "C:x", on no drive or the drive's current folder
	case !filepath.IsAbs(p):
		p = filepath.Join(home, filepath.FromSlash(jarvisDir), p)
	}
	return filepath.Clean(p), nil
}

// within is whether p is root or inside it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (t *Tools) files(args map[string]any) map[string]any {
	op, _ := args["op"].(string)
	path, _ := args["path"].(string)
	p, err := t.clean(path)
	if errors.Is(err, errPath) {
		return failCode("path_refused", "%v", err)
	} else if err != nil {
		return failCode("invalid_argument", "%v", err)
	}
	read, write, err := t.roots()
	if err != nil {
		return failCode("failed", "%v", err)
	}
	switch op {
	case "list", "read":
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return failCode("not_found", "%v", err)
		}
		if !slices.ContainsFunc(read, func(r string) bool { return within(real, r) }) {
			return failCode("path_refused", "%v", errPath)
		}
		if op == "list" {
			return list(real)
		}
		return readFile(real)
	case "write":
		data, err := base64.StdEncoding.DecodeString(stringArg(args, "content_b64"))
		if err != nil {
			return failCode("invalid_argument", "content_b64: %v", err)
		}
		if len(data) > maxWrite {
			return failCode("too_large", "%d bytes, at most %d", len(data), maxWrite)
		}
		// The folder it goes in, resolved, must be the Jarvis folder or inside it; the file
		// itself must not be a symlink (it could point anywhere).
		dir, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil || !within(dir, write) {
			return failCode("path_refused", "%v", errPath)
		}
		dst := filepath.Join(dir, filepath.Base(p))
		if fi, err := os.Lstat(dst); err == nil && !fi.Mode().IsRegular() {
			return failCode("path_refused", "%s isn't a plain file", path)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return failCode("failed", "%v", err)
		}
		if err := os.Chmod(dst, 0o600); err != nil {
			return failCode("failed", "%v", err)
		}
		return map[string]any{"success": true, "path": dst, "bytes": int64(len(data))}
	}
	return failCode("invalid_argument", "op must be list, read or write")
}

func stringArg(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

func list(dir string) map[string]any {
	es, err := os.ReadDir(dir)
	if err != nil {
		return failCode("failed", "%v", err)
	}
	entries := []map[string]any{}
	for _, e := range es {
		if len(entries) == maxEntries {
			break
		}
		m := map[string]any{"name": e.Name(), "dir": e.IsDir()}
		if fi, err := e.Info(); err == nil {
			if !e.IsDir() {
				m["bytes"] = fi.Size()
			}
			m["modified"] = fi.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		}
		entries = append(entries, m)
	}
	return map[string]any{"success": true, "path": dir, "entries": entries, "truncated": len(es) > len(entries)}
}

func readFile(p string) map[string]any {
	fi, err := os.Stat(p)
	if err != nil {
		return failCode("not_found", "%v", err)
	}
	if fi.IsDir() {
		return failCode("invalid_argument", "%s is a folder: list it", p)
	}
	ext := strings.ToLower(filepath.Ext(p))
	out := map[string]any{"success": true, "path": p, "bytes": fi.Size(), "mime": mimeOf(p, ext)}
	if slices.Contains(textExts, ext) && fi.Size() <= maxRead {
		b, err := os.ReadFile(p)
		if err != nil {
			return failCode("failed", "%v", err)
		}
		out["text"] = string(b)
	}
	return out
}

func mimeOf(p, ext string) string {
	if m := mime.TypeByExtension(ext); m != "" {
		m, _, _ = strings.Cut(m, ";")
		return m
	}
	f, err := os.Open(p)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	m, _, _ := strings.Cut(http.DetectContentType(buf[:n]), ";")
	return m
}
