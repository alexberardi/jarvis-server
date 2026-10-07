// Package update is jarvisd's signed self-update (AD5, ID10, 00-installers §4): finding a
// release, checking its minisign-signed SHA256SUMS with the key built into the running binary,
// staging the new binary, snapshotting the database, swapping the executable with a rollback
// copy, and the post-restart health gate that rolls back a release that doesn't come up.
// `jarvisd upgrade` and the admin's POST /api/update/apply share it.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// Repo is where releases are published.
	Repo = "alexberardi/jarvis-server"
	// DefaultAPIBase is the GitHub REST base.
	DefaultAPIBase = "https://api.github.com"
	// SumsName and SigName are the checksum list and its minisign signature in every release.
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.minisig"
)

// Release is one GitHub release.
type Release struct {
	Tag         string  `json:"tag_name"`
	HTMLURL     string  `json:"html_url"`
	Body        string  `json:"body"`
	Draft       bool    `json:"draft"`
	Prerelease  bool    `json:"prerelease"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Asset is a release download.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Asset finds a download by name.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Platform is GOOS-GOARCH, as release archives name it.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// ArchiveName is the release archive for a platform ("linux-amd64"):
// jarvisd-<tag>-<platform>.tar.gz, or .zip on Windows.
func ArchiveName(tag, platform string) string {
	ext := ".tar.gz"
	if strings.HasPrefix(platform, "windows-") {
		ext = ".zip"
	}
	return "jarvisd-" + tag + "-" + platform + ext
}

// Source reads releases from GitHub.
type Source struct {
	// APIBase replaces https://api.github.com (tests, the CI upgrade job).
	APIBase string
	// Client makes the requests; nil is a 15 s client.
	Client *http.Client
	// UserAgent identifies the caller ("jarvisd/<version>").
	UserAgent string
	// ReleaseBase, when set, replaces GitHub with one flat directory of release files
	// (SHA256SUMS, SHA256SUMS.minisig and the archives), as `install.sh --base-url` reads it:
	// a mirror, an offline copy, the CI and rehearsal servers. The release is the one its
	// SHA256SUMS names; the signature check is unchanged.
	ReleaseBase string
}

// archiveRE splits a release archive name into its tag: jarvisd-<tag>-<os>-<arch>.<ext>.
var archiveRE = regexp.MustCompile(`^jarvisd-(.+)-[a-z0-9]+-[a-z0-9]+\.(?:tar\.gz|zip)$`)

// flatRelease reads the one release a ReleaseBase directory holds.
func (s Source) flatRelease(ctx context.Context) (*Release, error) {
	base := strings.TrimRight(s.ReleaseBase, "/")
	sums, err := fetch(ctx, s, base+"/"+SumsName, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("Couldn't read the release files at %s: %w", base, err)
	}
	r := &Release{HTMLURL: base}
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		m := archiveRE.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		if r.Tag == "" {
			r.Tag = m[1]
		} else if r.Tag != m[1] {
			return nil, fmt.Errorf("%s/%s lists archives of more than one release (%s, %s)", base, SumsName, r.Tag, m[1])
		}
		r.Assets = append(r.Assets, Asset{Name: name, URL: base + "/" + name})
	}
	if r.Tag == "" {
		return nil, fmt.Errorf("%s/%s lists no jarvisd archive", base, SumsName)
	}
	r.Assets = append(r.Assets, Asset{Name: SumsName, URL: base + "/" + SumsName})
	// Listed only when present, so an unsigned directory gets Resolve's "not signed" refusal.
	if _, err := fetch(ctx, s, base+"/"+SigName, 64<<10); err == nil {
		r.Assets = append(r.Assets, Asset{Name: SigName, URL: base + "/" + SigName})
	}
	return r, nil
}

func (s Source) base() string {
	if b := strings.TrimRight(s.APIBase, "/"); b != "" {
		return b
	}
	return DefaultAPIBase
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// ErrNoRelease is returned by ByTag for an unknown tag.
var ErrNoRelease = errors.New("no such release")

func (s Source) get(ctx context.Context, path string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+path, nil)
	if err != nil {
		return fmt.Errorf("Couldn't build the update request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return fmt.Errorf("Couldn't reach GitHub to check for updates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNoRelease
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered HTTP %d to the update check", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(into); err != nil {
		return fmt.Errorf("GitHub's release list couldn't be read: %w", err)
	}
	return nil
}

// List returns the most recent releases.
func (s Source) List(ctx context.Context) ([]Release, error) {
	if s.ReleaseBase != "" {
		r, err := s.flatRelease(ctx)
		if err != nil {
			return nil, err
		}
		return []Release{*r}, nil
	}
	var out []Release
	err := s.get(ctx, "/repos/"+Repo+"/releases?per_page=30", &out)
	return out, err
}

// ByTag returns one release.
func (s Source) ByTag(ctx context.Context, tag string) (*Release, error) {
	if s.ReleaseBase != "" {
		r, err := s.flatRelease(ctx)
		if err != nil {
			return nil, err
		}
		if r.Tag != tag {
			return nil, fmt.Errorf("release %s: %w (%s holds %s)", tag, ErrNoRelease, strings.TrimRight(s.ReleaseBase, "/"), r.Tag)
		}
		return r, nil
	}
	var r Release
	if err := s.get(ctx, "/repos/"+Repo+"/releases/tags/"+tag, &r); err != nil {
		if errors.Is(err, ErrNoRelease) {
			return nil, fmt.Errorf("release %s: %w", tag, ErrNoRelease)
		}
		return nil, err
	}
	return &r, nil
}

// Newest picks the newest release a build at current may move to: never a draft or a tag that
// isn't a version, and prereleases only when current is itself a prerelease.
func Newest(releases []Release, current string) *Release {
	cur, ok := ParseVersion(current)
	allowPre := ok && cur.Pre != ""
	var best *Release
	var bestV Version
	for i := range releases {
		r := &releases[i]
		v, ok := ParseVersion(r.Tag)
		if r.Draft || !ok || (r.Prerelease && !allowPre) || (v.Pre != "" && !allowPre) {
			continue
		}
		if best == nil || v.Compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	return best
}

// --- semver (vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]) ---

// Version is a parsed release version.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion reads a release tag; ok is false for anything else ("dev", a commit hash).
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || (len(p) > 1 && p[0] == '0') || strings.HasPrefix(p, "+") {
			return Version{}, false
		}
		n[i] = x
	}
	return Version{n[0], n[1], n[2], pre}, true
}

// Compare orders by precedence (semver §11): a prerelease sorts before its release.
func (a Version) Compare(b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	ap, bp := strings.Split(a.Pre, "."), strings.Split(b.Pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		x, xErr := strconv.Atoi(ap[i])
		y, yErr := strconv.Atoi(bp[i])
		switch {
		case xErr == nil && yErr == nil:
			if x != y {
				return sign(x - y)
			}
		case xErr == nil:
			return -1 // numeric identifiers sort before alphanumeric
		case yErr == nil:
			return 1
		default:
			if c := strings.Compare(ap[i], bp[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(ap) - len(bp))
}

func sign(d int) int {
	switch {
	case d > 0:
		return 1
	case d < 0:
		return -1
	}
	return 0
}
