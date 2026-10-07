package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves a release list, per-tag lookups and the assets, like GitHub's API and
// download host.
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	releases []Release
	files    map[string][]byte // download path -> content
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) source() Source { return Source{APIBase: f.srv.URL, UserAgent: "test"} }

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/repos/"+Repo+"/releases":
		_ = json.NewEncoder(w).Encode(f.releases)
	case strings.HasPrefix(r.URL.Path, "/repos/"+Repo+"/releases/tags/"):
		tag := strings.TrimPrefix(r.URL.Path, "/repos/"+Repo+"/releases/tags/")
		for _, rel := range f.releases {
			if rel.Tag == tag {
				_ = json.NewEncoder(w).Encode(rel)
				return
			}
		}
		http.NotFound(w, r)
	default:
		b, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}
}

// releaseSpec describes one fake release.
type releaseSpec struct {
	tag      string
	platform string
	binary   []byte // the jarvisd inside the archive
	signer   *testSigner
	comment  string // trusted comment; "" is "jarvisd <tag> SHA256SUMS"
	noSig    bool
	badSum   bool // SHA256SUMS lists a wrong hash for the archive
	pre      bool
}

func (f *fakeGitHub) add(s releaseSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := ArchiveName(s.tag, s.platform)
	archive := makeArchive(f.t, name, s.binary)
	sum := sha256.Sum256(archive)
	hexSum := hex.EncodeToString(sum[:])
	if s.badSum {
		hexSum = strings.Repeat("0", 64)
	}
	sums := []byte(fmt.Sprintf("%s  %s\n%s  other-file.zip\n", hexSum, name, strings.Repeat("1", 64)))
	rel := Release{Tag: s.tag, Prerelease: s.pre, HTMLURL: "https://example/" + s.tag}
	put := func(n string, b []byte) {
		p := "/download/" + s.tag + "/" + n
		f.files[p] = b
		rel.Assets = append(rel.Assets, Asset{Name: n, URL: f.srv.URL + p, Size: int64(len(b))})
	}
	put(name, archive)
	put(SumsName, sums)
	if !s.noSig {
		comment := s.comment
		if comment == "" {
			comment = "jarvisd " + s.tag + " SHA256SUMS"
		}
		put(SigName, s.signer.sign(sums, comment, true))
	}
	f.releases = append(f.releases, rel)
}

// makeArchive packs bin as <dir>/jarvisd(.exe) the way release.yml does.
func makeArchive(t *testing.T, name string, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		dir := strings.TrimSuffix(name, ".zip")
		zw := zip.NewWriter(&buf)
		for n, b := range map[string][]byte{dir + "/LICENSE": []byte("AGPL"), dir + "/jarvisd.exe": bin} {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(b)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	dir := strings.TrimSuffix(name, ".tar.gz")
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct {
		name string
		b    []byte
	}{{dir + "/LICENSE", []byte("AGPL")}, {dir + "/jarvisd", bin}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(e.b)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
