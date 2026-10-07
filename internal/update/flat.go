package update

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// A flat release directory (Source.Base) is what the install scripts' --base-url points at: the
// release's files side by side, no API. It may hold one release only in practice, since the
// signed comment names one tag, but nothing here assumes that.

// flatReleases reads SHA256SUMS at the base and makes one Release per tag its archive names
// carry, each with SHA256SUMS, its signature and that tag's archives as assets.
func (s Source) flatReleases(ctx context.Context) ([]Release, error) {
	sums, err := fetch(ctx, s, s.flatURL(SumsName), 1<<20)
	if err != nil {
		return nil, fmt.Errorf("release directory %s: %w", s.Base, err)
	}
	byTag := map[string]*Release{}
	var tags []string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		tag, ok := archiveTag(name)
		if !ok {
			continue
		}
		r := byTag[tag]
		if r == nil {
			r = &Release{Tag: tag, HTMLURL: s.Base, Assets: []Asset{
				{Name: SumsName, URL: s.flatURL(SumsName)},
				{Name: SigName, URL: s.flatURL(SigName)},
			}}
			byTag[tag] = r
			tags = append(tags, tag)
		}
		u := s.flatURL(name)
		r.Assets = append(r.Assets, Asset{Name: name, URL: u, Size: s.size(ctx, u)})
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("release directory %s: %s lists no jarvisd archive", s.Base, SumsName)
	}
	out := make([]Release, 0, len(tags))
	for _, t := range tags {
		r := byTag[t]
		if v, ok := ParseVersion(t); ok && v.Pre != "" {
			r.Prerelease = true
		}
		out = append(out, *r)
	}
	return out, nil
}

func (s Source) flatByTag(ctx context.Context, tag string) (*Release, error) {
	list, err := s.flatReleases(ctx)
	if err != nil {
		return nil, err
	}
	var have []string
	for i := range list {
		if list[i].Tag == tag {
			return &list[i], nil
		}
		have = append(have, list[i].Tag)
	}
	slices.Sort(have)
	return nil, fmt.Errorf("release %s: %w (the release directory %s holds %s)", tag, ErrNoRelease, s.Base, strings.Join(have, ", "))
}

// archiveTag is the tag in a release archive's name (jarvisd-<tag>-<os>-<arch>.tar.gz|.zip).
func archiveTag(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "jarvisd-")
	if !ok {
		return "", false
	}
	switch {
	case strings.HasSuffix(rest, ".tar.gz"):
		rest = strings.TrimSuffix(rest, ".tar.gz")
	case strings.HasSuffix(rest, ".zip"):
		rest = strings.TrimSuffix(rest, ".zip")
	default:
		return "", false
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return "", false
	}
	j := strings.LastIndex(rest[:i], "-")
	if j <= 0 {
		return "", false
	}
	tag, platform := rest[:j], rest[j+1:]
	if _, ok := ParseVersion(tag); !ok || ArchiveName(tag, platform) != name {
		return "", false
	}
	return tag, true
}

// flatURL is where a file of the flat release directory lives.
func (s Source) flatURL(name string) string {
	if isURL(s.Base) {
		return strings.TrimRight(s.Base, "/") + "/" + url.PathEscape(name)
	}
	return filepath.Join(s.Base, name)
}

// isURL reports whether u is an http(s) or file URL rather than a local path (a Windows path
// such as C:\rel parses with the scheme "c").
func isURL(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "file://")
}

// local is the file a download location names when this source is a local release directory
// (a path or a file URL); GitHub's assets are always fetched over HTTP.
func (s Source) local(u string) (string, bool) {
	if s.Base == "" {
		return "", false
	}
	return localPath(u)
}

// localPath is the file a download location names, when it is a local path or a file URL.
func localPath(u string) (string, bool) {
	l := strings.ToLower(u)
	if strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
		return "", false
	}
	if !strings.HasPrefix(l, "file://") {
		return u, true
	}
	pu, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	p := pu.Path
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // file:///C:/rel
	}
	return filepath.FromSlash(p), true
}

// size is a download's length in bytes, or 0 when it can't be told cheaply (a HEAD without a
// plain Content-Length): the archive is then not size-checked, only checksummed.
func (s Source) size(ctx context.Context, u string) int64 {
	if p, ok := s.local(u); ok {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return st.Size()
		}
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return 0
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != "" || resp.ContentLength <= 0 {
		return 0
	}
	return resp.ContentLength
}
