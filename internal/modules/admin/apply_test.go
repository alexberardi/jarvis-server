package admin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
	"golang.org/x/crypto/blake2b"
)

type fakeRestarter struct {
	kind      service.Kind
	requested atomic.Int32
}

func (f *fakeRestarter) Kind() service.Kind { return f.kind }
func (f *fakeRestarter) Request() error {
	if !f.kind.Supervised() {
		return service.ErrUnsupervised
	}
	f.requested.Add(1)
	return nil
}

func noRestartDelay(t *testing.T) {
	old := restartDelay
	restartDelay = 0
	t.Cleanup(func() { restartDelay = old })
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRestartRoute(t *testing.T) {
	noRestartDelay(t)
	e := newA4(t, "v1.0.0")
	// Unsupervised: 409 with the command.
	w := send(e.mux, "POST", "/api/system/restart", "", root...)
	out := decode(t, w)
	if w.Code != http.StatusConflict || out["supervisor"] != "none" || !strings.Contains(fmt.Sprint(out["command"]), "jarvisd serve") {
		t.Fatalf("%d %v", w.Code, out)
	}
	// Supervised: 202, then the supervisor restart is requested.
	r := &fakeRestarter{kind: service.Systemd}
	e.m.Restarter = r
	w = send(e.mux, "POST", "/api/system/restart", "", root...)
	if out := decode(t, w); w.Code != http.StatusAccepted || out["supervisor"] != "systemd" || out["restarting"] != true {
		t.Fatalf("%d %v", w.Code, out)
	}
	waitFor(t, "restart request", func() bool { return r.requested.Load() == 1 })
	// Superuser only.
	if w := send(e.mux, "POST", "/api/system/restart", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	// System info names the supervisor.
	info := decode(t, send(e.mux, "GET", "/api/system/info", "", root...))
	caps, _ := info["capabilities"].(map[string]any)
	if info["supervisor"] != "systemd" || info["restart_supported"] != true || caps["restart"] != true || caps["self_update"] != false {
		t.Fatalf("info %v", info)
	}
}

func TestRestartCommands(t *testing.T) {
	for kind, want := range map[service.Kind]string{
		service.Systemd: "sudo systemctl restart jarvisd",
		service.Launchd: "sudo launchctl kickstart -k system/net.jarvisautomation.jarvisd",
		service.SCM:     "Restart-Service jarvisd",
	} {
		if got := service.RestartCommand(kind, false); got != want {
			t.Errorf("%s: %q", kind, got)
		}
	}
	if got := service.RestartCommand(service.Systemd, true); got != "systemctl --user restart jarvisd" {
		t.Error(got)
	}
}

// --- a fake signed release ---

type releaseKey struct {
	pub  update.PublicKey
	priv ed25519.PrivateKey
}

func newReleaseKey(t *testing.T) releaseKey {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return releaseKey{update.PublicKey{ID: [8]byte{9, 9, 9, 9, 9, 9, 9, 9}, Key: pub}, priv}
}

// sign writes a prehashed minisign signature.
func (k releaseKey) sign(msg []byte, trusted string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(k.priv, h[:])
	global := ed25519.Sign(k.priv, append(bytes.Clone(sig), trusted...))
	line := append(append([]byte("ED"), k.pub.ID[:]...), sig...)
	return []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(line) + "\ntrusted comment: " + trusted +
		"\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func packArchive(t *testing.T, name string, bin []byte) []byte {
	var buf bytes.Buffer
	if strings.HasSuffix(name, ".zip") {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(strings.TrimSuffix(name, ".zip") + "/jarvisd.exe")
		_, _ = w.Write(bin)
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: strings.TrimSuffix(name, ".tar.gz") + "/jarvisd", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(bin)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// signedRelease serves one signed release of tag through a fake GitHub.
func signedRelease(t *testing.T, k releaseKey, tag string, bin []byte) *httptest.Server {
	name := update.ArchiveName(tag, update.Platform())
	arch := packArchive(t, name, bin)
	sum := sha256.Sum256(arch)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	files := map[string][]byte{name: arch, update.SumsName: sums, update.SigName: k.sign(sums, "jarvisd "+tag+" SHA256SUMS")}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+update.Repo+"/releases" {
			var assets []map[string]any
			for n, b := range files {
				assets = append(assets, map[string]any{"name": n, "browser_download_url": srv.URL + "/dl/" + n, "size": len(b)})
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": tag, "assets": assets}})
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/dl/")]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestApplyUpdate(t *testing.T) {
	noRestartDelay(t)
	t.Setenv(EnvAllowUpdates, "")
	e := newA4(t, "v1.0.0")
	k := newReleaseKey(t)
	srv := signedRelease(t, k, "v1.1.0", []byte("new binary"))
	exe := filepath.Join(t.TempDir(), "jarvisd")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.m.Updates = UpdateOptions{APIBase: srv.URL}
	e.m.Upgrade = UpgradeConfig{Exe: exe, Keys: []update.PublicKey{k.pub}, SkipVersionCheck: true}
	homeDB, err := db.Open(context.Background(), filepath.Join(e.m.deps.Config.Home, "jarvis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { homeDB.Close() })

	apply := func() (int, map[string]any) {
		w := send(e.mux, "POST", "/api/update/apply", "", append([]string{"Content-Type", "application/json"}, root...)...)
		return w.Code, decode(t, w)
	}
	// Updates off: refused, nothing fetched.
	if code, out := apply(); code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["detail"]), SettingUpdatesEnabled) {
		t.Fatalf("off: %d %v", code, out)
	}
	send(e.mux, "PUT", "/api/update/settings", `{"enabled":true}`, root...)
	// Unsupervised: refused with the CLI command.
	if code, out := apply(); code != http.StatusConflict || out["command"] != "jarvisd upgrade" {
		t.Fatalf("unsupervised: %d %v", code, out)
	}
	st := decode(t, send(e.mux, "GET", "/api/update", "", root...))
	if st["can_apply"] != false || st["apply_command"] != "jarvisd upgrade" {
		t.Fatalf("status %v", st)
	}

	r := &fakeRestarter{kind: service.Systemd}
	e.m.Restarter = r
	info := decode(t, send(e.mux, "GET", "/api/system/info", "", root...))
	if caps, _ := info["capabilities"].(map[string]any); caps["self_update"] != true || caps["restart"] != true {
		t.Fatalf("capabilities %v", info["capabilities"])
	}
	code, out := apply()
	if code != http.StatusAccepted {
		t.Fatalf("apply: %d %v", code, out)
	}
	waitFor(t, "restart", func() bool { return r.requested.Load() == 1 })
	status := decode(t, send(e.mux, "GET", "/api/update/apply", "", root...))
	job, _ := status["job"].(map[string]any)
	if job["state"] != "restarting" || job["to_version"] != "v1.1.0" || job["from_version"] != "v1.0.0" {
		t.Fatalf("job %v", status)
	}
	if p, _ := status["pending"].(map[string]any); p["state"] != update.StateSwapped || p["to_version"] != "v1.1.0" {
		t.Fatalf("pending %v", status)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("exe %q", b)
	}
	if b, _ := os.ReadFile(exe + ".prev"); string(b) != "old binary" {
		t.Fatalf("prev %q", b)
	}
	// The database under home was snapshotted before the swap.
	if m, _ := filepath.Glob(filepath.Join(e.m.deps.Config.Home, "backups", "jarvis-v1.0.0-*.db")); len(m) != 1 {
		t.Fatalf("snapshots %v", m)
	}
	// The upgrade is pending (until the restarted binary passes its gate): another is refused.
	if code, out := apply(); code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["detail"]), "in progress") {
		t.Fatalf("second: %d %v", code, out)
	}
}

// TestApplyFailureReported: a bad signature fails the job with the reason; nothing swapped,
// no restart.
func TestApplyFailureReported(t *testing.T) {
	noRestartDelay(t)
	t.Setenv(EnvAllowUpdates, "1")
	e := newA4(t, "v1.0.0")
	srv := signedRelease(t, newReleaseKey(t), "v1.1.0", []byte("new binary"))
	exe := filepath.Join(t.TempDir(), "jarvisd")
	_ = os.WriteFile(exe, []byte("old binary"), 0o755)
	r := &fakeRestarter{kind: service.Launchd}
	e.m.Restarter = r
	e.m.Updates = UpdateOptions{APIBase: srv.URL}
	e.m.Upgrade = UpgradeConfig{Exe: exe, Keys: []update.PublicKey{newReleaseKey(t).pub}, SkipVersionCheck: true}
	w := send(e.mux, "POST", "/api/update/apply", "", root...)
	if w.Code != http.StatusAccepted {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var job map[string]any
	waitFor(t, "failure", func() bool {
		job, _ = decode(t, send(e.mux, "GET", "/api/update/apply", "", root...))["job"].(map[string]any)
		return job["state"] == "failed"
	})
	if !strings.Contains(fmt.Sprint(job["error"]), "signature") {
		t.Fatalf("job %v", job)
	}
	if r.requested.Load() != 0 {
		t.Fatal("restarted after a failure")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old binary" {
		t.Fatal("swapped")
	}
}
