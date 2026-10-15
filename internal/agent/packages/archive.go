package packages

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Limits on a package download: a Pantry package is source code, so anything near these is
// not one.
const (
	maxArchiveBytes = 64 << 20  // compressed
	maxPackageBytes = 256 << 20 // extracted
	maxPackageFiles = 20000
)

// ArchiveURL is the tarball of repo at ref, as the Pi node fetches it (no git needed): GitHub
// serves /archive/{ref}.tar.gz for a tag, a branch or a full commit SHA. No ref is the
// default branch (HEAD).
func ArchiveURL(repo, ref string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repo))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("package repository %q is not an https URL", repo)
	}
	p := strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	if strings.Count(strings.Trim(p, "/"), "/") != 1 {
		return "", fmt.Errorf("package repository %q is not an owner/repo URL", repo)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "HEAD"
	}
	if strings.Contains(ref, "..") || strings.ContainsAny(ref, "?#\\ ") || strings.HasPrefix(ref, "/") {
		return "", fmt.Errorf("invalid version %q", ref)
	}
	u.Path = p + "/archive/" + ref + ".tar.gz"
	return u.String(), nil
}

// Download fetches rawURL and unpacks the tarball's single top directory into dest (created).
func Download(ctx context.Context, hc *http.Client, rawURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", rawURL, resp.StatusCode)
	}
	return Extract(io.LimitReader(resp.Body, maxArchiveBytes+1), dest)
}

// Extract unpacks a GitHub-style .tar.gz (one top directory) into dest, dropping that
// directory. Only regular files and directories are written; links, devices and any path that
// would land outside dest are refused, as are archives over the size limits.
func Extract(r io.Reader, dest string) error {
	counted := &countingReader{r: r}
	gz, err := gzip.NewReader(counted)
	if err != nil {
		return fmt.Errorf("package archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	top := ""
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("package archive: %w", err)
		}
		if counted.n > maxArchiveBytes {
			return errors.New("package archive is too large")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeXGlobalHeader || name == "" {
			continue // GitHub's pax header carries the commit id
		}
		first, rest, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		}
		if first != top {
			return errors.New("package archive has more than one top directory")
		}
		rest = strings.TrimSuffix(rest, "/")
		if rest == "" {
			continue
		}
		if err := checkRelPath(rest); err != nil {
			return fmt.Errorf("package archive: %w", err)
		}
		target := filepath.Join(dest, filepath.FromSlash(path.Clean(rest)))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += h.Size
			if files > maxPackageFiles || total > maxPackageBytes {
				return errors.New("package is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, h.Size); err != nil {
				return err
			}
		default:
			// Symlinks and the rest: a package is plain files.
		}
	}
	if top == "" {
		return errors.New("package archive is empty")
	}
	return nil
}

func writeFile(target string, r io.Reader, size int64) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
