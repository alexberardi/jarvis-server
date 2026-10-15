// Command agent-upgrade-e2e checks the desktop agent's self-update end to end on this OS
// (01-no-cli-plan item 1.7; CI job agent-upgrade, all three OSes):
//
//	go run ./scripts/agent-upgrade-e2e [-work DIR] [-keep]
//
// It builds the agent at four release versions that trust a throwaway minisign key (test builds
// only, -X …/internal/update.extraTrustedKey, as the jarvisd upgrade jobs do), serves them as a
// fake GitHub (JARVIS_UPDATE_API) next to a fake jarvisd (discovery, heartbeat, MQTT broker,
// update tasks), installs version N where `jarvis-agent install` puts it (~/.local/lib on
// Linux, an ad-hoc signed "Jarvis Automation Agent.app" on macOS, a per-user folder on
// Windows) in a scratch home, and runs it unsupervised (the restart path is quit + a detached
// start). Then:
//
//  1. refusals: `update --version` an older signed release, and an unsigned one; jarvisd's
//     update request for the unsigned one is refused and reported on its task;
//  2. following the server: jarvisd reports N+1, the agent stages, hands over to `update
//     --finish`, which swaps, restarts and sees N+1 connected;
//  3. rollback: jarvisd asks for N+2, a build that fails at start; the update is swapped in,
//     never comes up, and is rolled back to N+1, which reports the failed task.
//
// Never run it on a machine someone uses for the agent: on macOS a loaded
// net.jarvisautomation.agent LaunchAgent would be restarted. It refuses to run outside CI
// unless JARVIS_AGENT_UPGRADE_E2E_OK=1.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"golang.org/x/crypto/blake2b"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/deploy"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/selfupdate"
	"github.com/alexberardi/jarvis-server/internal/update"
)

const (
	vN      = "v9.0.0"
	vNext   = "v9.0.1" // the server's version: followed
	vBroken = "v9.0.2" // fails at start: rolled back
	vNosig  = "v9.0.3" // published unsigned
	vOld    = "v8.9.0" // older, signed
	repo    = update.Repo
)

func main() {
	work := flag.String("work", "", "work directory (default: a temporary one)")
	keep := flag.Bool("keep", false, "keep the work directory")
	flag.Parse()
	if os.Getenv("CI") == "" && os.Getenv("JARVIS_AGENT_UPGRADE_E2E_OK") == "" {
		fail("CI only (set JARVIS_AGENT_UPGRADE_E2E_OK=1 on a machine where no agent runs)")
	}
	e := &e2e{work: *work}
	if e.work == "" {
		d, err := os.MkdirTemp("", "aue")
		must(err)
		e.work = d
	}
	defer func() {
		e.stopAgent()
		if !*keep {
			_ = os.RemoveAll(e.work)
		}
		if e.sock != "" {
			_ = os.RemoveAll(e.sock)
		}
	}()
	e.run()
	say("ok")
}

type e2e struct {
	work string
	sock string // the short socket directory
	pub  string
	priv ed25519.PrivateKey
	exe  string // the installed agent
	env  []string
	srv  *fakeServer
}

func say(format string, a ...any) { fmt.Printf("agent-upgrade-e2e: "+format+"\n", a...) }

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "agent-upgrade-e2e: FAIL: "+format+"\n", a...)
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "jarvis-agent.exe"
	}
	return "jarvis-agent"
}

func (e *e2e) run() {
	e.keygen()
	say("building %s, %s, %s, %s (fails at start), %s (published unsigned)", vOld, vN, vNext, vBroken, vNosig)
	for _, v := range []string{vOld, vN, vNext, vBroken, vNosig} {
		e.build(v, v == vBroken)
	}
	e.srv = newFakeServer(e)
	for _, v := range []string{vOld, vNext, vBroken, vNosig} {
		name, b := e.archive(v)
		e.srv.publish(v, name, b, v != vNosig)
	}
	e.install()
	e.srv.setServerVersion(vN)

	say("1. %s installed and running", vN)
	e.startAgent()
	e.waitHealthy(vN, 60*time.Second)

	say("2. refusals: older (%s) and unsigned (%s) releases", vOld, vNosig)
	out, err := e.agent("update", "--version", vOld)
	if err == nil || !strings.Contains(out, "not newer") {
		fail("update to an older release: %v\n%s", err, out)
	}
	out, err = e.agent("update", "--version", vNosig)
	if err == nil || !strings.Contains(out, "not signed") {
		fail("update to an unsigned release: %v\n%s", err, out)
	}
	e.expectInstalled(vN)
	out, _ = e.agent("update", "--check")
	if !strings.Contains(out, "up to date") {
		fail("update --check with the server on %s:\n%s", vN, out)
	}
	task := e.srv.queueUpdate(vNosig)
	e.restartAgent() // a fresh process heartbeats at once
	msg := e.srv.waitTaskFailed(task, 60*time.Second)
	if !strings.Contains(msg, "not signed") {
		fail("the unsigned update request was reported as %q", msg)
	}
	e.waitHealthy(vN, 60*time.Second)
	e.expectInstalled(vN)

	say("3. the server moves to %s: the agent follows", vNext)
	displayVersion, removeEntry := fakeUninstallEntry(strings.TrimPrefix(vN, "v"))
	defer removeEntry()
	e.srv.setServerVersion(vNext)
	e.restartAgent()
	e.waitResult(selfupdate.OutcomeSucceeded, vNext, 3*time.Minute)
	e.expectInstalled(vNext)
	e.waitHealthy(vNext, 60*time.Second)
	e.srv.waitBeat(vNext, 60*time.Second)
	e.waitLog("Updated to "+vNext+"; the agent is running it.", 60*time.Second) // the installer is done
	if displayVersion != nil && displayVersion() != strings.TrimPrefix(vNext, "v") {
		fail("the uninstall entry's DisplayVersion is %q after the update to %s", displayVersion(), vNext)
	}

	say("4. a release that fails at start (%s) is rolled back to %s", vBroken, vNext)
	task = e.srv.queueUpdate(vBroken)
	e.restartAgent()
	r := e.waitResult(selfupdate.OutcomeRolledBack, vBroken, 4*time.Minute)
	if !strings.Contains(r.Reason, "didn't come up") {
		fail("rollback reason %q", r.Reason)
	}
	e.expectInstalled(vNext)
	e.waitHealthy(vNext, 60*time.Second)
	msg = e.srv.waitTaskFailed(task, 90*time.Second)
	if !strings.Contains(msg, "didn't come up") {
		fail("the rolled-back task was reported as %q", msg)
	}
	e.waitLog("was rolled back to "+vNext, 60*time.Second) // the installer saw it healthy and left
	if strings.Contains(e.readLog(), "isn't healthy either") {
		fail("the restored %s didn't come up", vNext)
	}
	if displayVersion != nil && displayVersion() != strings.TrimPrefix(vNext, "v") {
		fail("the uninstall entry's DisplayVersion is %q after the rollback to %s", displayVersion(), vNext)
	}
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", e.bundle()).CombinedOutput()
		if err != nil {
			fail("the app's signature after the updates: %v\n%s", err, out)
		}
		entries, _ := os.ReadDir(filepath.Dir(e.bundle()))
		if len(entries) != 1 {
			fail("left beside the app: %v", entries)
		}
	}
}

// --- signing and building ---

func (e *e2e) keygen() {
	pub, priv, err := ed25519.GenerateKey(nil)
	must(err)
	id := []byte("aue2etst")
	e.priv = priv
	e.pub = base64.StdEncoding.EncodeToString(append(append([]byte("Ed"), id...), pub...))
}

// sign is a prehashed minisign signature, as release.yml makes (scripts/testsign).
func (e *e2e) sign(msg []byte, comment string) []byte {
	h := blake2b.Sum512(msg)
	sig := ed25519.Sign(e.priv, h[:])
	global := ed25519.Sign(e.priv, append(bytes.Clone(sig), comment...))
	line := append(append([]byte("ED"), []byte("aue2etst")...), sig...)
	return []byte("untrusted comment: agent-upgrade-e2e\n" + base64.StdEncoding.EncodeToString(line) +
		"\ntrusted comment: " + comment + "\n" + base64.StdEncoding.EncodeToString(global) + "\n")
}

func (e *e2e) buildDir(v string) string { return filepath.Join(e.work, "build", v) }

func (e *e2e) build(v string, broken bool) {
	flags := "-X main.version=" + v + " -X github.com/alexberardi/jarvis-server/internal/update.extraTrustedKey=" + e.pub
	if broken {
		flags += " -X main.crashForTest=1"
	}
	out := filepath.Join(e.buildDir(v), exeName())
	c := exec.Command("go", "build", "-trimpath", "-ldflags", flags, "-o", out, "./cmd/jarvis-agent")
	c.Env = append(os.Environ(), "CGO_ENABLED=0")
	c.Stdout, c.Stderr = os.Stderr, os.Stderr
	must(c.Run())
	if runtime.GOOS == "darwin" {
		e.makeBundle(filepath.Join(e.buildDir(v), selfupdate.BundleName), out, v)
	}
}

// makeBundle builds the app as scripts/agent-dev-run.sh does, ad-hoc signed (a release's would
// be Developer ID signed: item 2.2).
func (e *e2e) makeBundle(app, bin, v string) {
	must(os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755))
	must(os.MkdirAll(filepath.Join(app, "Contents", "Library", "LaunchAgents"), 0o755))
	must(copyFile(bin, filepath.Join(app, "Contents", "MacOS", "jarvis-agent"), 0o755))
	must(os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(strings.ReplaceAll(deploy.InfoPlist, "__VERSION__", strings.TrimPrefix(v, "v"))), 0o644))
	must(os.WriteFile(filepath.Join(app, "Contents", "Library", "LaunchAgents", "net.jarvisautomation.agent.plist"), []byte(deploy.BundleLaunchAgent), 0o644))
	out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "net.jarvisautomation.agent", app).CombinedOutput()
	if err != nil {
		fail("codesign %s: %v\n%s", app, err, out)
	}
}

// archive packs a release's agent archive as release.yml does (plus, on macOS, the app that
// item 2.2 will add).
func (e *e2e) archive(v string) (string, []byte) {
	plat := runtime.GOOS + "-" + runtime.GOARCH
	name := update.ProgramArchiveName(update.AgentProgram, v, plat)
	top := strings.TrimSuffix(strings.TrimSuffix(name, ".zip"), ".tar.gz")
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		b, err := os.ReadFile(filepath.Join(e.buildDir(v), exeName()))
		must(err)
		w, err := zw.Create(top + "/" + exeName())
		must(err)
		_, _ = w.Write(b)
		must(zw.Close())
		return name, buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	root := e.buildDir(v)
	must(filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ = os.Readlink(p)
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = top + "/" + filepath.ToSlash(rel)
		if d.IsDir() {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	}))
	must(tw.Close())
	must(gz.Close())
	return name, buf.Bytes()
}

// --- the scratch home and the agent ---

func (e *e2e) home() string { return filepath.Join(e.work, "h") }

func (e *e2e) bundle() string {
	return filepath.Join(e.home(), "Applications", selfupdate.BundleName)
}

func (e *e2e) install() {
	h := e.home()
	must(os.MkdirAll(h, 0o700))
	switch runtime.GOOS {
	case "darwin":
		must(os.MkdirAll(filepath.Dir(e.bundle()), 0o755))
		out, err := exec.Command("/bin/cp", "-R", filepath.Join(e.buildDir(vN), selfupdate.BundleName), e.bundle()).CombinedOutput()
		if err != nil {
			fail("cp app: %v %s", err, out)
		}
		e.exe = filepath.Join(e.bundle(), "Contents", "MacOS", "jarvis-agent")
	case "windows":
		e.exe = filepath.Join(h, "AppData", "Local", "Programs", "Jarvis Automation Agent", exeName())
	default:
		e.exe = filepath.Join(h, ".local", "lib", "jarvis-agent", "jarvis-agent")
	}
	if runtime.GOOS != "darwin" {
		must(os.MkdirAll(filepath.Dir(e.exe), 0o755))
		must(copyFile(filepath.Join(e.buildDir(vN), exeName()), e.exe, 0o755))
	}
	// Short socket paths (macOS sun_path is 104 bytes); a scratch config and cache everywhere.
	sock, err := os.MkdirTemp("", "aus")
	must(err)
	e.sock = sock
	cfg := filepath.Join(h, "cfg")
	e.env = append(os.Environ(),
		"HOME="+h, "USERPROFILE="+h,
		"XDG_CONFIG_HOME="+cfg, "XDG_CACHE_HOME="+filepath.Join(h, "cache"), "XDG_RUNTIME_DIR="+sock,
		"APPDATA="+cfg, "LOCALAPPDATA="+filepath.Join(h, "AppData", "Local"),
		browser.SocketEnv+"="+filepath.Join(sock, "b.sock"), browser.ControlSocketEnv+"="+filepath.Join(sock, "c.sock"),
		"JARVIS_UPDATE_API="+e.srv.url, "JARVIS_AGENT_UPDATE_GATE=25s", "JARVIS_AGENT_NO_AX_PROMPT=1",
		"INVOCATION_ID=", // never a systemd service here, even inside a runner's
	)
	for _, k := range []string{browser.SocketEnv, browser.ControlSocketEnv} {
		must(os.Setenv(k, envOf(e.env, k)))
	}
	// The agent's config: signed in to the fake jarvisd; nothing that needs a desktop.
	conf := e.configDir()
	must(node.SaveCreds(filepath.Join(conf, "node.json"), node.Creds{ConfigURL: e.srv.url, NodeID: "e2e-node", NodeKey: "e2e-key", Room: "desktop", EnrolledAt: time.Now()}))
	must(os.WriteFile(filepath.Join(conf, "agent.json"), []byte(`{"voice": false, "island": false, "tray": false, "browser": false}`), 0o600))
}

// configDir is os.UserConfigDir()/jarvis-agent under the scratch home.
func (e *e2e) configDir() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(e.home(), "Library", "Application Support", "jarvis-agent")
	case "windows":
		return filepath.Join(e.home(), "cfg", "jarvis-agent")
	}
	return filepath.Join(e.home(), "cfg", "jarvis-agent")
}

// updatesDir is os.UserCacheDir()/jarvis-agent/updates under the scratch home.
func (e *e2e) updatesDir() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(e.home(), "Library", "Caches", "jarvis-agent", "updates")
	case "windows":
		return filepath.Join(e.home(), "AppData", "Local", "jarvis-agent", "updates")
	}
	return filepath.Join(e.home(), "cache", "jarvis-agent", "updates")
}

func envOf(env []string, k string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], k+"="); ok {
			return v
		}
	}
	return ""
}

func (e *e2e) agent(args ...string) (string, error) {
	c := exec.Command(e.exe, args...)
	c.Env = e.env
	out, err := c.CombinedOutput()
	return string(out), err
}

// startAgent runs the agent detached, as the login item would (not `start`: on macOS that sets a
// bundle up as a login item first).
func (e *e2e) startAgent() {
	log, err := os.OpenFile(filepath.Join(e.work, "first-run.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(err)
	c := exec.Command(e.exe, "run")
	c.Env = e.env
	c.Stdout, c.Stderr = log, log
	detach(c)
	must(c.Start())
	_ = c.Process.Release()
}

func (e *e2e) restartAgent() {
	if out, err := e.agent("restart"); err != nil {
		fail("restart: %v\n%s", err, out)
	}
}

func (e *e2e) stopAgent() {
	if e.exe != "" {
		_, _ = e.agent("stop")
	}
	if e.srv != nil {
		e.srv.close()
	}
	if os.Getenv("CI") != "" {
		e.dumpLogs()
	}
}

func (e *e2e) dumpLogs() {
	for _, p := range []string{filepath.Join(e.updatesDir(), "update.log"), filepath.Join(filepath.Dir(e.updatesDir()), "agent.log"),
		filepath.Join(e.home(), "Library", "Logs", "jarvis-agent.log")} {
		if b, err := os.ReadFile(p); err == nil {
			fmt.Printf("--- %s (last 4 KB)\n%s\n", p, tail(b, 4096))
		}
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

func dial() (net.Conn, error) { return browser.Dial(os.Getenv(browser.ControlSocketEnv)) }

func (e *e2e) waitHealthy(v string, d time.Duration) {
	last := ""
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		r, err := control.Ask(dial, control.Health)
		if err == nil && r.Version == v {
			return
		}
		last = fmt.Sprintf("%q %v", r.Version, err)
	}
	e.dumpLogs()
	fail("the agent isn't healthy as %s after %s (last: %s)", v, d, last)
}

func (e *e2e) readLog() string {
	b, _ := os.ReadFile(filepath.Join(e.updatesDir(), "update.log"))
	return string(b)
}

// waitLog waits for `update --finish` (detached, logging to update.log) to write s.
func (e *e2e) waitLog(s string, d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if strings.Contains(e.readLog(), s) {
			return
		}
	}
	e.dumpLogs()
	fail("update.log never said %q", s)
}

func (e *e2e) expectInstalled(v string) {
	out, err := exec.Command(e.exe, "version").Output()
	if err != nil || strings.TrimSpace(string(out)) != v {
		fail("installed %s is %q (%v), want %s", e.exe, out, err, v)
	}
}

func (e *e2e) waitResult(outcome, to string, d time.Duration) *selfupdate.Result {
	p := selfupdate.Paths{Dir: e.updatesDir()}
	var last *selfupdate.Result
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(time.Second) {
		r, _ := selfupdate.ReadResult(p)
		if r != nil && r.To == to {
			last = r
			if r.Outcome == outcome {
				if m, _ := selfupdate.ReadMarker(p); m == nil {
					return r
				}
			}
		}
	}
	e.dumpLogs()
	fail("no %s update to %s after %s (last result %+v)", outcome, to, d, last)
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// --- the fake jarvisd and GitHub ---

type fakeServer struct {
	sign   func(msg []byte, comment string) []byte
	url    string
	http   *http.Server
	broker *mqtt.Server

	mu       sync.Mutex
	server   string
	pending  []map[string]any
	failed   map[string]string
	versions []string
	files    map[string][]byte
	releases map[string]update.Release
	nextTask int
}

func newFakeServer(e *e2e) *fakeServer {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	mqttLn, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	mqttAddr := mqttLn.Addr().String()
	_ = mqttLn.Close()
	b := mqtt.New(&mqtt.Options{})
	must(b.AddHook(new(allowAll), nil))
	must(b.AddListener(listeners.NewTCP(listeners.Config{ID: "e2e", Address: mqttAddr})))
	go func() { _ = b.Serve() }()
	f := &fakeServer{sign: e.sign, url: "http://" + ln.Addr().String(), broker: b, failed: map[string]string{},
		files: map[string][]byte{}, releases: map[string]update.Release{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /services", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"services": []map[string]string{
			{"name": node.SvcCommandCenter, "url": f.url},
			{"name": node.SvcMQTT, "url": "mqtt://" + mqttAddr},
		}})
	})
	mux.HandleFunc("GET /api/v0/node/mqtt-credentials", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"username": nil, "password": nil})
	})
	mux.HandleFunc("POST /api/v0/admin/nodes/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			VersionInfo struct {
				Version string `json:"version"`
			} `json:"version_info"`
			Busy bool `json:"is_busy"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.versions = append(f.versions, body.VersionInfo.Version)
		resp := map[string]any{"status": "ok", "server_version": f.server}
		if !body.Busy && len(f.pending) > 0 {
			resp["pending_update"] = f.pending[0]
			f.pending = f.pending[1:]
		}
		writeJSON(w, resp)
	})
	mux.HandleFunc("POST /api/v0/nodes/tasks/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State string `json:"state"`
			Msg   string `json:"error_message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.failed[r.PathValue("id")] = body.Msg
		f.mu.Unlock()
		writeJSON(w, map[string]any{"state": "failed"})
	})
	mux.HandleFunc("GET /repos/"+repo+"/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		rel, ok := f.releases[r.PathValue("tag")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, rel)
	})
	mux.HandleFunc("GET /download/{tag}/{name}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		b, ok := f.files[r.PathValue("tag")+"/"+r.PathValue("name")]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Everything else the agent asks at start (node settings, …): nothing here.
		http.NotFound(w, r)
	})
	f.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = f.http.Serve(ln) }()
	return f
}

// allowAll lets every client connect and use every topic (mochi's own allow hook would pull
// in its YAML ledger).
type allowAll struct{ mqtt.HookBase }

func (*allowAll) ID() string { return "allow-all" }

func (*allowAll) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck
}

func (*allowAll) OnConnectAuthenticate(*mqtt.Client, packets.Packet) bool { return true }

func (*allowAll) OnACLCheck(*mqtt.Client, string, bool) bool { return true }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeServer) close() {
	_ = f.http.Close()
	_ = f.broker.Close()
}

// publish makes a release with the agent archive, its SHA256SUMS and (signed) the signature.
func (f *fakeServer) publish(tag string, name string, archive []byte, signed bool) {
	sum := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
	rel := update.Release{Tag: tag}
	put := func(n string, b []byte) {
		f.files[tag+"/"+n] = b
		rel.Assets = append(rel.Assets, update.Asset{Name: n, URL: f.url + "/download/" + tag + "/" + n, Size: int64(len(b))})
	}
	put(name, archive)
	put(update.SumsName, sums)
	if signed {
		put(update.SigName, f.sign(sums, "jarvisd "+tag+" SHA256SUMS"))
	}
	f.mu.Lock()
	f.releases[tag] = rel
	f.mu.Unlock()
}

func (f *fakeServer) setServerVersion(v string) {
	f.mu.Lock()
	f.server = v
	f.mu.Unlock()
}

func (f *fakeServer) queueUpdate(v string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTask++
	id := fmt.Sprintf("task-%d", f.nextTask)
	f.pending = append(f.pending, map[string]any{"task_id": id, "target_version": v})
	return id
}

func (f *fakeServer) waitTaskFailed(id string, d time.Duration) string {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		f.mu.Lock()
		msg, ok := f.failed[id]
		f.mu.Unlock()
		if ok {
			return msg
		}
	}
	fail("task %s was never reported failed", id)
	return ""
}

func (f *fakeServer) waitBeat(v string, d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		f.mu.Lock()
		n := len(f.versions)
		last := ""
		if n > 0 {
			last = f.versions[n-1]
		}
		f.mu.Unlock()
		if last == v {
			return
		}
	}
	fail("no heartbeat from %s", v)
}
