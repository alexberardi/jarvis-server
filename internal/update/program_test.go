package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentRelease adds a release holding one agent archive (content as given) to f.
func (f *fakeGitHub) agentRelease(tag, platform string, archive []byte, signer testSigner, comment string, noSig bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := ProgramArchiveName(AgentProgram, tag, platform)
	sum := sha256.Sum256(archive)
	sums := []byte(fmt.Sprintf("%s  %s\n%s  %s\n", hex.EncodeToString(sum[:]), name,
		strings.Repeat("1", 64), ArchiveName(tag, platform)))
	rel := Release{Tag: tag}
	put := func(n string, b []byte) {
		p := "/download/" + tag + "/" + n
		f.files[p] = b
		rel.Assets = append(rel.Assets, Asset{Name: n, URL: f.srv.URL + p, Size: int64(len(b))})
	}
	put(name, archive)
	put(SumsName, sums)
	if !noSig {
		if comment == "" {
			comment = "jarvisd " + tag + " SHA256SUMS"
		}
		put(SigName, signer.sign(sums, comment, true))
	}
	f.releases = append(f.releases, rel)
}

type tarEntry struct {
	name, link string
	body       []byte
	mode       int64
	typ        byte
}

func tarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Linkname: e.link, Mode: e.mode, Size: int64(len(e.body)), Typeflag: e.typ}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			_, _ = tw.Write(e.body)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchSignedAgentArchive(t *testing.T) {
	gh := newFakeGitHub(t)
	signer := newTestSigner(t)
	other := newTestSigner(t)
	other.pub.ID = [8]byte{9, 9, 9, 9, 9, 9, 9, 9}
	const platform = "linux-amd64"
	dir := "jarvis-agent-v1.1.0-" + platform
	arch := tarGz(t, tarEntry{name: dir + "/jarvis-agent", body: []byte("agent v1.1.0"), mode: 0o755, typ: tar.TypeReg})
	gh.agentRelease("v1.1.0", platform, arch, signer, "", false)
	gh.agentRelease("v1.2.0", platform, arch, signer, "", true)                           // unsigned
	gh.agentRelease("v1.3.0", platform, arch, other, "", false)                           // another key
	gh.agentRelease("v1.4.0", platform, arch, signer, "jarvisd v1.1.0 SHA256SUMS", false) // replayed comment
	gh.agentRelease("v1.5.0", platform, append(bytes.Clone(arch), 0), signer, "", false)  // fine: its own hash
	ctx := context.Background()
	keys := []PublicKey{signer.pub}
	fetchTag := func(tag string) (*Fetched, error) {
		rel, err := gh.source().ByTag(ctx, tag)
		if err != nil {
			t.Fatal(err)
		}
		return FetchSigned(ctx, gh.source(), rel, ProgramArchiveName(AgentProgram, tag, platform), keys, filepath.Join(t.TempDir(), "staged"), nil)
	}

	f, err := fetchTag("v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(keys); err != nil {
		t.Fatalf("re-verify: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "jarvis-agent")
	if err := ExtractProgram(f.Archive, bin, AgentProgram); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "agent v1.1.0" {
		t.Fatalf("extracted %q", b)
	}
	// A staged archive changed after the check is caught by Verify.
	if err := os.WriteFile(f.Archive, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Verify(keys); err == nil {
		t.Fatal("Verify accepted a tampered archive")
	}
	if err := f.Verify([]PublicKey{other.pub}); !errors.Is(err, ErrSignature) {
		t.Fatalf("Verify with another key: %v", err)
	}

	if _, err := fetchTag("v1.2.0"); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("unsigned: %v", err)
	}
	if _, err := fetchTag("v1.3.0"); !errors.Is(err, ErrSignature) {
		t.Fatalf("foreign key: %v", err)
	}
	if _, err := fetchTag("v1.4.0"); !errors.Is(err, ErrSignature) {
		t.Fatalf("comment naming another release: %v", err)
	}
	if _, err := fetchTag("v1.5.0"); err != nil {
		t.Fatalf("v1.5.0: %v", err)
	}
	// No keys at all is a refusal, never "nothing to check".
	rel, _ := gh.source().ByTag(ctx, "v1.1.0")
	if _, err := FetchSigned(ctx, gh.source(), rel, ProgramArchiveName(AgentProgram, "v1.1.0", platform), nil, t.TempDir()+"/s", nil); !errors.Is(err, ErrSignature) {
		t.Fatalf("no keys: %v", err)
	}
}

func TestFetchSignedBadChecksum(t *testing.T) {
	gh := newFakeGitHub(t)
	signer := newTestSigner(t)
	arch := tarGz(t, tarEntry{name: "d/jarvis-agent", body: []byte("x"), mode: 0o755, typ: tar.TypeReg})
	gh.agentRelease("v2.0.0", "linux-amd64", arch, signer, "", false)
	// Serve other bytes than the signed list names.
	gh.mu.Lock()
	gh.files["/download/v2.0.0/"+ProgramArchiveName(AgentProgram, "v2.0.0", "linux-amd64")] = append(bytes.Clone(arch), 1)
	gh.releases[0].Assets[0].Size++
	gh.mu.Unlock()
	ctx := context.Background()
	rel, _ := gh.source().ByTag(ctx, "v2.0.0")
	dir := filepath.Join(t.TempDir(), "s")
	_, err := FetchSigned(ctx, gh.source(), rel, ProgramArchiveName(AgentProgram, "v2.0.0", "linux-amd64"), []PublicKey{signer.pub}, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("bad checksum: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("a refused download left its directory")
	}
}

func TestFlatSourceListsAgentArchives(t *testing.T) {
	dir := t.TempDir()
	name := ProgramArchiveName(AgentProgram, "v3.0.0", "linux-amd64")
	if err := os.WriteFile(filepath.Join(dir, SumsName), []byte(strings.Repeat("a", 64)+"  "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := (Source{Base: dir}).ByTag(context.Background(), "v3.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rel.Asset(name); !ok {
		t.Fatalf("no agent asset: %+v", rel.Assets)
	}
}

func TestExtractTree(t *testing.T) {
	const app = "Jarvis Automation Agent.app"
	top := "jarvis-agent-v1.0.0-darwin-arm64/"
	entries := []tarEntry{
		{name: top + "jarvis-agent", body: []byte("bare"), mode: 0o755, typ: tar.TypeReg},
		{name: top + app + "/", typ: tar.TypeDir, mode: 0o755},
		{name: top + app + "/Contents/Info.plist", body: []byte("plist"), typ: tar.TypeReg},
		{name: top + app + "/Contents/MacOS/jarvis-agent", body: []byte("bin"), mode: 0o4755, typ: tar.TypeReg},
	}
	if runtime.GOOS != "windows" { // the tree is a macOS app; Windows needs privileges for links
		entries = append(entries, tarEntry{name: top + app + "/Contents/Current", link: "MacOS", typ: tar.TypeSymlink})
	}
	good := tarGz(t, entries...)
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ArchiveHasTree(p, app); err != nil || !ok {
		t.Fatalf("ArchiveHasTree: %v %v", ok, err)
	}
	if ok, _ := ArchiveHasTree(p, "Other.app"); ok {
		t.Fatal("ArchiveHasTree found a missing tree")
	}
	dst := filepath.Join(t.TempDir(), "new.app")
	if err := ExtractTree(p, dst, app); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "Contents", "MacOS", "jarvis-agent")); string(b) != "bin" {
		t.Fatalf("binary %q", b)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dst, "Contents", "MacOS", "jarvis-agent"))
		if st.Mode()&os.ModeSetuid != 0 || st.Mode().Perm() != 0o755 {
			t.Fatalf("mode %v", st.Mode())
		}
		if l, err := os.Readlink(filepath.Join(dst, "Contents", "Current")); err != nil || l != "MacOS" {
			t.Fatalf("link %q %v", l, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "jarvis-agent")); !os.IsNotExist(err) {
		t.Fatal("extracted a file outside the tree")
	}
	if err := ExtractTree(p, dst, app); err == nil {
		t.Fatal("extracted into an existing directory")
	}

	for name, bad := range map[string][]byte{
		"absolute link": tarGz(t, tarEntry{name: top + app + "/Contents/x", link: "/etc", typ: tar.TypeSymlink}),
		"escaping link": tarGz(t, tarEntry{name: top + app + "/Contents/x", link: "../../../etc", typ: tar.TypeSymlink}),
		"hard link":     tarGz(t, tarEntry{name: top + app + "/Contents/x", link: top + "jarvis-agent", typ: tar.TypeLink}),
		"no tree":       tarGz(t, tarEntry{name: top + "jarvis-agent", body: []byte("x"), typ: tar.TypeReg}),
	} {
		p := filepath.Join(t.TempDir(), "bad.tar.gz")
		if err := os.WriteFile(p, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ExtractTree(p, filepath.Join(t.TempDir(), "o.app"), app); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A path that climbs out of the archive's top directory is cleaned to inside it, never outside.
	climb := tarGz(t, tarEntry{name: top + app + "/../../../escape", body: []byte("x"), typ: tar.TypeReg},
		tarEntry{name: top + app + "/Contents/ok", body: []byte("x"), typ: tar.TypeReg})
	p = filepath.Join(t.TempDir(), "climb.tar.gz")
	_ = os.WriteFile(p, climb, 0o600)
	root := t.TempDir()
	if err := ExtractTree(p, filepath.Join(root, "o.app"), app); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatal("an entry escaped the destination")
	}
}
