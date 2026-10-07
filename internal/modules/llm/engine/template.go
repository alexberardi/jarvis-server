package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// templateFile writes a pinned chat template where llama-server's --chat-template-file can
// read it and returns the path. Files are named by their digest, so a template is written
// once, never changes under a running engine, and a different one gets a different path
// (and so a different instance key). A file that has drifted from its content is replaced.
func (r *Resolver) templateFile(tpl string) (string, error) {
	dir := r.TemplateDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "jarvisd-chat-templates")
	}
	sum := sha256.Sum256([]byte(tpl))
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".jinja")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, []byte(tpl)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, ".tpl-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, err = f.WriteString(tpl)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}
