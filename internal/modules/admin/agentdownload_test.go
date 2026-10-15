package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// agentRelease is a fake GitHub serving one release with agent builds, counting downloads.
type agentRelease struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

type agentReleaseOpts struct {
	unsigned bool   // no SHA256SUMS.minisig
	tamper   string // this file's bytes differ from what SHA256SUMS lists
	signer   *releaseKey
	comment  string // the trusted comment; "" names the tag
}

func newAgentRelease(t *testing.T, k releaseKey, tag string, o agentReleaseOpts) *agentRelease {
	t.Helper()
	f := &agentRelease{files: map[string][]byte{}, hits: map[string]int{}}
	builds := map[string][]byte{
		update.ProgramArchiveName(update.AgentProgram, tag, "linux-amd64"):       []byte("linux agent archive"),
		update.ProgramArchiveName(update.AgentProgram, tag, "darwin-arm64"):      []byte("mac agent archive"),
		update.ProgramArchiveName(update.AgentProgram, tag, "windows-amd64"):     []byte("windows agent zip"),
		"jarvis-automation-agent_" + strings.TrimPrefix(tag, "v") + "_amd64.deb": []byte("agent deb amd64"),
		"Jarvis-Automation-Agent-Setup-" + strings.TrimPrefix(tag, "v") + ".exe": []byte("windows agent setup"),
	}
	var sums strings.Builder
	for n, b := range builds {
		sum := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), n)
		if n == o.tamper {
			b = append([]byte("evil "), b...)
		}
		f.files[n] = b
	}
	f.files[update.SumsName] = []byte(sums.String())
	if !o.unsigned {
		signer, comment := k, o.comment
		if o.signer != nil {
			signer = *o.signer
		}
		if comment == "" {
			comment = "jarvisd " + tag + " SHA256SUMS"
		}
		f.files[update.SigName] = signer.sign(f.files[update.SumsName], comment)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/repos/"+update.Repo+"/releases/tags/"+tag {
			var assets []map[string]any
			for n, b := range f.files {
				assets = append(assets, map[string]any{"name": n, "browser_download_url": f.srv.URL + "/dl/" + n, "size": len(b)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": assets})
			return
		}
		n := strings.TrimPrefix(r.URL.Path, "/dl/")
		if b, ok := f.files[n]; ok {
			f.hits[n]++
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *agentRelease) downloads(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[name]
}

var member = []string{"Authorization", "Bearer member"}

func agentEnv(t *testing.T, version string, k releaseKey, rel *agentRelease) *a4Env {
	t.Helper()
	e := newA4(t, version)
	e.m.Upgrade = UpgradeConfig{Keys: []update.PublicKey{k.pub}}
	if rel != nil {
		e.m.Upgrade.Source = update.Source{APIBase: rel.srv.URL}
	}
	return e
}

func TestAgentDownloadVerifiesCachesAndStreams(t *testing.T) {
	k := newReleaseKey(t)
	rel := newAgentRelease(t, k, "v1.2.0", agentReleaseOpts{})
	e := agentEnv(t, "v1.2.0", k, rel)

	cases := []struct {
		path, file, ctype, body string
	}{
		{"/api/addons/agent/linux/amd64", "jarvis-automation-agent_1.2.0_amd64.deb", "application/vnd.debian.binary-package", "agent deb amd64"},
		{"/api/addons/agent/linux/amd64?format=archive", "jarvis-agent-v1.2.0-linux-amd64.tar.gz", "application/gzip", "linux agent archive"},
		{"/api/addons/agent/darwin/arm64", "jarvis-agent-v1.2.0-darwin-arm64.tar.gz", "application/gzip", "mac agent archive"},
		{"/api/addons/agent/windows/amd64", "Jarvis-Automation-Agent-Setup-1.2.0.exe", "application/vnd.microsoft.portable-executable", "windows agent setup"},
		{"/api/addons/agent/windows/amd64?format=archive", "jarvis-agent-v1.2.0-windows-amd64.zip", "application/zip", "windows agent zip"},
	}
	for _, c := range cases {
		// A member gets it, and asking twice downloads it from the release once.
		for range 2 {
			w := send(e.mux, "GET", c.path, "", member...)
			if w.Code != http.StatusOK || w.Body.String() != c.body {
				t.Fatalf("%s: %d %q", c.path, w.Code, w.Body.String())
			}
			if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="`+c.file+`"` {
				t.Errorf("%s: Content-Disposition %q", c.path, got)
			}
			if got := w.Header().Get("Content-Type"); got != c.ctype {
				t.Errorf("%s: Content-Type %q", c.path, got)
			}
			if got := w.Header().Get("X-Jarvis-Release"); got != "v1.2.0" {
				t.Errorf("%s: X-Jarvis-Release %q", c.path, got)
			}
		}
		if n := rel.downloads(c.file); n != 1 {
			t.Errorf("%s fetched %d times, want 1 (cached)", c.file, n)
		}
		cached := filepath.Join(e.m.deps.Config.Home, "updates", "agent", "v1.2.0", c.file, c.file)
		if b, err := os.ReadFile(cached); err != nil || string(b) != c.body {
			t.Errorf("cache %s: %q %v", cached, b, err)
		}
	}

	// A cached copy altered on disk is never served: it is fetched and verified again.
	cached := filepath.Join(e.m.deps.Config.Home, "updates", "agent", "v1.2.0", cases[0].file, cases[0].file)
	if err := os.WriteFile(cached, []byte("swapped on disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := send(e.mux, "GET", cases[0].path, "", member...); w.Code != http.StatusOK || w.Body.String() != cases[0].body {
		t.Fatalf("after tampering with the cache: %d %q", w.Code, w.Body.String())
	}
	if n := rel.downloads(cases[0].file); n != 2 {
		t.Errorf("refetches after tampering: %d, want 2", n)
	}

	// A range request is honoured (resumed downloads).
	w := send(e.mux, "GET", cases[0].path, "", append(member, "Range", "bytes=0-4")...)
	if w.Code != http.StatusPartialContent || w.Body.String() != "agent" {
		t.Errorf("range: %d %q", w.Code, w.Body.String())
	}
}

func TestAgentDownloadRefusesUnverified(t *testing.T) {
	k := newReleaseKey(t)
	other := newReleaseKey(t)
	deb := "jarvis-automation-agent_1.2.0_amd64.deb"
	for name, o := range map[string]agentReleaseOpts{
		"unsigned":               {unsigned: true},
		"tampered":               {tamper: deb},
		"signed by another key":  {signer: &other},
		"signed for another tag": {comment: "jarvisd v1.1.0 SHA256SUMS"},
	} {
		t.Run(name, func(t *testing.T) {
			rel := newAgentRelease(t, k, "v1.2.0", o)
			e := agentEnv(t, "v1.2.0", k, rel)
			w := send(e.mux, "GET", "/api/addons/agent/linux/amd64", "", member...)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("%d %q", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "agent deb") {
				t.Fatal("the unverified file's bytes reached the browser")
			}
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			if out["release_url"] != "https://github.com/alexberardi/jarvis-server/releases/tag/v1.2.0" {
				t.Errorf("release_url %v", out["release_url"])
			}
			// Nothing is kept: not the file, not its directory.
			if _, err := os.Stat(filepath.Join(e.m.deps.Config.Home, "updates", "agent", "v1.2.0", deb)); !os.IsNotExist(err) {
				t.Errorf("something was cached: %v", err)
			}
			// Unsigned: nothing large is downloaded at all.
			if name == "unsigned" && rel.downloads(deb) != 0 {
				t.Errorf("the archive was downloaded for an unsigned release")
			}
		})
	}
}

func TestAgentDownloadRequests(t *testing.T) {
	k := newReleaseKey(t)
	rel := newAgentRelease(t, k, "v1.2.0", agentReleaseOpts{})
	e := agentEnv(t, "v1.2.0", k, rel)
	for _, c := range []struct {
		path string
		hdr  []string
		want int
	}{
		{"/api/addons/agent/linux/amd64", nil, http.StatusUnauthorized},
		{"/api/addons/agent/linux/riscv64", member, http.StatusNotFound},
		{"/api/addons/agent/darwin/amd64", member, http.StatusNotFound}, // no Intel Mac build
		{"/api/addons/agent/freebsd/amd64", member, http.StatusNotFound},
		{"/api/addons/agent/darwin/arm64?format=installer", member, http.StatusNotFound}, // no dmg yet
		{"/api/addons/agent/linux/amd64?format=rpm", member, http.StatusNotFound},
	} {
		if w := send(e.mux, "GET", c.path, "", c.hdr...); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.path, w.Code, w.Body.String(), c.want)
		}
	}
	// A dev build has no release to match: 409 with the release page, and nothing fetched.
	dev := agentEnv(t, "dev", k, rel)
	w := send(dev.mux, "GET", "/api/addons/agent/linux/amd64", "", member...)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "/releases") {
		t.Errorf("dev build: %d %s", w.Code, w.Body.String())
	}
	if n := rel.downloads(update.SumsName); n != 0 {
		t.Errorf("SHA256SUMS fetched %d times by the rejected requests", n)
	}
}

func TestAgentDownloadPrunesOtherReleases(t *testing.T) {
	k := newReleaseKey(t)
	rel := newAgentRelease(t, k, "v1.2.0", agentReleaseOpts{})
	e := agentEnv(t, "v1.2.0", k, rel)
	old := filepath.Join(e.m.deps.Config.Home, "updates", "agent", "v1.1.0", "x", "x")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := send(e.mux, "GET", "/api/addons/agent/linux/amd64", "", member...); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(old))); !os.IsNotExist(err) {
		t.Errorf("the old release's copy is still there: %v", err)
	}
}
