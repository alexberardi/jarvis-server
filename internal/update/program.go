package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The desktop agent's self-update (01-no-cli-plan item 1.7, internal/agent/selfupdate) reuses
// this package's trust root and checks. One release carries both programs' archives in one
// SHA256SUMS under one signature, so an agent archive is verified exactly as jarvisd's is: the
// signature over SHA256SUMS with the keys compiled into the running binary, the trusted comment
// naming the release, then the archive's SHA-256 from that list. The functions here are that
// path with the program's name as a parameter; jarvisd's own Stage keeps its database steps.

// AgentProgram is the desktop agent's name in release archives.
const AgentProgram = "jarvis-agent"

// programs are the archive name prefixes a release holds.
var programs = []string{"jarvisd", AgentProgram}

// ProgramArchiveName is prog's release archive for a platform ("linux-amd64"):
// <prog>-<tag>-<platform>.tar.gz, or .zip on Windows (release.yml).
func ProgramArchiveName(prog, tag, platform string) string {
	ext := ".tar.gz"
	if strings.HasPrefix(platform, "windows-") {
		ext = ".zip"
	}
	return prog + "-" + tag + "-" + platform + ext
}

// Fetched is a release archive that passed FetchSigned: its signed SHA256SUMS, the signature
// and the archive, side by side in Dir.
type Fetched struct {
	Dir     string
	Tag     string
	Name    string // the archive's name in SHA256SUMS
	Archive string // Dir/Name
}

// FetchSigned downloads SHA256SUMS and its signature first (nothing large is fetched for a
// release that isn't signed), verifies the signature with keys and that its trusted comment
// names rel.Tag, then downloads the archive name into dir and checks its SHA-256 against the
// list. dir is created (0700) and emptied first. progress, when set, gets the bytes so far.
func FetchSigned(ctx context.Context, s Source, rel *Release, name string, keys []PublicKey, dir string, progress func(int64)) (*Fetched, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no trusted keys", ErrSignature)
	}
	archive, ok := rel.Asset(name)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", rel.Tag, name)
	}
	sumsAsset, ok := rel.Asset(SumsName)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s", rel.Tag, SumsName)
	}
	sigAsset, ok := rel.Asset(SigName)
	if !ok {
		return nil, fmt.Errorf("release %s is not signed (no %s); only signed releases are installed", rel.Tag, SigName)
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	fail := func(err error) (*Fetched, error) {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	sums, err := fetch(ctx, s, sumsAsset.URL, 1<<20)
	if err != nil {
		return fail(err)
	}
	sig, err := fetch(ctx, s, sigAsset.URL, 64<<10)
	if err != nil {
		return fail(fmt.Errorf("release %s: no %s (%w); only signed releases are installed", rel.Tag, SigName, err))
	}
	want, err := verifySums(sums, sig, keys, rel.Tag, name)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SumsName), sums, 0o600); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SigName), sig, 0o600); err != nil {
		return fail(err)
	}
	f := &Fetched{Dir: dir, Tag: rel.Tag, Name: name, Archive: filepath.Join(dir, name)}
	if err := download(ctx, s, archive, f.Archive, progress); err != nil {
		return fail(err)
	}
	if err := checkSHA256(f.Archive, want); err != nil {
		return fail(err)
	}
	return f, nil
}

// Verify checks the fetched files again from disk: the signature over SHA256SUMS with keys, the
// trusted comment naming f.Tag, and the archive's hash. What gets installed is always unpacked
// from an archive that passed this, never from a file staged earlier.
func (f *Fetched) Verify(keys []PublicKey) error {
	if f.Name != filepath.Base(f.Name) || f.Archive != filepath.Join(f.Dir, f.Name) {
		return fmt.Errorf("the staged archive %q isn't in %s", f.Archive, f.Dir)
	}
	sums, err := readSmall(filepath.Join(f.Dir, SumsName), 1<<20)
	if err != nil {
		return err
	}
	sig, err := readSmall(filepath.Join(f.Dir, SigName), 64<<10)
	if err != nil {
		return err
	}
	want, err := verifySums(sums, sig, keys, f.Tag, f.Name)
	if err != nil {
		return err
	}
	return checkSHA256(f.Archive, want)
}

func readSmall(p string, limit int64) ([]byte, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than expected", filepath.Base(p))
	}
	return b, nil
}

// ExtractProgram copies <dir>/<prog>(.exe for a .zip) out of a release archive to dst,
// executable (as ExtractBinary does for jarvisd).
func ExtractProgram(archive, dst, prog string) error {
	return extractNamed(archive, dst, programFile(archive, prog))
}

func programFile(archive, prog string) string {
	if strings.HasSuffix(archive, ".zip") {
		return prog + ".exe"
	}
	return prog
}

// maxTree bounds what ExtractTree writes in total (an app bundle with the agent is ~60 MB).
const maxTree = 1 << 30

// ExtractTree unpacks the directory <top>/<sub>/ of a .tar.gz release archive (one top-level
// directory, as release.yml packs it) into dst, which must not exist: the macOS app bundle in
// the darwin agent archive. Only directories, regular files and symbolic links are written;
// a link must be relative and stay inside the tree; set-id bits are dropped; anything else in
// the tree (devices, hard links, an entry whose path leaves it) refuses the archive.
func ExtractTree(archive, dst, sub string) error {
	if !strings.HasSuffix(archive, ".tar.gz") {
		return fmt.Errorf("%s: only a .tar.gz carries a directory tree", filepath.Base(archive))
	}
	if sub == "" || sub != path.Base(sub) {
		return fmt.Errorf("ExtractTree: %q is not one directory name", sub)
	}
	if err := os.Mkdir(dst, 0o755); err != nil {
		return err
	}
	in, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(h.Name, `\`, "/")), "/")
		parts := strings.SplitN(name, "/", 3)
		if len(parts) < 2 || parts[1] != sub {
			continue
		}
		found = true
		rel := ""
		if len(parts) == 3 {
			rel = parts[2]
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		mode := os.FileMode(h.Mode).Perm() // no set-id, sticky or type bits
		switch h.Typeflag {
		case tar.TypeDir:
			if rel == "" {
				continue
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if rel == "" {
				return fmt.Errorf("%s: %s is a file, not a directory", filepath.Base(archive), sub)
			}
			if total += h.Size; total > maxTree {
				return fmt.Errorf("%s: %s is too large", filepath.Base(archive), sub)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, h.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			l := h.Linkname
			if rel == "" || l == "" || path.IsAbs(l) || strings.Contains(l, `\`) ||
				strings.HasPrefix(path.Clean(path.Join(path.Dir(rel), l)), "../") || path.Clean(path.Join(path.Dir(rel), l)) == ".." {
				return fmt.Errorf("%s: the link %s points outside %s", filepath.Base(archive), rel, sub)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(l, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: %s has an entry of an unexpected type (%q)", filepath.Base(archive), name, h.Typeflag)
		}
	}
	if !found {
		return fmt.Errorf("%s has no %s", filepath.Base(archive), sub)
	}
	return nil
}

// ArchiveHasTree reports whether a .tar.gz release archive holds the directory <top>/<sub>/.
func ArchiveHasTree(archive, sub string) (bool, error) {
	if !strings.HasSuffix(archive, ".tar.gz") {
		return false, nil
	}
	in, err := os.Open(archive)
	if err != nil {
		return false, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return false, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		name := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(h.Name, `\`, "/")), "/")
		if parts := strings.SplitN(name, "/", 3); len(parts) >= 2 && parts[1] == sub {
			return true, nil
		}
	}
}

// CheckProgramVersion runs `<bin> version` and requires tag: the new binary runs on this OS and
// CPU and is the release it claims to be.
func CheckProgramVersion(ctx context.Context, bin, tag, prog string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return fmt.Errorf("the new %s doesn't run here: %w", prog, err)
	}
	if got := strings.TrimSpace(string(out)); got != tag {
		return fmt.Errorf("the new %s reports version %q, expected %s", prog, got, tag)
	}
	return nil
}

// RenameRetry is os.Rename, retried while Windows reports a transient lock (an antivirus scan
// of a just-written executable); see renameRetry.
func RenameRetry(oldpath, newpath string) error { return renameRetry(oldpath, newpath) }
