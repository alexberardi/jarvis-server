package selfupdate

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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/blake2b"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// signer is a throwaway minisign key (prehashed signatures, as release.yml makes).
type signer struct {
	pub  update.PublicKey
	priv ed25519.PrivateKey
}

func newSigner(t *testing.T, id byte) signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte("Ed"), bytes.Repeat([]byte{id}, 8)...), pub...)
	k, err := update.ParsePublicKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	return signer{pub: k, priv: priv}
}

func (s signer) sign(msg []byte, comment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(s.priv, h[:])
	global := ed25519.Sign(s.priv, append(bytes.Clone(sig), comment...))
	line := append(append([]byte("ED"), s.pub.ID[:]...), sig...)
	return []byte("untrusted comment: test\n" + base64.StdEncoding.EncodeToString(line) + "\ntrusted comment: " +
		comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

// fakeBinary is what a test "binary" holds: CheckVersion reads the version back from it.
func fakeBinary(v string) []byte { return []byte("jarvis-agent " + v + "\n") }

func checkFake(bin, tag string) error {
	b, err := os.ReadFile(bin)
	if err != nil {
		return err
	}
	if got := strings.TrimSpace(strings.TrimPrefix(string(b), "jarvis-agent ")); got != tag {
		return fmt.Errorf("the new jarvis-agent reports version %q, expected %s", got, tag)
	}
	return nil
}

// releases is a flat release directory per tag (update.Source.Base), as release.yml lays out.
type releases struct {
	t    *testing.T
	root string
}

type relOpt struct {
	signer   *signer
	comment  string
	bin      []byte // default fakeBinary(tag)
	bundle   bool   // a darwin archive with the app bundle
	unsigned bool
}

func (r releases) add(tag, platform string, o relOpt) {
	r.t.Helper()
	dir := filepath.Join(r.root, tag)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	bin := o.bin
	if bin == nil {
		bin = fakeBinary(tag)
	}
	name := update.ProgramArchiveName(update.AgentProgram, tag, platform)
	top := strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".tar.gz")
	var arch []byte
	if strings.HasSuffix(name, ".zip") {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(top + "/jarvis-agent.exe")
		_, _ = w.Write(bin)
		_ = zw.Close()
		arch = buf.Bytes()
	} else {
		files := map[string][]byte{top + "/jarvis-agent": bin, top + "/LICENSE": []byte("AGPL")}
		if o.bundle {
			files[top+"/"+BundleName+"/Contents/MacOS/jarvis-agent"] = bin
			files[top+"/"+BundleName+"/Contents/Info.plist"] = []byte("plist " + tag)
		}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for n, b := range files {
			_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(b)), Typeflag: tar.TypeReg})
			_, _ = tw.Write(b)
		}
		_ = tw.Close()
		_ = gz.Close()
		arch = buf.Bytes()
	}
	sum := sha256.Sum256(arch)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	write := func(n string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	write(name, arch)
	write(update.SumsName, sums)
	if !o.unsigned {
		c := o.comment
		if c == "" {
			c = "jarvisd " + tag + " SHA256SUMS"
		}
		write(update.SigName, o.signer.sign(sums, c))
	}
}

func (r releases) source(tag string) update.Source {
	return update.Source{Base: filepath.Join(r.root, tag)}
}

type fixture struct {
	t    *testing.T
	rel  releases
	key  signer
	exe  string
	u    *Updater
	now  time.Time
	goos string
}

// newFixture installs a fake agent at version cur for goos (linux-style paths on any host;
// "windows" uses the Windows file names).
func newFixture(t *testing.T, cur, goos string) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{t: t, rel: releases{t: t, root: filepath.Join(root, "rel")}, key: newSigner(t, 7), goos: goos,
		now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	name := "jarvis-agent"
	if goos == "windows" {
		name += ".exe"
	}
	f.exe = filepath.Join(root, "lib", name)
	if err := os.MkdirAll(filepath.Dir(f.exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.exe, fakeBinary(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	f.u = &Updater{Paths: Paths{Dir: filepath.Join(root, "cache", "updates")}, Target: Target{GOOS: goos, Exe: f.exe},
		Current: cur, Keys: []update.PublicKey{f.key.pub}, Platform: platformFor(goos), CheckVersion: checkFake,
		Now: func() time.Time { return f.now }}
	return f
}

func platformFor(goos string) string {
	if goos == "windows" {
		return "windows-amd64"
	}
	if goos == "darwin" {
		return "darwin-arm64"
	}
	return "linux-amd64"
}

func (f *fixture) publish(tag string, o relOpt) {
	if o.signer == nil {
		o.signer = &f.key
	}
	f.rel.add(tag, f.u.Platform, o)
}

func (f *fixture) installed() string {
	b, err := os.ReadFile(f.exe)
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(strings.TrimPrefix(string(b), "jarvis-agent "))
}

// healthyAs is an Agent whose restarted process answers health as whatever is installed,
// unless the installed version is in broken.
func (f *fixture) agent(broken ...string) (*Agent, *[]string) {
	var log []string
	a := &Agent{Running: true, Attempts: 3, Pause: func() {}}
	a.Restart = func(context.Context) error {
		log = append(log, "restart "+f.installed())
		return nil
	}
	a.Health = func() (string, error) {
		v := f.installed()
		for _, b := range broken {
			if v == b {
				return "", errors.New("the agent isn't running")
			}
		}
		return v, nil
	}
	return a, &log
}

func TestUpdateSwapsAndRestarts(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newFixture(t, "v1.0.0", goos)
			f.publish("v1.1.0", relOpt{})
			f.u.Source = f.rel.source("v1.1.0")
			m, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0", TaskID: "t1", By: ByServer})
			if err != nil {
				t.Fatal(err)
			}
			if m.State != StateStaged || f.installed() != "v1.0.0" {
				t.Fatalf("staging touched the agent: %+v %s", m, f.installed())
			}
			a, log := f.agent()
			r := f.u.Finish(context.Background(), *a)
			if r.Outcome != OutcomeSucceeded || r.To != "v1.1.0" || r.TaskID != "t1" {
				t.Fatalf("result %+v", r)
			}
			if f.installed() != "v1.1.0" || len(*log) != 1 || (*log)[0] != "restart v1.1.0" {
				t.Fatalf("installed %s, restarts %v", f.installed(), *log)
			}
			if got, _ := ReadResult(f.u.Paths); got == nil || got.Outcome != OutcomeSucceeded {
				t.Fatalf("recorded %+v", got)
			}
			if m, _ := ReadMarker(f.u.Paths); m != nil {
				t.Fatal("marker left")
			}
			for _, p := range []string{f.u.Paths.Staged(), f.u.Target.prevPath(), f.u.Target.newPath()} {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("left %s", p)
				}
			}
		})
	}
}

func TestFailedStartRollsBack(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f := newFixture(t, "v1.0.0", goos)
			f.publish("v1.1.0", relOpt{})
			f.u.Source = f.rel.source("v1.1.0")
			if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0", By: ByAuto}); err != nil {
				t.Fatal(err)
			}
			a, log := f.agent("v1.1.0") // the new version never answers
			r := f.u.Finish(context.Background(), *a)
			if r.Outcome != OutcomeRolledBack || !strings.Contains(r.Reason, "didn't come up") {
				t.Fatalf("result %+v", r)
			}
			if f.installed() != "v1.0.0" {
				t.Fatalf("installed %s after rollback", f.installed())
			}
			if strings.Join(*log, ",") != "restart v1.1.0,restart v1.0.0" {
				t.Fatalf("restarts %v", *log)
			}
			if m, _ := ReadMarker(f.u.Paths); m != nil {
				t.Fatal("marker left")
			}
		})
	}
}

func TestStageRefuses(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "v1.0.0", "linux")
	other := newSigner(t, 9)
	f.publish("v1.1.0", relOpt{})
	f.publish("v0.9.0", relOpt{})
	f.publish("v1.2.0", relOpt{unsigned: true})
	f.publish("v1.3.0", relOpt{signer: &other})
	f.publish("v1.4.0", relOpt{comment: "jarvisd v1.1.0 SHA256SUMS"})
	f.publish("v1.5.0", relOpt{bin: fakeBinary("v1.4.9")}) // reports another version
	for tag, want := range map[string]string{
		"v1.0.0":  "not newer",
		"v0.9.0":  "not newer",
		"nightly": "not a release version",
		"v1.2.0":  "only signed releases",
		"v1.3.0":  "signature verification failed",
		"v1.4.0":  "doesn't name release v1.4.0",
		"v1.5.0":  `reports version "v1.4.9"`,
	} {
		f.u.Source = f.rel.source(tag)
		_, err := f.u.Stage(ctx, StageOptions{Target: tag, Force: true})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tag, err, want)
		}
		if f.installed() != "v1.0.0" {
			t.Fatalf("%s changed the agent", tag)
		}
		if m, _ := ReadMarker(f.u.Paths); m != nil {
			t.Fatalf("%s left a marker", tag)
		}
	}
	// A development build only when forced, and never "downgrades" a dev build's release check.
	d := newFixture(t, "dev-abc123", "linux")
	d.publish("v1.1.0", relOpt{})
	d.u.Source = d.rel.source("v1.1.0")
	if _, err := d.u.Stage(ctx, StageOptions{Target: "v1.1.0"}); !errors.Is(err, ErrDevBuild) {
		t.Fatalf("dev build: %v", err)
	}
	if _, err := d.u.Stage(ctx, StageOptions{Target: "v1.1.0", Force: true}); err != nil {
		t.Fatalf("forced dev build: %v", err)
	}
	// One update at a time; an abandoned one is cleared after a while.
	if _, err := d.u.Stage(ctx, StageOptions{Target: "v1.1.0", Force: true}); !errors.Is(err, ErrInProgress) {
		t.Fatalf("second stage: %v", err)
	}
	d.now = d.now.Add(staleAfter + time.Minute)
	if _, err := d.u.Stage(ctx, StageOptions{Target: "v1.1.0", Force: true}); err != nil {
		t.Fatalf("after an abandoned update: %v", err)
	}
	if r, _ := ReadResult(d.u.Paths); r == nil || r.Outcome != OutcomeFailed || !strings.Contains(r.Reason, "interrupted") {
		t.Fatalf("abandoned result %+v", r)
	}
}

// What Finish installs is re-verified: a staged archive changed after Stage is refused and the
// agent is left alone.
func TestFinishReverifies(t *testing.T) {
	f := newFixture(t, "v1.0.0", "linux")
	f.publish("v1.1.0", relOpt{})
	f.u.Source = f.rel.source("v1.1.0")
	m, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.u.Paths.Staged(), m.Asset), []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, log := f.agent()
	r := f.u.Finish(context.Background(), *a)
	if r.Outcome != OutcomeFailed || !strings.Contains(r.Reason, "doesn't match") || f.installed() != "v1.0.0" || len(*log) != 0 {
		t.Fatalf("result %+v, installed %s, restarts %v", r, f.installed(), *log)
	}
	// A marker for another executable is refused too.
	f.now = f.now.Add(time.Minute)
	if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	m, _ = ReadMarker(f.u.Paths)
	m.Exe = "/usr/bin/sudo"
	_ = writeMarker(f.u.Paths, m)
	if r := f.u.Finish(context.Background(), *a); r.Outcome != OutcomeFailed || !strings.Contains(r.Reason, "not this agent") {
		t.Fatalf("foreign marker: %+v", r)
	}
}

// With the agent stopped, the update only swaps (and checks the installed binary).
func TestFinishWithTheAgentStopped(t *testing.T) {
	f := newFixture(t, "v1.0.0", "linux")
	f.publish("v1.1.0", relOpt{})
	f.u.Source = f.rel.source("v1.1.0")
	if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0", By: ByCLI}); err != nil {
		t.Fatal(err)
	}
	r := f.u.Finish(context.Background(), Agent{CheckInstalled: func(tag string) error { return checkFake(f.exe, tag) }})
	if r.Outcome != OutcomeSucceeded || f.installed() != "v1.1.0" {
		t.Fatalf("%+v %s", r, f.installed())
	}
}

func TestNotWritable(t *testing.T) {
	f := newFixture(t, "v1.0.0", "linux")
	f.u.Target.Exe = filepath.Join(t.TempDir(), "missing", "jarvis-agent")
	if err := f.u.Allowed("v1.1.0", false); !errors.Is(err, ErrNotWritable) || !strings.Contains(err.Error(), "jarvis-agent install") {
		t.Fatalf("%v", err)
	}
}

// The Pi node: /opt/jarvis-agent belongs to the node's user (install-pi.sh), so the same stage,
// swap, restart and rollback replace the agent there; a root-owned one (an older install-pi.sh)
// says what to run instead of pointing at `jarvis-agent install`.
func TestPiTarget(t *testing.T) {
	f := newFixture(t, "v1.0.0", "linux")
	f.u.Target.Pi = true
	if err := f.u.Allowed("v1.1.0", false); err != nil {
		t.Fatalf("a writable /opt/jarvis-agent: %v", err)
	}
	f.publish("v1.1.0", relOpt{})
	f.u.Source = f.rel.source("v1.1.0")
	if _, err := f.u.Stage(context.Background(), StageOptions{Target: "v1.1.0", TaskID: "t1", By: ByServer}); err != nil {
		t.Fatal(err)
	}
	a, log := f.agent("v1.1.0") // the new version never answers
	if r := f.u.Finish(context.Background(), *a); r.Outcome != OutcomeRolledBack || r.TaskID != "t1" || f.installed() != "v1.0.0" {
		t.Fatalf("result %+v, installed %s, restarts %v", r, f.installed(), *log)
	}

	f.u.Target.Exe = filepath.Join(t.TempDir(), "missing", "jarvis-agent")
	err := f.u.Allowed("v1.1.0", false)
	if !errors.Is(err, ErrNotWritable) || !strings.Contains(err.Error(), "install-pi.sh") || strings.Contains(err.Error(), "jarvis-agent install") {
		t.Fatalf("%v", err)
	}
}
