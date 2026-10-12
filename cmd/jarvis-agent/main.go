// jarvis-agent is the Jarvis desktop node: it enrolls with jarvisd like a Pi node, connects to
// jarvisd's MQTT, and offers OS control (open, volume, windows) as client tools.
//
//	jarvis-agent enroll --server http://192.0.2.10:7700 --email you@example.com
//	jarvis-agent run
//	jarvis-agent status
//	jarvis-agent try desktop_set_volume '{"volume": 30}'
//	jarvis-agent run --voice        # plus the "hey jarvis" voice pipeline (Linux, macOS)
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/clock"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

var version = "dev"

const installMode = "desktop-agent"

func main() {
	// A browser starting us as its native-messaging host passes its own argv (the manifest's
	// "path" can't carry a subcommand).
	if origin, ok := browser.HostInvocation(os.Args[1:]); ok {
		os.Exit(browserHost(origin))
	}
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch os.Args[1] {
	case "enroll":
		err = enroll(ctx, os.Args[2:])
	case "run":
		err = run(ctx, os.Args[2:])
	case "status":
		err = status(ctx, os.Args[2:])
	case "try":
		err = try(ctx, os.Args[2:])
	case "listen":
		err = listen(ctx, os.Args[2:])
	case "apps":
		err = appsCmd(os.Args[2:])
	case "browser":
		err = browserCmd(ctx, os.Args[2:])
	case "hat":
		err = hatCmd(ctx, os.Args[2:])
	case "island-demo":
		err = islandDemo(ctx, os.Args[2:])
	case "browser-host":
		os.Exit(browserHost(strings.Join(os.Args[2:], " ")))
	case "version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		usage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "jarvis-agent:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: jarvis-agent <command> [flags]

  enroll   register this computer as a node of a jarvisd household
           --server URL (jarvisd config service, e.g. http://192.0.2.10:7700)
           then either --email ADDR [--household ID] (prompts for the password)
           or --token prov_... --node-id ID (a token minted elsewhere)
           [--name NAME] [--room ROOM] [--creds PATH] [--force | --if-needed]
           [--browser | --no-browser]   set up browser control afterwards (default: ask)
  run      connect to jarvisd and serve tool calls until interrupted
           [--creds PATH] [--allow-open-any] [--no-browser]
           [--sidecar-python PY --sidecar-commands DIR]
           [--voice [--voice-input DEV] [--voice-output DEV] [--wake-threshold 0.4]
            [--wake-models DIR] [--no-chimes]]   wake word "hey jarvis" (Linux, macOS)
  listen   the voice pipeline alone (no MQTT), same voice and tool flags
           [--once] [--timeout DUR] [--no-tools]   e.g. --voice-input wav:hey_jarvis.wav --once
  status   show enrollment, reachability and what this host can do right now
  apps     list launchable apps, or show what a name resolves to (never launches): apps [NAME]
  try      run one tool locally, no jarvisd involved: try [--wait 10s] TOOL ['{"arg": ...}']
           (browser_* tools wait for the browser extension to connect)
  browser  browser control (Firefox, Chrome, Edge, Brave, Arc):
           install [--all] [--no-open]   register the native host, set up the extension
           uninstall | status | pack --target firefox|chromium --out DIR
  version
`)
}

func credsPath(p string) (string, error) {
	if p != "" {
		return p, nil
	}
	return node.DefaultCredsPath()
}

// logger logs at info, or at debug with JARVIS_AGENT_LOG=debug.
func logger() *slog.Logger {
	lvl := slog.LevelInfo
	if strings.EqualFold(os.Getenv("JARVIS_AGENT_LOG"), "debug") {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// appCatalogue is the installed-apps lister: platform defaults plus apps.json, re-read on every
// call so installs and config edits apply without a restart.
func appCatalogue(log *slog.Logger) desktop.Lister {
	return func() ([]apps.App, error) {
		home, _ := os.UserHomeDir()
		var cfg apps.Config
		if p, err := apps.ConfigPath(); err == nil {
			if cfg, err = apps.LoadConfig(p); err != nil {
				// A broken config must not silently widen or empty the policy: refuse.
				return nil, fmt.Errorf("apps config: %w", err)
			}
		}
		return apps.Build(apps.Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv}, cfg).List()
	}
}

func nativeHost() (*host.Host, error) {
	b, err := host.Native()
	if err != nil {
		return nil, err
	}
	return host.New(b), nil
}

// --- enroll ---

func enroll(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := fs.String("server", "", "jarvisd config service URL, e.g. http://192.0.2.10:7700")
	email := fs.String("email", "", "your Jarvis account email (prompts for the password)")
	pwStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of prompting")
	household := fs.String("household", "", "household id (needed only if you belong to several)")
	token := fs.String("token", "", "a provisioning token minted elsewhere (with --node-id)")
	nodeID := fs.String("node-id", "", "the node id the token was minted for")
	hostname, _ := os.Hostname()
	name := fs.String("name", hostname, "node name shown in the app")
	room := fs.String("room", "desktop", "room")
	cp := fs.String("creds", "", "credentials file (default: user config dir)")
	force := fs.Bool("force", false, "replace existing credentials")
	ifNeeded := fs.Bool("if-needed", false, "do nothing (exit 0) when already enrolled")
	browserYes := fs.Bool("browser", false, "set up browser control after enrolling, without asking")
	browserNo := fs.Bool("no-browser", false, "don't offer browser control setup")
	_ = fs.Parse(args)
	path, err := credsPath(*cp)
	if err != nil {
		return err
	}
	if _, err := node.LoadCreds(path); err == nil && !*force {
		if *ifNeeded {
			fmt.Printf("Already enrolled (%s).\n", path)
			return nil
		}
		return fmt.Errorf("already enrolled (%s); pass --force to enroll again", path)
	}
	if *server == "" {
		return errors.New("--server is required (jarvisd's address, e.g. http://192.0.2.10:7700)")
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	svcs, err := node.Discover(ctx, hc, *server)
	if err != nil {
		return err
	}
	ccURL, err := svcs.Need(node.SvcCommandCenter)
	if err != nil {
		return err
	}

	tok, id := *token, *nodeID
	nodeCC := ""
	switch {
	case tok != "" && id != "":
	case tok != "" || id != "":
		return errors.New("--token and --node-id go together")
	case *email != "":
		authURL, err := svcs.Need(node.SvcAuth)
		if err != nil {
			return err
		}
		pw, err := readPassword(*pwStdin)
		if err != nil {
			return err
		}
		jwt, err := node.Login(ctx, hc, authURL, *email, pw)
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		hh := *household
		if hh == "" {
			hs, err := node.Households(ctx, hc, authURL, jwt)
			if err != nil {
				return err
			}
			switch len(hs) {
			case 0:
				return errors.New("your account belongs to no household")
			case 1:
				hh = hs[0].ID
			default:
				var b strings.Builder
				for _, h := range hs {
					fmt.Fprintf(&b, "\n  %s  %s (%s)", h.ID, h.Name, h.Role)
				}
				return fmt.Errorf("you belong to several households; pick one with --household:%s", b.String())
			}
		}
		pt, err := node.MintToken(ctx, hc, ccURL, jwt, hh, *name, *room)
		if err != nil {
			return fmt.Errorf("provisioning token: %w", err)
		}
		tok, id = pt.Token, pt.NodeID
		nodeCC = pt.NodeCommandCenterURL
	default:
		return errors.New("give --email (sign in) or --token and --node-id")
	}

	// Prefer the URL jarvisd says nodes should use; if this machine can't reach it (a VPN, a
	// loopback-only dev server), fall back to the one discovery gave us. The token is single
	// use, so only a connection failure (nothing was redeemed) falls back.
	var c node.Creds
	if nodeCC != "" && nodeCC != ccURL {
		c, err = node.Register(ctx, hc, nodeCC, *server, id, tok, *room)
		var he *node.HTTPError
		if err != nil && !errors.As(err, &he) {
			fmt.Fprintf(os.Stderr, "note: %s unreachable (%v); registering via %s\n", nodeCC, err, ccURL)
			c, err = node.Register(ctx, hc, ccURL, *server, id, tok, *room)
		}
	} else {
		c, err = node.Register(ctx, hc, ccURL, *server, id, tok, *room)
	}
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.Name = *name
	if err := node.SaveCreds(path, c); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}
	fmt.Printf("Enrolled as node %s (room %q). Credentials saved to %s.\n", c.NodeID, c.Room, path)
	offerBrowserSetup(*browserYes, *browserNo)
	fmt.Println("Next: jarvis-agent run")
	return nil
}

func readPassword(fromStdin bool) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on stdin")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// --- run ---

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cp := fs.String("creds", "", "credentials file")
	tf := addToolFlags(fs)
	voiceOn := fs.Bool("voice", false, `listen for "hey jarvis" and run voice turns`)
	noOverlay := fs.Bool("no-overlay", false, "with --voice: don't show the on-screen indicator (the island)")
	vf := addVoiceFlags(fs)
	hf := addHatFlags(fs)
	_ = fs.Parse(args)
	path, err := credsPath(*cp)
	if err != nil {
		return err
	}
	c, err := node.LoadCreds(path)
	if err != nil {
		return err
	}
	log := logger()
	h, tools, closeTools, err := tf.build(ctx, log, true)
	if err != nil {
		return err
	}
	defer closeTools()
	caps, _ := h.Capabilities(ctx)
	log.Info("jarvis-agent starting", "version", version, "creds", c, "platform", h.Platform(), "capabilities", caps)
	r := &node.Runner{Creds: c, Tools: tools, Version: version, InstallMode: installMode, Log: log}
	hd := openHat(hf, log)
	defer hd.Close()
	var vs *voiceStack
	if *voiceOn {
		// Voice is an extra: if it can't start, the agent still serves its tools.
		if vs, err = loadVoice(ctx, vf, log); err != nil {
			log.Error("voice disabled", "err", err)
			vs = nil
		} else {
			defer vs.close()
			if hd != nil {
				vs.addIndicator(&ledCue{leds: hd.leds, log: log})
			}
			r.OnConnected = func(ctx context.Context, cc *node.CC) {
				// Voice turns run the same toolset the MQTT dispatcher serves.
				_ = voice.Serve(ctx, vs.options(cc, tools, log, nil))
			}
		}
	}
	if vs == nil || *noOverlay {
		return r.Run(ctx)
	}
	// The island needs the main thread on macOS: island.Run keeps it and runs the agent on
	// other goroutines. Where it can't show one, it says why once and runs the agent as is.
	return island.Run(ctx, log, func(i *island.Island) error {
		if i != nil {
			vs.addIndicator(islandCue{i})
			hideIslandForScreenshots(tools, i)
		}
		return r.Run(ctx)
	})
}

// hideIslandForScreenshots keeps the island out of desktop_read_screen's pictures.
func hideIslandForScreenshots(tools node.Multi, i *island.Island) {
	for _, t := range tools {
		if st, ok := t.(*screen.Tools); ok {
			st.Hide = i.Suspend
		}
	}
}

// toolFlags choose the node's client tools; `run` and `listen` share them.
type toolFlags struct {
	anyOpen    *bool
	noBrowser  *bool
	py, cmdDir *string
}

func addToolFlags(fs *flag.FlagSet) *toolFlags {
	return &toolFlags{
		anyOpen:   fs.Bool("allow-open-any", false, "let desktop_open open any path, app or URI scheme (default: http, https, mailto)"),
		noBrowser: fs.Bool("no-browser", false, "don't serve the browser extension (no browser_* tools)"),
		py:        fs.String("sidecar-python", "", "python interpreter for the package sidecar (spike)"),
		cmdDir:    fs.String("sidecar-commands", "", "directory of IJarvisCommand modules for the sidecar (spike)"),
	}
}

// build is the one toolset of this node: the desktop tools, read-the-screen, the browser tools (offered while
// the extension is connected), then the sidecar's commands. The MQTT dispatcher and voice
// turns both serve it. promptAX asks macOS for Accessibility (the
// long-running `run` does; `listen`, a test command often started over SSH, doesn't).
func (f *toolFlags) build(ctx context.Context, log *slog.Logger, promptAX bool) (*host.Host, node.Multi, func(), error) {
	h, err := nativeHost()
	if err != nil {
		return nil, nil, nil, err
	}
	if promptAX && macax.Supported() && !macax.Trusted() {
		// macOS adds this app to Privacy & Security → Accessibility and shows its prompt. Window
		// tools appear once the user turns it on (capabilities are computed per call).
		macax.Prompt()
		log.Warn("Accessibility not granted: window control is off until you enable Jarvis Agent in System Settings → Privacy & Security → Accessibility")
	}
	tools := node.Multi{desktop.New(h, desktop.OpenPolicy{AllowAny: *f.anyOpen}, appCatalogue(log)), &screen.Tools{C: screen.Native(), Log: log}}
	closeAll := func() {}
	if !*f.noBrowser {
		hub, stopBrowser := startBrowser(ctx, log)
		tools = append(tools, browser.NewTools(hub))
		closeAll = stopBrowser
	}
	if *f.py != "" && *f.cmdDir != "" {
		sc, err := sidecar.Start(ctx, *f.py, *f.cmdDir, log)
		if err != nil {
			closeAll()
			return nil, nil, nil, fmt.Errorf("sidecar: %w", err)
		}
		stopBrowser := closeAll
		closeAll = func() { _ = sc.Close(); stopBrowser() }
		tools = append(tools, sc)
	}
	// Last, so a sidecar's own get_current_time (Python's, with its city table) wins when present.
	tools = append(tools, &clock.Tool{})
	return h, tools, closeAll, nil
}

// --- status ---

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cp := fs.String("creds", "", "credentials file")
	_ = fs.Parse(args)
	h, err := nativeHost()
	if err != nil {
		return err
	}
	caps, err := h.Capabilities(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("platform:     %s\ncapabilities: %s\n", h.Platform(), strings.Join(caps, ", "))
	if macax.Supported() {
		trusted := macax.Trusted()
		fmt.Printf("accessibility: %v\n", trusted)
		if !trusted {
			fmt.Print(`  -> Window control is OFF: this process has no Accessibility grant.
     Turn on "Jarvis Agent" in System Settings > Privacy & Security > Accessibility.
     If it already looks on, it's an old entry from a differently signed build. Run
       tccutil reset Accessibility io.jarvis.agent && tccutil reset AppleEvents io.jarvis.agent
     then restart the agent and allow it again. (Run from a terminal, this check reports the
     terminal's grant; use scripts/agent-dev-run.sh --status for the agent's own.)
`)
		}
	}
	if macax.Supported() {
		screen := macax.ScreenTrusted()
		fmt.Printf("screen recording: %v\n", screen)
		if !screen {
			// Puts Jarvis Agent in the list and shows macOS's prompt (read-the-screen needs it).
			macax.ScreenPrompt()
			fmt.Print("  -> Reading the screen is OFF: allow \"Jarvis Agent\" in System Settings > Privacy & Security >\n" +
				"     Screen & System Audio Recording, then restart the agent.\n")
		}
	}
	fmt.Printf("voice:        %s\n", voiceStatus())
	fmt.Printf("browser:      %s\n", browserSummary())
	if list, err := appCatalogue(nil)(); err != nil {
		fmt.Printf("apps:         %v\n", err)
	} else {
		cp, _ := apps.ConfigPath()
		fmt.Printf("apps:         %d launchable (config: %s)\n", len(list), cp)
	}
	path, err := credsPath(*cp)
	if err != nil {
		return err
	}
	c, err := node.LoadCreds(path)
	if err != nil {
		fmt.Printf("enrollment:   %v\n", err)
		return nil
	}
	fmt.Printf("node:         %s (room %q)\nserver:       %s\ncredentials:  %s\n", c.NodeID, c.Room, c.ConfigURL, path)
	hc := &http.Client{Timeout: 5 * time.Second}
	svcs, err := node.Discover(ctx, hc, c.ConfigURL)
	if err != nil {
		fmt.Printf("jarvisd:      unreachable (%v)\n", err)
		return nil
	}
	ccURL, err := svcs.Need(node.SvcCommandCenter)
	if err != nil {
		return err
	}
	if err := node.NewCC(ccURL, c).Heartbeat(ctx, version, installMode); err != nil {
		fmt.Printf("jarvisd:      reachable, but the node key was refused or failed (%v)\n", err)
		return nil
	}
	fmt.Printf("jarvisd:      ok (%s, broker %s)\n", ccURL, svcs[node.SvcMQTT])
	return nil
}

// --- try ---

func try(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("try", flag.ExitOnError)
	anyOpen := fs.Bool("allow-open-any", false, "relax the open policy")
	wait := fs.Duration("wait", 10*time.Second, "how long browser_* tools wait for the browser extension to connect")
	saveImages := fs.String("save-images", "", "write a result's attached images (desktop_read_screen) into this directory instead of printing their data")
	_ = fs.Parse(args)
	h, err := nativeHost()
	if err != nil {
		return err
	}
	ts := node.Multi{desktop.New(h, desktop.OpenPolicy{AllowAny: *anyOpen}, appCatalogue(nil)), &screen.Tools{C: screen.Native()}, &clock.Tool{}}
	if fs.NArg() == 0 || strings.HasPrefix(fs.Arg(0), "browser_") {
		// Serve the extension for this one call (fails over to "not connected" when a running
		// agent holds the socket: stop it, or use that agent).
		hub, stopBrowser := startBrowser(ctx, logger())
		defer stopBrowser()
		ts = append(ts, browser.NewTools(hub))
		w := *wait
		if fs.NArg() == 0 {
			w = min(w, 3*time.Second)
		}
		wctx, cancel := context.WithTimeout(ctx, w)
		if hub.WaitConnected(wctx) {
			// Each browser's host reconnects on its own 2 s retry: give the others a moment so
			// one-shot calls see (and target) the same browsers a running agent would.
			select {
			case <-wctx.Done():
			case <-time.After(2500 * time.Millisecond):
			}
		}
		cancel()
	}
	if fs.NArg() == 0 {
		for _, d := range ts.Available(ctx) {
			fmt.Println(d.Name)
		}
		return nil
	}
	a := map[string]any{}
	if fs.NArg() > 1 {
		if err := json.Unmarshal([]byte(fs.Arg(1)), &a); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	out, ok := ts.Call(ctx, fs.Arg(0), a, node.Request{})
	if !ok {
		return fmt.Errorf("unknown tool %q", fs.Arg(0))
	}
	if err := elideAttachments(out, *saveImages); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// elideAttachments replaces attached images' base64 with their size for printing, writing them
// (0600) into dir first when one is given.
func elideAttachments(out map[string]any, dir string) error {
	atts, _ := out[screen.AttachmentsKey].([]any)
	for i, a := range atts {
		m, _ := a.(map[string]any)
		data, _ := m["data"].(string)
		if data == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return err
		}
		m["data"] = fmt.Sprintf("<%d bytes>", len(raw))
		if dir != "" {
			p := filepath.Join(dir, fmt.Sprintf("attachment-%d.jpg", i+1))
			if err := os.WriteFile(p, raw, 0o600); err != nil {
				return err
			}
			m["saved_to"] = p
		}
	}
	return nil
}

// --- apps ---

func appsCmd(args []string) error {
	list, err := appCatalogue(nil)()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		for _, a := range list {
			fmt.Printf("%-40s %s\n", a.Name, a.Path)
		}
		return nil
	}
	a, err := apps.Resolve(list, strings.Join(args, " "))
	if err != nil {
		return err
	}
	fmt.Printf("%s -> %s\n", a.Name, a.Path)
	return nil
}
