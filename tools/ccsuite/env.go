package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// Env is one run's world, in a private run dir and never the user's session: a sandbox jarvisd
// (scripts/sandbox up --name ccsuite-<run>), the fixture sites (tools/ccsites), and, only when a
// case needs them, a desktop session (headless sway, or Xvfb for X11) with the real agent
// enrolled to the sandbox and Playwright's Chromium carrying the extension. Down takes it all
// away again; every process it starts is in its own process group.
type Env struct {
	Root     string        // the jarvis-server checkout
	Dir      string        // the run dir (absolute): bin/, logs/, agent-home/, browser/, ...
	Name     string        // the sandbox: ccsuite-<run>
	LLM      string        // shared or scripted
	Label    string        // shared: the installed jarvisd's label (live, background); "" = as configured
	Chromium string        // the browser binary; "" = Playwright's newest Chromium
	Keep     bool          // leave the sandbox up at the end
	Settle   time.Duration // an errand is over once nothing happened this long after the chat's answer
	Log      io.Writer

	Sbx       map[string]string // sandbox.env
	Sites     string            // the fixture sites' base URL
	Household string            // the seeded superuser's household
	Model     string            // what the sandbox's live label runs
	NodeID    string            // the agent's node id, once enrolled

	sitesProc *proc
	sandboxUp bool
	desk      *desktop
	creds     string
}

// desktop is a running session: its kind (wlroots, x11), its processes and the environment its
// clients get.
type desktop struct {
	kind    string
	rt      string // its XDG_RUNTIME_DIR (short: unix socket paths)
	env     []string
	session *proc // sway or Xvfb, under dbus-run-session
	agent   *proc
	browser *proc
	cdp     *cdpPipe
	notes   *proc
	notesSt string // the notes app's state file
}

// proc is a started process group.
type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
}

func (e *Env) logf(format string, a ...any) { fmt.Fprintf(e.Log, "ccsuite: "+format+"\n", a...) }

func (e *Env) path(p ...string) string { return filepath.Join(append([]string{e.Dir}, p...)...) }

// start runs a command in its own process group, output to logs/<name>.log.
func (e *Env) start(name string, cmd *exec.Cmd) (*proc, error) {
	f, err := os.OpenFile(e.path("logs", name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if cmd.Stdout == nil {
		cmd.Stdout = f
	}
	cmd.Stderr = f
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	p := &proc{name: name, cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); f.Close(); close(p.done) }()
	return p, nil
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop ends the whole group: TERM, then KILL after 3 s.
func (p *proc) stop() {
	if p == nil {
		return
	}
	signalGroup(p.cmd, false)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		signalGroup(p.cmd, true)
		<-p.done
	}
	signalGroup(p.cmd, true) // stragglers that outlived the leader
}

func goBin() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// Up builds the binaries, brings the sandbox up and starts the fixture sites.
func (e *Env) Up(ctx context.Context) error {
	for _, d := range []string{"bin", "logs", "agent-home"} {
		if err := os.MkdirAll(e.path(d), 0o755); err != nil {
			return err
		}
	}
	e.logf("building jarvis-agent and ccsites (logs/build.log)")
	for _, b := range [][2]string{{"jarvis-agent", "./cmd/jarvis-agent"}, {"ccsites", "./tools/ccsites"}} {
		cmd := exec.CommandContext(ctx, goBin(), "build", "-o", e.path("bin", b[0]), b[1])
		cmd.Dir, cmd.Env = e.Root, append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			os.WriteFile(e.path("logs", "build.log"), out, 0o644)
			return fmt.Errorf("building %s: %w (logs/build.log)", b[0], err)
		}
	}
	if err := e.sandbox(ctx); err != nil {
		return err
	}
	return e.startSites(ctx)
}

var tapLine = regexp.MustCompile(`llm: tap .*\((live|background) = ([^)]*)\)`)

func (e *Env) sandbox(ctx context.Context) error {
	e.logf("sandbox %s up (--llm %s; logs/sandbox.log)", e.Name, e.LLM)
	cmd := exec.CommandContext(ctx, filepath.Join(e.Root, "scripts", "sandbox"), "up", "--name", e.Name, "--llm", e.LLM)
	cmd.Dir, cmd.Env = e.Root, os.Environ()
	if e.LLM == "shared" && e.Label != "" {
		// The label is read from the harness env file: a file of ours that sources it and
		// overrides only the label (no credential is copied).
		orig := os.Getenv("JARVIS_HARNESS_ENV")
		if orig == "" {
			home, _ := os.UserHomeDir()
			orig = filepath.Join(home, ".config", "jarvis-harness", "installed.env")
		}
		f := e.path("harness.env")
		body := fmt.Sprintf("[ -f %q ] && . %q\nINSTALLED_LLM_LABEL=%s\n", orig, orig, e.Label)
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			return err
		}
		cmd.Env = append(cmd.Env, "JARVIS_HARNESS_ENV="+f)
	}
	log, err := os.Create(e.path("logs", "sandbox.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	e.sandboxUp = true // from here on, Down takes it down even if up failed half way
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scripts/sandbox up: %w (logs/sandbox.log)", err)
	}
	f, err := os.Open(filepath.Join(e.Root, ".sandbox", e.Name, "sandbox.env"))
	if err != nil {
		return err
	}
	defer f.Close()
	kvs, err := config.ParseEnv(f)
	if err != nil {
		return err
	}
	e.Sbx = map[string]string{}
	for _, kv := range kvs {
		e.Sbx[kv[0]] = kv[1]
	}
	for _, k := range []string{"SBX_CC", "SBX_CONFIG", "SBX_AUTH", "SBX_NOTIFICATIONS", "SBX_TOKEN", "SBX_EMAIL", "SBX_PASSWORD"} {
		if e.Sbx[k] == "" {
			return fmt.Errorf("sandbox.env has no %s", k)
		}
	}
	e.Model, e.Label = "scripted", "scripted"
	if e.LLM == "shared" {
		e.Model, e.Label = "unknown", firstNonEmpty(e.Label, "live")
		if b, err := os.ReadFile(e.path("logs", "sandbox.log")); err == nil {
			if m := tapLine.FindStringSubmatch(string(b)); m != nil {
				e.Label, e.Model = m[1], strings.TrimSpace(m[2])
			}
		}
	}
	var hhs []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	if err := e.api(ctx, "GET", e.Sbx["SBX_AUTH"]+"/households", nil, &hhs); err != nil {
		return fmt.Errorf("households: %w", err)
	}
	for _, h := range hhs {
		if e.Household == "" || h.Role == "admin" {
			e.Household = h.ID
		}
		if h.Role == "admin" {
			break
		}
	}
	if e.Household == "" {
		return errors.New("the seeded user has no household")
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

func (e *Env) startSites(ctx context.Context) error {
	cmd := exec.Command(e.path("bin", "ccsites"), "--listen", "127.0.0.1:0", "--state", e.path("sites"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if e.sitesProc, err = e.start("ccsites", cmd); err != nil {
		return err
	}
	line := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			select {
			case line <- sc.Text():
			default:
			}
		}
	}()
	select {
	case l := <-line:
		u, ok := strings.CutPrefix(l, "ccsites: ")
		if !ok {
			return fmt.Errorf("ccsites said %q (logs/ccsites.log)", l)
		}
		e.Sites = u
	case <-time.After(15 * time.Second):
		return errors.New("ccsites did not start (logs/ccsites.log)")
	case <-ctx.Done():
		return ctx.Err()
	}
	e.logf("fixture sites at %s", e.Sites)
	return nil
}

// Down stops everything Up and the cases started, and purges the sandbox unless Keep.
func (e *Env) Down() {
	e.stopDesktop()
	e.sitesProc.stop()
	if !e.sandboxUp {
		return
	}
	if e.Keep {
		e.logf("kept: sandbox %s (scripts/sandbox down --name %s --purge)", e.Name, e.Name)
		return
	}
	cmd := exec.Command(filepath.Join(e.Root, "scripts", "sandbox"), "down", "--name", e.Name, "--purge")
	cmd.Dir = e.Root
	if out, err := cmd.CombinedOutput(); err != nil {
		e.logf("sandbox down: %v: %s", err, strings.TrimSpace(string(out)))
	} else {
		e.logf("sandbox %s purged", e.Name)
	}
}

// api is a JSON call as the seeded superuser.
func (e *Env) api(ctx context.Context, method, url string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.Sbx != nil {
		req.Header.Set("Authorization", "Bearer "+e.Sbx["SBX_TOKEN"])
	}
	return doJSON(req, out)
}

func doJSON(req *http.Request, out any) error {
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		s := strings.TrimSpace(string(b))
		if len(s) > 300 {
			s = s[:300]
		}
		return fmt.Errorf("%s %s: %d %s", req.Method, req.URL.Path, resp.StatusCode, s)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// --- the desktop session -----------------------------------------------------------------------

// baseEnv is a client's whole environment: nothing of the user's session (display, session bus,
// audio) gets through, and HOME is the agent's own.
func (e *Env) baseEnv(rt string) []string {
	h := e.path("agent-home")
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + h, "XDG_CONFIG_HOME=" + h + "/.config",
		"XDG_CACHE_HOME=" + h + "/.cache", "XDG_DATA_HOME=" + h + "/.local/share", "XDG_STATE_HOME=" + h + "/.local/state",
		"XDG_RUNTIME_DIR=" + rt, "LANG=C.UTF-8", "TZ=" + firstNonEmpty(os.Getenv("TZ"), "UTC")}
}

// Desktop makes sure a session of kind (wlroots, x11) is up with the agent in it, and a browser
// when browser is set; a session of the other kind is stopped first.
func (e *Env) Desktop(ctx context.Context, kind string, wantBrowser bool) (*desktop, error) {
	if e.desk != nil && (e.desk.kind != kind || !e.desk.session.alive() || !e.desk.agent.alive()) {
		e.stopDesktop()
	}
	if e.desk == nil {
		if err := e.enroll(ctx); err != nil {
			return nil, err
		}
		d, err := e.startSession(ctx, kind)
		if err != nil {
			return nil, err
		}
		e.desk = d
		if err := e.startAgent(ctx, d); err != nil {
			return nil, err
		}
	}
	if wantBrowser && (e.desk.browser == nil || !e.desk.browser.alive()) {
		if err := e.startBrowser(ctx, e.desk); err != nil {
			return nil, err
		}
	}
	return e.desk, nil
}

func (e *Env) stopDesktop() {
	d := e.desk
	if d == nil {
		return
	}
	e.desk = nil
	if d.cdp != nil {
		d.cdp.Close()
	}
	d.notes.stop()
	d.browser.stop()
	d.agent.stop()
	d.session.stop()
	os.RemoveAll(d.rt)
}

// enroll makes the agent a node of the sandbox's household, once a run (its own HOME).
func (e *Env) enroll(ctx context.Context) error {
	if e.creds != "" {
		return nil
	}
	creds := e.path("agent-home", "creds.json")
	cmd := exec.CommandContext(ctx, e.path("bin", "jarvis-agent"), "enroll", "--server", e.Sbx["SBX_CONFIG"],
		"--email", e.Sbx["SBX_EMAIL"], "--password-stdin", "--name", "ccsuite", "--room", "desktop",
		"--no-browser", "--profile", "desktop", "--creds", creds, "--force")
	cmd.Env = e.baseEnv(os.TempDir())
	cmd.Stdin = strings.NewReader(e.Sbx["SBX_PASSWORD"])
	out, err := cmd.CombinedOutput()
	os.WriteFile(e.path("logs", "enroll.log"), out, 0o644)
	if err != nil {
		return fmt.Errorf("jarvis-agent enroll: %w (logs/enroll.log)", err)
	}
	var c struct {
		NodeID string `json:"node_id"`
	}
	b, err := os.ReadFile(creds)
	if err == nil {
		err = json.Unmarshal(b, &c)
	}
	if err != nil || c.NodeID == "" {
		return fmt.Errorf("enroll wrote no node id to %s (%v)", creds, err)
	}
	e.creds, e.NodeID = creds, c.NodeID
	e.logf("agent enrolled: node %s", e.NodeID)
	return nil
}

// findSway is scripts/desktop-shot's: sway on PATH, else the one `desktop-shot deps` built.
func findSway() (sway, swaymsg string, err error) {
	if p, err := exec.LookPath("sway"); err == nil {
		m, _ := exec.LookPath("swaymsg")
		return p, m, nil
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		home, _ := os.UserHomeDir()
		cache = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(cache, "jarvis-harness", "desktop", "sway-1.12", "bin")
	if _, err := os.Stat(filepath.Join(dir, "sway")); err == nil {
		return filepath.Join(dir, "sway"), filepath.Join(dir, "swaymsg"), nil
	}
	return "", "", errors.New("no sway: scripts/desktop-shot deps (builds it into ~/.cache, no sudo), or sudo pacman -S sway")
}

// runtimeDir is a short private dir: a unix socket's path has ~100 bytes at most.
func runtimeDir() (string, error) {
	for _, base := range []string{"/tmp", os.TempDir()} {
		if d, err := os.MkdirTemp(base, "ccs."); err == nil {
			return d, nil
		}
	}
	return "", errors.New("no temp dir for the session's XDG_RUNTIME_DIR")
}

// startSession starts sway (headless, Xwayland on) or Xvfb under its own dbus-run-session.
func (e *Env) startSession(ctx context.Context, kind string) (*desktop, error) {
	if _, err := exec.LookPath("dbus-run-session"); err != nil {
		return nil, errors.New("dbus-run-session not found (Arch: sudo pacman -S dbus)")
	}
	rt, err := runtimeDir()
	if err != nil {
		return nil, err
	}
	d := &desktop{kind: kind, rt: rt}
	bus := filepath.Join(rt, "bus")
	env := e.baseEnv(rt)
	var server []string
	switch kind {
	case "wlroots":
		sway, _, err := findSway()
		if err != nil {
			os.RemoveAll(rt)
			return nil, err
		}
		cfg := filepath.Join(rt, "sway.conf")
		if err := os.WriteFile(cfg, []byte("output HEADLESS-1 mode 1280x800 position 0 0\nxwayland enable\n"+
			"default_border none\nfocus_follows_mouse no\n"), 0o644); err != nil {
			return nil, err
		}
		server = []string{sway, "-c", cfg}
		env = append(env, "WLR_BACKENDS=headless", "WLR_HEADLESS_OUTPUTS=1", "WLR_LIBINPUT_NO_DEVICES=1",
			"WLR_RENDERER=pixman", "XDG_CURRENT_DESKTOP=sway", "XDG_SESSION_TYPE=wayland")
	case "x11":
		xvfb, err := exec.LookPath("Xvfb")
		if err != nil {
			os.RemoveAll(rt)
			return nil, errors.New("Xvfb not found: install it (Arch: sudo pacman -S xorg-server-xvfb)")
		}
		n := freeDisplay()
		server = []string{xvfb, fmt.Sprintf(":%d", n), "-screen", "0", "1280x800x24", "-nolisten", "tcp"}
		env = append(env, fmt.Sprintf("DISPLAY=:%d", n), "XDG_SESSION_TYPE=x11")
	default:
		return nil, fmt.Errorf("no %s session", kind)
	}
	// The bus address is known only inside dbus-run-session: the session writes it down.
	args := append([]string{"--", "sh", "-c", `printf %s "$DBUS_SESSION_BUS_ADDRESS" >"$0"; exec "$@"`, bus}, server...)
	cmd := exec.Command("dbus-run-session", args...)
	cmd.Env = env
	if d.session, err = e.start("session-"+kind, cmd); err != nil {
		os.RemoveAll(rt)
		return nil, err
	}
	ready := func() []string {
		b, _ := os.ReadFile(bus)
		if len(b) == 0 {
			return nil
		}
		extra := []string{"DBUS_SESSION_BUS_ADDRESS=" + string(b)}
		if kind == "x11" {
			for _, v := range env {
				if strings.HasPrefix(v, "DISPLAY=") {
					if _, err := os.Stat("/tmp/.X11-unix/X" + strings.TrimPrefix(v, "DISPLAY=:")); err != nil {
						return nil
					}
				}
			}
			return extra
		}
		way, _ := filepath.Glob(filepath.Join(rt, "wayland-[0-9]"))
		ipc, _ := filepath.Glob(filepath.Join(rt, "sway-ipc.*.sock"))
		if len(way) == 0 || len(ipc) == 0 {
			return nil
		}
		return append(extra, "WAYLAND_DISPLAY="+filepath.Base(way[0]), "SWAYSOCK="+ipc[0])
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if extra := ready(); extra != nil {
			d.env = append(env, extra...)
			break
		}
		if !d.session.alive() || time.Now().After(deadline) || ctx.Err() != nil {
			d.session.stop()
			os.RemoveAll(rt)
			return nil, fmt.Errorf("the %s session did not come up (logs/session-%s.log)", kind, kind)
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.logf("%s session up (XDG_RUNTIME_DIR=%s)", kind, rt)
	return d, nil
}

// freeDisplay is the first X display number from 90 with neither a socket nor a lock file.
func freeDisplay() int {
	for n := 90; ; n++ {
		_, s := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", n))
		_, l := os.Stat(fmt.Sprintf("/tmp/.X%d-lock", n))
		if s != nil && l != nil {
			return n
		}
	}
}

// startAgent runs the real agent in the session, with its own control and browser sockets.
func (e *Env) startAgent(ctx context.Context, d *desktop) error {
	ctl := filepath.Join(d.rt, "control.sock")
	cmd := exec.Command(e.path("bin", "jarvis-agent"), "run", "--profile", "desktop", "--creds", e.creds,
		"--voice=false", "--no-auto-update")
	cmd.Env = append(d.env, browser.ControlSocketEnv+"="+ctl, browser.SocketEnv+"="+filepath.Join(d.rt, "browser.sock"))
	var err error
	if d.agent, err = e.start("agent", cmd); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ctl); err == nil {
			break
		}
		if !d.agent.alive() || time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("the agent did not start (logs/agent.log)")
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.logf("agent running (logs/agent.log)")
	return nil
}

// findChromium is Playwright's newest Chromium (~/.cache/ms-playwright/chromium-*), which still
// takes --load-extension.
func findChromium() (string, error) {
	if p := os.Getenv("CCSUITE_CHROMIUM"); p != "" {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	base := firstNonEmpty(os.Getenv("PLAYWRIGHT_BROWSERS_PATH"), filepath.Join(home, ".cache", "ms-playwright"))
	found, _ := filepath.Glob(filepath.Join(base, "chromium-*", "chrome-linux*", "chrome"))
	if len(found) == 0 {
		return "", fmt.Errorf("no Playwright Chromium under %s (cd e2e/web && npx playwright install chromium), or --chromium PATH", base)
	}
	sort.Strings(found)
	return found[len(found)-1], nil
}

// startBrowser starts Chromium in the session with a throwaway profile: the extension loaded
// unpacked (jarvis-agent browser pack), the native-messaging host registered in the profile, and
// a DevTools pipe for the session cookies.
func (e *Env) startBrowser(ctx context.Context, d *desktop) error {
	bin := e.Chromium
	if bin == "" {
		var err error
		if bin, err = findChromium(); err != nil {
			return err
		}
	}
	dir := e.path("browser")
	os.RemoveAll(dir)
	ext, profile := filepath.Join(dir, "extension"), filepath.Join(dir, "profile")
	if out, err := exec.CommandContext(ctx, e.path("bin", "jarvis-agent"), "browser", "pack", "--target", browser.TargetChromium,
		"--out", ext).CombinedOutput(); err != nil {
		return fmt.Errorf("jarvis-agent browser pack: %w: %s", err, out)
	}
	manifest, err := browser.HostManifest(browser.TargetChromium, e.path("bin", "jarvis-agent"))
	if err != nil {
		return err
	}
	hosts := filepath.Join(profile, "NativeMessagingHosts")
	if err := os.MkdirAll(hosts, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hosts, browser.HostName+".json"), manifest, 0o644); err != nil {
		return err
	}
	// --remote-debugging-pipe: Chromium reads DevTools commands on fd 3 and answers on fd 4.
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		return err
	}
	resR, resW, err := os.Pipe()
	if err != nil {
		return err
	}
	args := []string{"--user-data-dir=" + profile, "--load-extension=" + ext, "--disable-extensions-except=" + ext,
		"--disable-features=DisableLoadExtensionCommandLineSwitch", "--no-first-run", "--no-default-browser-check",
		"--password-store=basic", "--disable-background-networking", "--remote-debugging-pipe", "--window-size=1280,800"}
	if d.kind == "wlroots" {
		args = append(args, "--ozone-platform=wayland")
	}
	cmd := exec.Command(bin, append(args, e.Sites+"/")...)
	cmd.Env = append(d.env, browser.SocketEnv+"="+filepath.Join(d.rt, "browser.sock"))
	cmd.ExtraFiles = []*os.File{cmdR, resW}
	d.browser, err = e.start("browser", cmd)
	cmdR.Close()
	resW.Close()
	if err != nil {
		cmdW.Close()
		resR.Close()
		return err
	}
	d.cdp = newCDP(cmdW, resR)
	vctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_, err = d.cdp.Call(vctx, "Browser.getVersion", nil)
	cancel()
	if err != nil {
		return fmt.Errorf("chromium's DevTools pipe: %w (logs/browser.log)", err)
	}
	// The extension connects through the native host to the agent's browser socket.
	sock := filepath.Join(d.rt, "browser.sock")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if st, err := browser.QueryStatus(sock); err == nil && len(st.Browsers) > 0 {
			e.logf("browser up, extension %s connected", st.Browsers[0].ExtensionVersion)
			return nil
		}
		if !d.browser.alive() || time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("the extension never connected to the agent (logs/browser.log, logs/agent.log)")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Login asks the sites for a session per site and puts the cookies in the browser, standing in
// for saved logins. Called after every reset (a reset forgets the sessions).
func (e *Env) Login(ctx context.Context, sites []string) error {
	var cookies []map[string]any
	for _, s := range sites {
		var r struct {
			Cookie struct{ Name, Value, Path string } `json:"cookie"`
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", e.Sites+"/control/login?site="+s, nil)
		if err := doJSON(req, &r); err != nil {
			return err
		}
		cookies = append(cookies, map[string]any{"name": r.Cookie.Name, "value": r.Cookie.Value,
			"url": e.Sites + "/", "path": firstNonEmpty(r.Cookie.Path, "/")})
	}
	if len(cookies) == 0 {
		return nil
	}
	if e.desk == nil || e.desk.cdp == nil {
		return errors.New("no browser to put the session cookies in")
	}
	if _, err := e.desk.cdp.Call(ctx, "Storage.setCookies", map[string]any{"cookies": cookies}); err != nil {
		return err
	}
	// Read them back: a cookie the browser refused would only show later as a login wall.
	raw, err := e.desk.cdp.Call(ctx, "Storage.getCookies", nil)
	if err != nil {
		return err
	}
	var got struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return err
	}
	for _, c := range cookies {
		found := false
		for _, g := range got.Cookies {
			found = found || (g.Name == c["name"] && g.Value == c["value"])
		}
		if !found {
			return fmt.Errorf("the browser did not keep the %s cookie", c["name"])
		}
	}
	return nil
}

// StartNotes (re)starts the notes app in the session with an empty state file.
func (e *Env) StartNotes(ctx context.Context, layout string) error {
	d := e.desk
	if d == nil {
		return errors.New("no desktop session")
	}
	d.notes.stop()
	d.notesSt = e.path("notes.json")
	os.Remove(d.notesSt)
	app := filepath.Join(e.Root, "tools", "ccsites", "notesapp", "notes.py")
	// The system's Python: the session's HOME is not the user's, so a version manager's shim
	// (mise, pyenv) on PATH would not know which Python to run.
	py := "/usr/bin/python3"
	if _, err := os.Stat(py); err != nil {
		py = "python3"
	}
	argv := []string{py, app, "--state", d.notesSt, "--layout", firstNonEmpty(layout, "default")}
	var cmd *exec.Cmd
	if d.kind == "wlroots" {
		// Tk is X11: under sway it runs through Xwayland, whose DISPLAY only sway's children get.
		_, swaymsg, err := findSway()
		if err != nil {
			return err
		}
		// A script file: sway expands $ in an exec line.
		pidf, script := filepath.Join(d.rt, "notes.pid"), filepath.Join(d.rt, "notes.sh")
		os.Remove(pidf)
		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		body := fmt.Sprintf("echo $$ >'%s'\nexec %s >>'%s' 2>&1\n", pidf, strings.Join(quoted, " "), e.path("logs", "notes.log"))
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			return err
		}
		sm := exec.CommandContext(ctx, swaymsg, "exec", "sh "+script)
		sm.Env = d.env
		if out, err := sm.CombinedOutput(); err != nil {
			return fmt.Errorf("swaymsg exec: %w: %s", err, out)
		}
		// Wait for its pid and keep a handle that kills it with the session.
		cmd = exec.Command("sh", "-c", `while [ ! -s "$0" ]; do sleep 0.1; done; pid=$(cat "$0"); trap 'kill $pid' TERM; while kill -0 $pid 2>/dev/null; do sleep 0.5; done`, pidf)
	} else {
		cmd = exec.Command(argv[0], argv[1:]...)
		cmd.Env = d.env
	}
	var err error
	if d.notes, err = e.start("notes", cmd); err != nil {
		return err
	}
	// Wait for its window (sway lists it; on X11, still running after a moment will do).
	deadline, since := time.Now().Add(20*time.Second), time.Now()
	for {
		if !d.notes.alive() {
			return fmt.Errorf("the notes app exited: %s (logs/notes.log)", lastLine(e.path("logs", "notes.log")))
		}
		if d.kind == "wlroots" {
			_, swaymsg, _ := findSway()
			cmd := exec.CommandContext(ctx, swaymsg, "-t", "get_tree")
			cmd.Env = d.env
			if out, err := cmd.Output(); err == nil && strings.Contains(string(out), `"Jarvis Test Notes"`) {
				return nil
			}
		} else if time.Since(since) > 3*time.Second {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("the notes window never appeared (under sway it needs Xwayland: a sway built with it, e.g. sudo pacman -S sway xorg-xwayland; logs/notes.log)")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// lastLine is a log's last non-empty line, for an error message.
func lastLine(path string) string {
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Screenshot saves the session's screen to path (sway: grim; X11: ImageMagick's import), best
// effort.
func (e *Env) Screenshot(ctx context.Context, path string) error {
	d := e.desk
	if d == nil {
		return errors.New("no desktop session")
	}
	var cmd *exec.Cmd
	if d.kind == "wlroots" {
		cmd = exec.CommandContext(ctx, "grim", "-t", "png", path)
	} else {
		cmd = exec.CommandContext(ctx, "import", "-window", "root", path)
	}
	cmd.Env = d.env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", cmd.Args[0], err, out)
	}
	return nil
}
