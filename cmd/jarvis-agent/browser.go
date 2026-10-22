package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
)

// --- native-messaging host mode (started by the browser) ---

// browserHost relays between the extension (stdin/stdout) and the running agent. stdout
// carries native-messaging frames only; logs go to stderr, which the browser keeps.
func browserHost(origin string) int {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	path, err := browser.SocketPath()
	if err != nil {
		log.Error("jarvis-agent browser-host", "err", err)
		return 1
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	err = browser.RunHost(ctx, os.Stdin, os.Stdout, browser.HostConfig{
		Origin: origin,
		Dial:   func() (net.Conn, error) { return browser.Dial(path) },
		Log:    log,
	})
	if err != nil {
		log.Error("jarvis-agent browser-host", "err", err)
		return 1
	}
	return 0
}

// --- the agent's side ---

// startBrowser serves native hosts on the agent's socket. If another agent already serves it
// (a `run` while `listen` starts), or it can't be opened, browser tools simply stay unoffered.
func startBrowser(ctx context.Context, log *slog.Logger) (*browser.Hub, func()) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	hub := browser.NewHub(log)
	path, err := browser.SocketPath()
	if err == nil {
		var ln net.Listener
		if ln, err = browser.Listen(path); err == nil {
			ctx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if err := hub.Serve(ctx, ln); err != nil {
					log.Warn("browser control stopped", "err", err)
				}
			}()
			log.Debug("browser control listening", "socket", path)
			return hub, func() { cancel(); <-done }
		}
	}
	if errors.Is(err, browser.ErrInUse) {
		log.Warn("browser control is served by another jarvis-agent on this computer; this one offers no browser tools")
	} else {
		log.Warn("browser control off", "err", err)
	}
	return hub, func() {}
}

// refreshBrowserHosts runs when the agent starts: an app started from its bundle repoints the
// browsers already registered at itself, so a moved or reinstalled app (say from ~/Downloads
// to /Applications) doesn't leave browsers starting the old copy. Browsers the user didn't
// register stay unregistered. A bare binary (a dev build, `go run`, a Linux or Windows
// install) changes nothing: its registrations are `browser install`'s and the installer's.
// The bundle is found by slashes, like Translocated, so the check reads the same on every OS.
func refreshBrowserHosts(env browser.Env, exe string, log *slog.Logger) {
	if install.BundleOf(filepath.ToSlash(exe)) == "" {
		return
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	for _, r := range browser.Refresh(env, exe, browser.NativeRegistry()) {
		switch {
		case errors.Is(r.Err, browser.ErrTranslocated):
			log.Warn("browser control: not repointing the browsers' native host", "err", r.Err)
		case r.Err != nil:
			log.Warn("browser control: repointing the native host", "browser", r.Target.Label, "err", r.Err)
		default:
			log.Info("browser control: native host now starts this app", "browser", r.Target.Label, "exe", exe)
		}
	}
}

// --- jarvis-agent browser install|uninstall|status|pack ---

func browserEnv() (browser.Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return browser.Env{}, err
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		return browser.Env{}, err
	}
	exe, _ := agentExe()
	return browser.Env{GOOS: runtime.GOOS, Home: home, DataDir: filepath.Join(cfg, "jarvis-agent"), Getenv: os.Getenv, Exe: exe}, nil
}

func browserCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: jarvis-agent browser install|uninstall|status|pack")
	}
	switch args[0] {
	case "install":
		return browserInstall(args[1:], os.Stdout)
	case "uninstall":
		return browserUninstall(os.Stdout)
	case "status":
		return browserStatus(os.Stdout)
	case "pack":
		fs := flag.NewFlagSet("browser pack", flag.ExitOnError)
		target := fs.String("target", browser.TargetFirefox, "firefox or chromium")
		out := fs.String("out", "", "directory to write the extension into")
		_ = fs.Parse(args[1:])
		if err := noStrayArgs(fs); err != nil {
			return err
		}
		if *out == "" {
			return errors.New("--out DIR is required")
		}
		if err := browser.WriteExtension(*out, *target); err != nil {
			return err
		}
		fmt.Printf("wrote the %s extension %s to %s\n", *target, browser.ExtensionVersion(), *out)
		return nil
	case "host":
		origin := ""
		if len(args) > 1 {
			origin = args[1]
		}
		os.Exit(browserHost(origin))
	}
	return fmt.Errorf("unknown browser command %q (install, uninstall, status, pack)", args[0])
}

func agentExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Abs(exe)
}

func browserInstall(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("browser install", flag.ExitOnError)
	all := fs.Bool("all", false, "register with every supported browser, not just the ones found")
	noOpen := fs.Bool("no-open", false, "don't open the Firefox extension in Firefox (just print where it is)")
	exeFlag := fs.String("exe", "", "the jarvis-agent binary browsers should start (default: this one)")
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	env, err := browserEnv()
	if err != nil {
		return err
	}
	exe := *exeFlag
	if exe == "" {
		if exe, err = agentExe(); err != nil {
			return err
		}
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return fmt.Errorf("%s is a temporary `go run` binary; build jarvis-agent first", exe)
	}
	if browser.Translocated(exe) {
		return fmt.Errorf("%s: %w", exe, browser.ErrTranslocated)
	}
	results := browser.Register(env, exe, *all, browser.NativeRegistry())
	fmt.Fprintf(w, "Registering the native-messaging host (%s -> %s):\n", browser.HostName, exe)
	var firefox, chromium, failed bool
	for _, r := range results {
		switch {
		case r.Err != nil:
			failed = true
			fmt.Fprintf(w, "  %-26s FAILED: %v\n", r.Target.Label, r.Err)
		case r.Done:
			fmt.Fprintf(w, "  %-26s registered (%s)\n", r.Target.Label, r.Path)
			if r.Target.Family == browser.TargetFirefox {
				firefox = true
			} else {
				chromium = true
			}
		default:
			fmt.Fprintf(w, "  %-26s not found, skipped\n", r.Target.Label)
		}
	}
	dir := filepath.Join(env.DataDir, "browser")
	if chromium {
		cdir := filepath.Join(dir, "extension-chromium")
		if err := browser.WriteExtension(cdir, browser.TargetChromium); err != nil {
			return err
		}
		fmt.Fprintf(w, "\n%s\n", browser.ChromiumSteps(cdir))
	}
	if firefox {
		fmt.Fprintln(w)
		if xpi, ok := browser.SignedXPI(); ok {
			p := filepath.Join(dir, browser.SignedXPIName)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, xpi, 0o644); err != nil {
				return err
			}
			if *noOpen {
				fmt.Fprintf(w, "Firefox: open %s in Firefox (or drag it onto a Firefox window) and click Add.\n", p)
			} else if err := openInFirefox(p); err != nil {
				fmt.Fprintf(w, "Firefox: couldn't start Firefox (%v). Open %s in Firefox and click Add.\n", err, p)
			} else {
				fmt.Fprintln(w, "Firefox: asking Firefox to install \"Jarvis Browser Control\": click Add in the prompt it shows.")
			}
		} else {
			fdir := filepath.Join(dir, "extension-firefox")
			if err := browser.WriteExtension(fdir, browser.TargetFirefox); err != nil {
				return err
			}
			fmt.Fprintf(w, "Firefox: this build has no Mozilla-signed extension yet, and release Firefox installs only signed ones.\n"+
				"  To try it now (until Firefox restarts): about:debugging#/runtime/this-firefox -> \"Load Temporary Add-on...\" -> %s\n",
				filepath.Join(fdir, "manifest.json"))
		}
	}
	if firefox || chromium {
		st, err := queryLive()
		fmt.Fprint(w, installAccessNote(firefox, chromium, st, err))
	}
	if !firefox && !chromium {
		fmt.Fprintln(w, "\nNo supported browser found for this user. Install Firefox or a Chromium browser, or pass --all.")
	} else {
		fmt.Fprintln(w, "\nBrowser tools appear once the extension connects to a running agent (jarvis-agent run).\nCheck with: jarvis-agent browser status")
	}
	if failed {
		return errors.New("some registrations failed")
	}
	return nil
}

// openInFirefox hands the signed package to Firefox, which shows its own install prompt.
func openInFirefox(xpi string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-a", "Firefox", xpi)
	case "windows":
		exe := ""
		for _, d := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
			if p := filepath.Join(d, "Mozilla Firefox", "firefox.exe"); d != "" && fileExists(p) {
				exe = p
				break
			}
		}
		if exe == "" {
			return errors.New("firefox.exe not found")
		}
		cmd = exec.Command(exe, xpi)
	default:
		exe, err := exec.LookPath("firefox")
		if err != nil {
			return err
		}
		cmd = exec.Command(exe, xpi)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	time.Sleep(500 * time.Millisecond) // let a short-lived launcher hand over before we exit
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func browserUninstall(w io.Writer) error {
	env, err := browserEnv()
	if err != nil {
		return err
	}
	var failed bool
	for _, r := range browser.Unregister(env, browser.NativeRegistry()) {
		switch {
		case r.Err != nil:
			failed = true
			fmt.Fprintf(w, "  %-26s FAILED: %v\n", r.Target.Label, r.Err)
		case r.Done:
			fmt.Fprintf(w, "  %-26s unregistered\n", r.Target.Label)
		}
	}
	if err := os.RemoveAll(filepath.Join(env.DataDir, "browser")); err != nil {
		return err
	}
	fmt.Fprintln(w, "Browsers can no longer reach the agent. Remove the extension itself in each browser:\n"+
		"  Firefox: about:addons -> Jarvis Browser Control -> Remove\n"+
		"  Chrome/Edge/Brave/Arc: the extensions page -> Jarvis Browser Control -> Remove")
	if failed {
		return errors.New("some registrations could not be removed")
	}
	return nil
}

func browserStatus(w io.Writer) error {
	env, err := browserEnv()
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "Registrations:")
	for _, s := range browser.Inspect(env, browser.NativeRegistry()) {
		var line string
		switch {
		case s.Registered && s.Problem != "":
			line = "registered, BROKEN: " + s.Problem
		case s.Registered && s.Warning != "":
			line = "registered, WARNING: " + s.Warning
		case s.Registered:
			line = "registered -> " + s.HostPath
		case s.Present:
			line = "found, not registered (run: jarvis-agent browser install)"
		default:
			continue
		}
		fmt.Fprintf(w, "  %-26s %s\n", s.Target.Label, line)
	}
	if _, ok := browser.SignedXPI(); ok {
		fmt.Fprintf(w, "Extension %s: signed Firefox package included\n", browser.ExtensionVersion())
	} else {
		fmt.Fprintf(w, "Extension %s: no signed Firefox package in this build (temporary install only)\n", browser.ExtensionVersion())
	}
	fmt.Fprintln(w, browserLive())
	return nil
}

// queryLive asks the running agent which browsers are connected.
func queryLive() (browser.StatusReply, error) {
	path, err := browser.SocketPath()
	if err != nil {
		return browser.StatusReply{}, err
	}
	return browser.QueryStatus(path)
}

// browserLive is a line about the running agent's connected browsers, and how to turn website
// access on for each one that has it off.
func browserLive() string {
	path, err := browser.SocketPath()
	if err != nil {
		return "Agent: " + err.Error()
	}
	st, err := browser.QueryStatus(path)
	if err != nil {
		return "Agent: not running (or not reachable at " + path + ")"
	}
	return liveText(st)
}

func liveText(st browser.StatusReply) string {
	if len(st.Browsers) == 0 {
		return "Agent: running; no browser connected (is the extension installed and enabled?)"
	}
	var parts []string
	for _, b := range st.Browsers {
		s := fmt.Sprintf("%s %s (extension %s)", b.Browser, b.Version, b.ExtensionVersion)
		if b.Focused {
			s += ", focused"
		}
		if b.WebsiteAccess != nil {
			s += ", website access " + map[bool]string{true: "on", false: "off"}[*b.WebsiteAccess]
		}
		parts = append(parts, s)
	}
	return "Agent: running; connected: " + strings.Join(parts, "; ") + accessOffSteps(st)
}

// accessOffSteps is a line per connected browser whose extension has website access off.
func accessOffSteps(st browser.StatusReply) string {
	var out string
	for _, b := range st.Browsers {
		if b.WebsiteAccess != nil && !*b.WebsiteAccess {
			out += fmt.Sprintf("\n  %s: website access is off, so Jarvis can't read or act on pages. To turn it on: %s",
				b.Browser, browser.WebsiteAccessSteps(b.Browser))
		}
	}
	return out
}

// installAccessNote ends browser install: how to turn website access on in each connected
// browser that has it off, and, while no Firefox has connected to say, the Firefox step (Firefox
// lets the user leave it off at install). Empty when there's nothing to do.
func installAccessNote(firefox, chromium bool, st browser.StatusReply, err error) string {
	if err != nil {
		st = browser.StatusReply{}
	}
	out := accessOffSteps(st)
	if firefox {
		reported := false
		for _, b := range st.Browsers {
			if b.WebsiteAccess != nil && strings.Contains(strings.ToLower(b.Browser), "firefox") {
				reported = true
			}
		}
		if !reported {
			out += "\nFirefox: Jarvis needs website access to read and act on pages. If the toolbar button says " +
				"\"Website access: off\": " + browser.WebsiteAccessSteps("Firefox")
		}
	}
	if out == "" {
		return ""
	}
	return "\n" + strings.TrimPrefix(out, "\n") + "\n"
}

// offerBrowserSetup is enroll's last step: set up browser control if asked to, or ask.
func offerBrowserSetup(yes, no bool) {
	if no {
		return
	}
	if !yes {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Println("Browser control (optional): jarvis-agent browser install")
			return
		}
		fmt.Print("\nSet up browser control, so Jarvis can use your Firefox / Chrome tabs (list, open, read, pause video)? [Y/n] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if !browserAnswerYes(line, err) {
			fmt.Println("Skipped. Later: jarvis-agent browser install")
			return
		}
	}
	if err := browserInstall(nil, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "browser setup:", err)
	}
}

// browserAnswerYes reads the setup prompt's answer: Enter means yes (the default), but input
// that ends without an answer is no, so nothing installs unless someone was asked.
func browserAnswerYes(line string, err error) bool {
	a := strings.ToLower(strings.TrimSpace(line))
	if a == "" {
		return err == nil
	}
	return a == "y" || a == "yes"
}

// browserSummary is `status`'s one line: where the host is registered, and what's connected.
func browserSummary() string {
	env, err := browserEnv()
	if err != nil {
		return err.Error()
	}
	var reg []string
	for _, s := range browser.Inspect(env, browser.NativeRegistry()) {
		switch {
		case s.Registered && s.Problem != "":
			reg = append(reg, s.Target.Label+" (broken)")
		case s.Registered && s.Warning != "":
			reg = append(reg, s.Target.Label+" (another copy: "+s.HostPath+")")
		case s.Registered:
			reg = append(reg, s.Target.Label)
		}
	}
	r := "not set up (jarvis-agent browser install)"
	if len(reg) > 0 {
		r = "registered for " + strings.Join(reg, ", ")
	}
	return r + "; " + strings.TrimPrefix(browserLive(), "Agent: ")
}
