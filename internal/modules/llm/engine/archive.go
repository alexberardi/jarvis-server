package engine

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Extract unpacks a .tar.gz, .tar.bz2 or .zip archive into dest (engine builds, and
// directory-shaped models such as Kokoro). See extract.
func Extract(archive, dest string) error { return extract(archive, dest) }

// extract unpacks a .tar.gz, .tar.bz2 or .zip archive into dest. A single top-level directory
// shared by every entry (llama-b11457/, Release/) is stripped, so a flavour's several assets
// (the binaries and the CUDA runtime) land side by side. Entries that would escape dest are
// rejected, and so are symlinks pointing outside it.
func extract(archive, dest string) error {
	switch {
	case strings.HasSuffix(archive, ".zip"):
		return extractZip(archive, dest)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		return extractTar(archive, dest, func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) })
	case strings.HasSuffix(archive, ".tar.bz2"), strings.HasSuffix(archive, ".tbz2"):
		return extractTar(archive, dest, func(r io.Reader) (io.Reader, error) { return bzip2.NewReader(r), nil })
	}
	return fmt.Errorf("extract %s: unknown archive type", filepath.Base(archive))
}

// commonRoot returns the single top-level directory every entry sits under, or "". Directory
// entries end in "/".
func commonRoot(names []string) string {
	root, nested := "", false
	for _, n := range names {
		n = strings.ReplaceAll(n, `\`, "/")
		isDir := strings.HasSuffix(n, "/")
		c := strings.TrimPrefix(path.Clean("/"+n), "/")
		if c == "" || c == "." {
			continue
		}
		first, _, has := strings.Cut(c, "/")
		if !has && !isDir {
			return "" // a file at the top level
		}
		nested = nested || has
		if root == "" {
			root = first
		} else if root != first {
			return ""
		}
	}
	if !nested {
		return ""
	}
	return root
}

// target maps an archive name to a path under dest, stripping root.
func target(dest, root, name string) (string, bool, error) {
	n := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, `\`, "/")), "/")
	if root != "" {
		if n == root {
			return "", false, nil
		}
		n = strings.TrimPrefix(n, root+"/")
	}
	if n == "" || n == "." {
		return "", false, nil
	}
	p := filepath.Join(dest, filepath.FromSlash(n))
	if !within(dest, p) {
		return "", false, fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return p, true, nil
}

func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func writeFile(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode|0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	names := make([]string, len(zr.File))
	for i, f := range zr.File {
		names[i] = f.Name
	}
	root := commonRoot(names)
	for _, f := range zr.File {
		p, ok, err := target(dest, root, f.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(p, rc, 0o755)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(archive, dest string, decompress func(io.Reader) (io.Reader, error)) error {
	open := func() (*tar.Reader, func(), error) {
		f, err := os.Open(archive)
		if err != nil {
			return nil, nil, err
		}
		zr, err := decompress(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return tar.NewReader(zr), func() { f.Close() }, nil
	}
	// First pass: names, to find the common root.
	tr, done, err := open()
	if err != nil {
		return err
	}
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			done()
			return err
		}
		n := h.Name
		if h.Typeflag == tar.TypeDir && !strings.HasSuffix(n, "/") {
			n += "/"
		}
		names = append(names, n)
	}
	done()
	root := commonRoot(names)

	tr, done, err = open()
	if err != nil {
		return err
	}
	defer done()
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		p, ok, err := target(dest, root, h.Name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeFile(p, tr, os.FileMode(h.Mode)&0o777); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || !within(dest, filepath.Join(filepath.Dir(p), h.Linkname)) {
				return fmt.Errorf("archive symlink %q -> %q escapes the destination", h.Name, h.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		default:
			// Hard links, devices and the like don't occur in release archives; skip them.
		}
	}
}
