package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Steps reported while staging.
const (
	StepChecking    = "checking"
	StepDownloading = "downloading"
	StepVerifying   = "verifying"
	StepUnpacking   = "unpacking"
	StepTesting     = "testing"
	StepSnapshot    = "snapshot"
	StepInstalling  = "installing"
)

// Progress is reported as staging advances.
type Progress struct {
	Step  string `json:"step"`
	Done  int64  `json:"bytes_done,omitempty"`
	Total int64  `json:"bytes_total,omitempty"`
}

// StageOptions configure Stage.
type StageOptions struct {
	Paths   Paths
	Current string // the running (or installed) version
	// Target is the tag to install; "" picks the newest release this version may move to.
	Target string
	// AllowOlder installs a Target that isn't newer than Current (an explicit --version).
	AllowOlder bool
	Source     Source
	// Keys verify SHA256SUMS.minisig; nil is TrustedKeys().
	Keys []PublicKey
	// Platform picks the archive ("linux-amd64"); "" is this one.
	Platform string
	// Progress, when set, is called as staging advances.
	Progress func(Progress)
	// By names who started it ("cli" or "admin"), for the marker.
	By string
	// SkipVersionCheck skips running the new binary (tests that stage a foreign platform).
	SkipVersionCheck bool
}

// ErrUpToDate means there is nothing newer to install.
var ErrUpToDate = errors.New("already up to date")

// ErrInProgress means another upgrade hasn't finished.
var ErrInProgress = errors.New("an upgrade is already in progress")

// Plan is what an upgrade would install.
type Plan struct {
	From    string
	Release *Release
	Archive Asset
	Sums    Asset
	Sig     Asset
}

// Resolve picks the release and its downloads without downloading the archive.
func Resolve(ctx context.Context, o StageOptions) (*Plan, error) {
	var rel *Release
	if o.Target != "" {
		r, err := o.Source.ByTag(ctx, o.Target)
		if err != nil {
			return nil, err
		}
		rel = r
	} else {
		list, err := o.Source.List(ctx)
		if err != nil {
			return nil, err
		}
		if rel = Newest(list, o.Current); rel == nil {
			return nil, errors.New("no published release was found")
		}
	}
	tv, ok := ParseVersion(rel.Tag)
	if !ok {
		return nil, fmt.Errorf("release %q is not a version", rel.Tag)
	}
	if cv, ok := ParseVersion(o.Current); ok {
		c := tv.Compare(cv)
		switch {
		case o.Target == "" && c <= 0:
			return nil, fmt.Errorf("%w (running %s, newest release %s)", ErrUpToDate, o.Current, rel.Tag)
		case c == 0 && !o.AllowOlder:
			return nil, fmt.Errorf("%w (running %s)", ErrUpToDate, o.Current)
		case c < 0 && !o.AllowOlder:
			return nil, fmt.Errorf("%s is older than the running %s; installing it needs --allow-older "+
				"(and its database snapshot, since a newer jarvisd may have migrated the database)", rel.Tag, o.Current)
		}
	}
	platform := o.Platform
	if platform == "" {
		platform = Platform()
	}
	p := &Plan{From: o.Current, Release: rel}
	name := ArchiveName(rel.Tag, platform)
	if p.Archive, ok = rel.Asset(name); !ok {
		return nil, fmt.Errorf("release %s has no download for %s (%s)", rel.Tag, platform, name)
	}
	if p.Sums, ok = rel.Asset(SumsName); !ok {
		return nil, fmt.Errorf("release %s has no %s", rel.Tag, SumsName)
	}
	if p.Sig, ok = rel.Asset(SigName); !ok {
		return nil, fmt.Errorf("release %s is not signed (no %s); jarvisd only installs signed releases", rel.Tag, SigName)
	}
	return p, nil
}

// Stage downloads the release for this platform, verifies the signature and the checksum,
// unpacks the binary, checks that it runs and reports the release's version, snapshots every
// database and records the migration state, and writes the marker (StateStaged). Swap (or a
// privileged pre-start) puts it in place.
func Stage(ctx context.Context, o StageOptions) (*Marker, error) {
	report := func(p Progress) {
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	p := o.Paths
	if m, err := ReadMarker(p); err != nil {
		return nil, err
	} else if m != nil {
		return nil, fmt.Errorf("%w (%s → %s, %s); finish it or remove %s", ErrInProgress, m.From, m.To, m.State, p.MarkerPath())
	}
	report(Progress{Step: StepChecking})
	plan, err := Resolve(ctx, o)
	if err != nil {
		return nil, err
	}
	keys := o.Keys
	if keys == nil {
		if keys, err = TrustedKeys(); err != nil {
			return nil, err
		}
	}
	dir := p.StagedDir()
	_ = os.RemoveAll(dir)
	if err := mkdirOwned(p.Home, p.UpdatesDir()); err != nil {
		return nil, err
	}
	if err := mkdirOwned(p.Home, dir); err != nil {
		return nil, err
	}
	fail := func(err error) (*Marker, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	// The small files first: nothing large is downloaded for a release that isn't signed.
	report(Progress{Step: StepVerifying})
	sums, err := fetch(ctx, o.Source, plan.Sums.URL, 1<<20)
	if err != nil {
		return fail(err)
	}
	sig, err := fetch(ctx, o.Source, plan.Sig.URL, 64<<10)
	if err != nil {
		return fail(err)
	}
	want, err := verifySums(sums, sig, keys, plan.Release.Tag, plan.Archive.Name)
	if err != nil {
		return fail(err)
	}
	sumsPath, sigPath := filepath.Join(dir, SumsName), filepath.Join(dir, SigName)
	if err := os.WriteFile(sumsPath, sums, 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(sigPath, sig, 0o600); err != nil {
		return fail(err)
	}

	report(Progress{Step: StepDownloading, Total: plan.Archive.Size})
	archive := filepath.Join(dir, plan.Archive.Name)
	if err := download(ctx, o.Source, plan.Archive, archive, func(done int64) {
		report(Progress{Step: StepDownloading, Done: done, Total: plan.Archive.Size})
	}); err != nil {
		return fail(err)
	}
	report(Progress{Step: StepVerifying})
	if err := checkSHA256(archive, want); err != nil {
		return fail(err)
	}

	report(Progress{Step: StepUnpacking})
	bin := filepath.Join(dir, binaryName(plan.Archive.Name))
	if err := extractBinary(archive, bin); err != nil {
		return fail(err)
	}
	if !o.SkipVersionCheck {
		report(Progress{Step: StepTesting})
		if err := checkVersion(ctx, bin, plan.Release.Tag); err != nil {
			return fail(err)
		}
	}

	report(Progress{Step: StepSnapshot})
	files, err := DatabaseFiles(p.Home)
	if err != nil {
		return fail(err)
	}
	m := &Marker{
		State: StateStaged, From: o.Current, To: plan.Release.Tag,
		Exe: p.Exe, Prev: p.Prev(),
		Archive: archive, Asset: plan.Archive.Name, Sums: sumsPath, Sig: sigPath,
		Snapshots: map[string]string{}, StagedAt: time.Now().UTC(), By: o.By,
	}
	if m.GooseBefore, err = gooseVersions(ctx, p.Home, files); err != nil {
		return fail(err)
	}
	for _, f := range files {
		snap, err := Snapshot(ctx, p.Home, f, p.BackupsDir(), o.Current)
		if err != nil {
			return fail(err)
		}
		m.Snapshots[f] = snap
	}
	if err := WriteMarker(p, m); err != nil {
		return fail(err)
	}
	return m, nil
}

// verifySums checks the signature over SHA256SUMS with the trusted keys, that the trusted
// comment names this release (a signed list can't be replayed under another tag), and returns
// the archive's expected hash.
func verifySums(sums, sig []byte, keys []PublicKey, tag, archive string) (string, error) {
	comment, err := VerifyFile(sums, sig, keys...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", SumsName, err)
	}
	if !commentNames(comment, tag) {
		return "", fmt.Errorf("%w: the signed comment %q doesn't name release %s", ErrSignature, comment, tag)
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == archive {
			want = strings.ToLower(f[0])
		}
	}
	if len(want) != 64 {
		return "", fmt.Errorf("%s has no entry for %s", SumsName, archive)
	}
	return want, nil
}

// VerifyDir checks a release directory as the release workflow lays it out: SHA256SUMS signed
// by one of keys for tag, and every file it lists that is present matches. It returns the
// files checked.
func VerifyDir(dir, tag string, keys []PublicKey) ([]string, error) {
	sums, err := os.ReadFile(filepath.Join(dir, SumsName))
	if err != nil {
		return nil, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, SigName))
	if err != nil {
		return nil, err
	}
	comment, err := VerifyFile(sums, sig, keys...)
	if err != nil {
		return nil, err
	}
	if !commentNames(comment, tag) {
		return nil, fmt.Errorf("%w: the signed comment %q doesn't name release %s", ErrSignature, comment, tag)
	}
	var checked []string
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		name := strings.TrimPrefix(f[1], "*")
		if name != filepath.Base(name) {
			return nil, fmt.Errorf("%s lists a path, not a file name: %q", SumsName, name)
		}
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := checkSHA256(p, strings.ToLower(f[0])); err != nil {
			return nil, err
		}
		checked = append(checked, name)
	}
	if len(checked) == 0 {
		return nil, fmt.Errorf("none of the files in %s is in %s", SumsName, dir)
	}
	return checked, nil
}

// commentNames reports whether the trusted comment ("jarvisd <tag> SHA256SUMS ...", written by
// the release workflow) names the tag.
func commentNames(comment, tag string) bool {
	f := strings.Fields(comment)
	return len(f) >= 2 && f[0] == "jarvisd" && f[1] == tag
}

func fetch(ctx context.Context, s Source, url string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	resp, err := get(ctx, s, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", path.Base(url))
	}
	return b, nil
}

func get(ctx context.Context, s Source, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	client := s.Client
	if client == nil {
		// No overall timeout: the archive is large; the context bounds it.
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", path.Base(url), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: HTTP %d", path.Base(url), resp.StatusCode)
	}
	return resp, nil
}

// download streams the archive to dst, checking its size.
func download(ctx context.Context, s Source, a Asset, dst string, progress func(int64)) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	resp, err := get(ctx, s, a.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	limit := a.Size
	if limit <= 0 {
		limit = 1 << 30
	}
	n, err := io.Copy(f, &progressReader{r: io.LimitReader(resp.Body, limit+1), fn: progress})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("download %s: %w", a.Name, err)
	}
	if a.Size > 0 && n != a.Size {
		return fmt.Errorf("download %s: got %d bytes, expected %d", a.Name, n, a.Size)
	}
	return nil
}

type progressReader struct {
	r    io.Reader
	n    int64
	last time.Time
	fn   func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if p.fn != nil && (time.Since(p.last) > 250*time.Millisecond || err != nil) {
		p.last = time.Now()
		p.fn(p.n)
	}
	return n, err
}

func checkSHA256(file, want string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s doesn't match %s (got %s, expected %s)", filepath.Base(file), SumsName, got, want)
	}
	return nil
}

// binaryName is jarvisd, or jarvisd.exe for a .zip (Windows) archive.
func binaryName(archive string) string {
	if strings.HasSuffix(archive, ".zip") {
		return "jarvisd.exe"
	}
	return "jarvisd"
}

// maxBinary bounds the extracted binary (the real one is ~100 MB).
const maxBinary = 1 << 30

// extractBinary copies <dir>/jarvisd(.exe) out of the release archive (one top-level
// directory, as release.yml packs it) to dst, executable.
func extractBinary(archive, dst string) error {
	want := binaryName(archive)
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	found := false
	copyFrom := func(r io.Reader) error {
		n, err := io.Copy(out, io.LimitReader(r, maxBinary+1))
		if err == nil && n > maxBinary {
			err = errors.New("the binary in the archive is too large")
		}
		found = true
		return err
	}
	isBinary := func(name string) bool {
		name = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, `\`, "/")), "/")
		parts := strings.Split(name, "/")
		return len(parts) == 2 && parts[1] == want
	}
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.OpenReader(archive)
		if err != nil {
			out.Close()
			return err
		}
		defer zr.Close()
		for _, f := range zr.File {
			if !isBinary(f.Name) || f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				out.Close()
				return err
			}
			err = copyFrom(rc)
			rc.Close()
			if err != nil {
				out.Close()
				return err
			}
			break
		}
	} else {
		in, err := os.Open(archive)
		if err != nil {
			out.Close()
			return err
		}
		defer in.Close()
		gz, err := gzip.NewReader(in)
		if err != nil {
			out.Close()
			return err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				out.Close()
				return err
			}
			if h.Typeflag != tar.TypeReg || !isBinary(h.Name) {
				continue
			}
			if err := copyFrom(tr); err != nil {
				out.Close()
				return err
			}
			break
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s has no %s", filepath.Base(archive), want)
	}
	return os.Chmod(dst, 0o755)
}

// checkVersion runs `<bin> version` and requires the release's tag: the new binary runs on
// this OS and CPU and is the release it claims to be.
func checkVersion(ctx context.Context, bin, tag string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("the new jarvisd doesn't run here: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the new jarvisd reports version %q, expected %s", got, tag)
	}
	return nil
}
