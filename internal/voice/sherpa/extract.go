package sherpa

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// extract writes the embedded libraries to baseDir/<content-hash>/ and returns that directory.
// The hash covers every embedded file, so a binary upgrade lands in a fresh directory and a
// half-written extraction (crash mid-copy) is never mistaken for a complete one.
func extract(baseDir string) (string, error) {
	if embeddedLibs == nil {
		return "", ErrUnsupported
	}
	names, err := fs.Glob(embeddedLibs, "libs/*/*")
	if err != nil || len(names) == 0 {
		return "", ErrUnsupported
	}
	sort.Strings(names)

	h := sha256.New()
	for _, n := range names {
		b, err := fs.ReadFile(embeddedLibs, n)
		if err != nil {
			return "", err
		}
		h.Write([]byte(filepath.Base(n)))
		h.Write(b)
	}
	dir := filepath.Join(baseDir, "sherpa-"+hex.EncodeToString(h.Sum(nil))[:16])
	marker := filepath.Join(dir, ".complete")
	if _, err := os.Stat(marker); err == nil {
		return dir, nil
	}

	tmp, err := os.MkdirTemp(baseDir, ".sherpa-extract-")
	if err != nil {
		if err := os.MkdirAll(baseDir, 0o755); err != nil {
			return "", fmt.Errorf("sherpa-onnx: create %s: %w", baseDir, err)
		}
		if tmp, err = os.MkdirTemp(baseDir, ".sherpa-extract-"); err != nil {
			return "", err
		}
	}
	defer os.RemoveAll(tmp)
	for _, n := range names {
		if filepath.Base(n) == ".version" {
			continue
		}
		b, _ := fs.ReadFile(embeddedLibs, n)
		if err := os.WriteFile(filepath.Join(tmp, filepath.Base(n)), b, 0o755); err != nil {
			return "", fmt.Errorf("sherpa-onnx: extract %s: %w", n, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, ".complete"), nil, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// Another process won the race; theirs is complete if the marker exists.
		if _, statErr := os.Stat(marker); statErr == nil {
			return dir, nil
		}
		return "", fmt.Errorf("sherpa-onnx: install %s: %w", dir, err)
	}
	return dir, nil
}
