// jarvis-agent is the Jarvis desktop node: it enrolls with jarvisd like a Pi node, connects to
// jarvisd's MQTT, and offers OS control (open, volume, windows) as client tools.
//
//	jarvis-agent enroll --server http://192.0.2.10:7700   # shows a code to approve
//	jarvis-agent enroll --server http://192.0.2.10:7700 --email you@example.com
//	jarvis-agent run
//	jarvis-agent status
//	jarvis-agent try desktop_set_volume '{"volume": 30}'
//	jarvis-agent stop               # until the next login (the status icon's Quit)
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
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/clock"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/jarvispage"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

var version = "dev"

const installMode = "desktop-agent"

// nodeKind tells jarvisd this is a computer, not a Pi (DESIGN §9 Q1; reported in heartbeats).
const nodeKind = "desktop"

// Values of the heartbeat's browser_extension capability.
const (
	extConnected    = "connected"    // a browser's extension is talking to this agent
	extDisconnected = "disconnected" // browser control is on, no extension connected
	extDisabled     = "disabled"     // run --no-browser
)

// heartbeatCaps reports the browser extension's state, and wakes a heartbeat when it changes.
func heartbeatCaps(tools node.Multi) (caps func() map[string]any, changed func() <-chan struct{}) {
	var hub *browser.Hub
	for _, t := range tools {
		if bt, ok := t.(*browser.Tools); ok {
			hub = bt.Hub
		}
	}
	if hub == nil {
		return func() map[string]any { return map[string]any{"browser_extension": extDisabled} }, nil
	}
	return func() map[string]any {
		state := extDisconnected
		if hub.Connected() {
			state = extConnected
		}
		return map[string]any{"browser_extension": state}
	}, hub.Changed
}

func main() {
	// A browser starting us as its native-messaging host passes its own argv (the manifest's
	// "path" can't carry a subcommand).
	if origin, ok := browser.HostInvocation(os.Args[1:]); ok {
		os.Exit(browserHost(origin))
	}
	if openedAsApp() {
		// Opening "Jarvis Automation Agent.app" (Finder, Dock, Launchpad) runs it with no
		// command: that is the start-only shortcut.
		os.Args = []string{os.Args[0], "start"}
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
		redirectLaunchdOutput()
		err = run(ctx, os.Args[2:])
	case "status":
		err = status(ctx, os.Args[2:])
	case "install":
		err = installCmd(ctx, os.Args[2:], os.Stdout)
	case "uninstall":
		err = uninstallCmd(ctx, os.Args[2:], os.Stdout)
	case "start":
		err = startCmd(ctx, os.Args[2:])
	case "stop":
		err = stopCmd(ctx, os.Args[2:], os.Stdout)
	case "setup":
		err = setupCmd(ctx, os.Args[2:])
	case "permission":
		err = permissionCmd(os.Args[2:], os.Stdout)
	case "restart":
		err = restartCmd(ctx, os.Args[2:], os.Stdout)
	case "update":
		err = updateCmd(ctx, os.Args[2:], os.Stdout)
	case "config":
		err = configCmd(os.Args[2:], os.Stdout)
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
	case "dictation":
		err = dictationCmd(ctx, os.Args[2:])
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
		os.Exit(exitCode(err))
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: jarvis-agent <command> [flags]

  enroll   register this computer as a node of a jarvisd household
           --server ADDRESS (the server: 192.0.2.10, host:7700, or any of its URLs, the web app's too)
           then either nothing: shows a code for a household member to approve (or --code)
           or --email ADDR [--household ID] (prompts for the password)
           or --token prov_... --node-id ID (a token minted elsewhere)
           [--name NAME] [--room ROOM] [--creds PATH] [--force | --if-needed]
           [--browser | --no-browser]   set up browser control afterwards (default: ask)
  run      connect to jarvisd and serve tool calls until interrupted
           (settings come from the config file, see config below; flags given here win)
           [--creds PATH] [--config PATH] [--allow-open-any] [--no-browser]
           [--confirm-dictation off|submit|all]   ask before typing/clicking sends anything
           [--sidecar-python PY --sidecar-commands DIR]
           [--no-overlay] [--no-tray]   no on-screen island / no status icon
           [--voice-input DEV] [--voice-output DEV] [--wake-threshold 0.4] [--wake-models DIR]
           [--no-chimes]   listening for "Hey Jarvis" (Linux, macOS): on and off from the
           status icon's menu
  listen   the voice pipeline alone (no MQTT), same voice and tool flags
           [--once] [--timeout DUR] [--no-tools]   e.g. --voice-input wav:hey_jarvis.wav --once
  install  set the agent up for this user: start at login, the app menu entry (and desktop
           shortcut), the jarvis-agent:// link, browser control's native host; then start it
           [--no-start] [--no-browser] [--desktop-shortcut] [--login-item auto|smappservice|launchagent]
           [--files-only] [--dry-run]
  uninstall  undo install [--purge: also delete the sign-in, settings and caches] [--dry-run]
  start    start the agent (what the menu entry, the shortcut and jarvis-agent://start run);
           on a computer that isn't set up, it opens the setup page
  setup    the setup page in your browser: find the server, add this computer (a pairing
           code or your Jarvis Automation sign-in), browser extension, permissions, voice,
           remove (the status icon's "Set up / Settings…") [--no-open] [--no-start]
  stop     stop the running agent until the next login or start (the menu's Quit does the same)
  restart  restart the running agent (through systemd or launchd when they run it), so it
           reads the settings file again
  update   update to the version of the jarvisd this computer is signed in to (the agent also
           does it by itself: daily, and when Jarvis Automation sends an update; agent.json
           auto_update false turns that off) [--check] [--version vX] [--force: a dev build]
  config   the settings file run reads: config [show | path | set KEY VALUE | unset KEY]
  status   show enrollment, reachability and what this host can do right now
  dictation allow | revoke | status   the one-time typing permission GNOME and KDE on Wayland
           need (allow shows the desktop's dialog once; nothing else ever does)
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
	server := fs.String("server", "", "the Jarvis Automation server's address: its host (192.0.2.10), host:port, or any of its URLs, the web app's included")
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
	useCode := fs.Bool("code", false, "pair with a code a household member approves (the default without --email or --token)")
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	roomSet := false
	fs.Visit(func(f *flag.Flag) { roomSet = roomSet || f.Name == "room" })
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
		return errors.New("--server is required (the Jarvis Automation server's address, e.g. 192.0.2.10)")
	}
	hc := &http.Client{Timeout: 20 * time.Second}
	// Whatever was pasted (a bare host, the web app's URL on 7710, any path): the config service.
	typed := *server
	if *server, err = node.ResolveConfigURL(ctx, hc, typed); err != nil {
		return err
	}
	if n, _ := node.NormalizeAddress(typed); n != *server {
		// Not the address given: say where the server was found.
		fmt.Printf("Found the Jarvis Automation server at %s.\n", *server)
	}
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
	regRoom := *room
	switch {
	case *useCode && (tok != "" || id != "" || *email != ""):
		return errors.New("--code doesn't go with --email or --token")
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
		res, err := pairWithCode(ctx, hc, ccURL, *name)
		if err != nil {
			return err
		}
		tok, id, nodeCC = res.Token, res.NodeID, res.NodeCommandCenterURL
		// The approver chose the room; --room given here still wins, as with a token.
		if !roomSet {
			regRoom = ""
		}
		if res.Name != "" {
			*name = res.Name
		}
	}

	// Prefer the URL jarvisd says nodes should use; if this machine can't reach it (a VPN, a
	// loopback-only dev server), fall back to the one discovery gave us.
	c, err := node.RegisterPreferring(ctx, hc, nodeCC, ccURL, *server, id, tok, regRoom, func(err error) {
		fmt.Fprintf(os.Stderr, "note: %s unreachable (%v); registering via %s\n", nodeCC, err, ccURL)
	})
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

// pairWithCode starts a device-code pairing, shows the code and waits for a household member
// to approve it (DESIGN §13). No password is typed here.
func pairWithCode(ctx context.Context, hc *http.Client, ccURL, name string) (node.PairingResult, error) {
	p, err := node.StartPairing(ctx, hc, ccURL, name, runtime.GOOS)
	if err != nil {
		return node.PairingResult{}, fmt.Errorf("starting pairing: %w", err)
	}
	mins := max(1, (p.ExpiresIn+59)/60)
	fmt.Printf("To add this computer (%s), approve this code in Jarvis Automation\n"+
		"(the admin's Nodes page, or the app):\n\n    %s\n\nWaiting for approval (the code expires in %d minutes)...\n", name, p.Code, mins)
	res, err := node.WaitPairing(ctx, hc, ccURL, p, node.SleepCtx)
	if err != nil {
		return node.PairingResult{}, err
	}
	fmt.Printf("Approved: room %q.\n", res.Room)
	return res, nil
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

// runFlags are `run`'s flags; the config file (settings.Keys) stands for some of them.
type runFlags struct {
	cp, config         *string
	tf                 *toolFlags
	voiceOn, noOverlay *bool
	noTray             *bool
	noAutoUpdate       *bool
	vf                 *voiceFlags
	hf                 *hatFlags
}

// voiceByDefault is run's --voice default: a desktop install listens for "Hey Jarvis" out of
// the box (user, 2026-10-13). The status icon's toggle is how a user turns it off (it saves
// agent.json's `voice`, which overrides this); --voice=false is for scripts and development.
const voiceByDefault = true

func newRunFlags(errs flag.ErrorHandling) (*flag.FlagSet, *runFlags) {
	fs := flag.NewFlagSet("run", errs)
	return fs, &runFlags{
		cp:        fs.String("creds", "", "credentials file"),
		config:    fs.String("config", "", "settings file (default: <user config dir>/jarvis-agent/agent.json); flags given here win over it"),
		tf:        addToolFlags(fs),
		voiceOn:   fs.Bool("voice", voiceByDefault, `listen for "hey jarvis" and run voice turns`),
		noOverlay: fs.Bool("no-overlay", false, "don't show the on-screen indicator (the island: voice states, the \"Jarvis is running\" pill)"),
		noTray:    fs.Bool("no-tray", false, "don't show the status icon and its menu (menu bar, tray, notification area)"),
		noAutoUpdate: fs.Bool("no-auto-update", false, "don't update to the server's version by itself, and refuse updates sent from Jarvis Automation "+
			"(jarvis-agent update still works)"),
		vf: addVoiceFlags(fs),
		hf: addHatFlags(fs),
	}
}

func run(ctx context.Context, args []string) error {
	fs, f := newRunFlags(flag.ExitOnError)
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	log := logger()
	if crashForTest != "" {
		return errors.New("crashForTest: this build fails on purpose (the agent-upgrade e2e's broken release)")
	}
	if err := applySettings(fs, *f.config, log); err != nil {
		return err
	}
	cp, tf, voiceOn, noOverlay, vf, hf := f.cp, f.tf, f.voiceOn, f.noOverlay, f.vf, f.hf
	// One agent per user: the control socket is the lock, and how a second launch, the
	// start-only shortcut and uninstall reach this one (installers plan item 1.9).
	ctl, err := listenControl()
	if errors.Is(err, browser.ErrInUse) {
		fmt.Fprintln(os.Stderr, "jarvis-agent is already running for this user; not starting a second one")
		return nil
	}
	if err != nil {
		log.Warn("control socket off: a second launch can't reach this agent", "err", err)
	}
	path, err := credsPath(*cp)
	if err != nil {
		return err
	}
	c, err := node.LoadCreds(path)
	if err != nil {
		if ctl != nil {
			_ = ctl.Close()
		}
		if errors.Is(err, node.ErrNotEnrolled) && !interactive() {
			// A login item on a computer that isn't set up: the status icon's "Set up", then
			// the agent once it is (setupwait.go). Typed in a terminal, it says what to do.
			return waitForSetup(ctx, log, path, *f.noTray)
		}
		return notSetUp(err)
	}
	migrateLegacyID(log)
	ctx, quit := context.WithCancel(ctx)
	defer quit()
	pill := &runningPill{}
	var swReady atomic.Pointer[voiceSwitch]
	var runner atomic.Pointer[node.Runner]
	if ctl != nil {
		// Healthy (the self-update's check) once connected to jarvisd's broker.
		health := func() error {
			if r := runner.Load(); r != nil && r.MQTTConnected() {
				return nil
			}
			return errors.New("not connected to jarvisd yet")
		}
		defer serveControl(ctx, ctl, log, pill, quit, swReady.Load, health)()
	}
	// K2, command secrets and data live next to node.json; another node's are dropped here.
	st, err := nodesettings.OpenStore(filepath.Dir(path), c.NodeID)
	if err != nil {
		return fmt.Errorf("node settings: %w", err)
	}
	h, tools, closeTools, err := tf.build(ctx, log, true, st)
	if err != nil {
		return err
	}
	defer closeTools()
	go learnServer(ctx, c.ConfigURL, tf.origins, log)
	caps, _ := h.Capabilities(ctx)
	log.Info("jarvis-agent starting", "version", version, "creds", c, "platform", h.Platform(), "capabilities", caps)
	// Commands the household turned off in the app's node settings are neither offered nor run.
	served := nodesettings.Gate{Tools: tools, Store: st}
	r := &node.Runner{Creds: c, Tools: served, Version: version, InstallMode: installMode, NodeKind: nodeKind, Log: log}
	runner.Store(r)
	r.Capabilities, r.Changed = heartbeatCaps(tools)
	wireNodeSettings(r, st, tools, log)
	hd := openHat(hf, log)
	defer hd.Close()
	// Voice is an extra: if it can't start, the agent still serves its tools. The status icon's
	// toggle turns it on and off while the agent runs (voiceSwitch).
	sw := newVoiceSwitch(*voiceOn && voice.Supported, log)
	sw.load = func(ctx context.Context) (*voiceStack, error) { return loadVoice(ctx, vf, log) }
	sw.serve = func(ctx context.Context, vs *voiceStack, cc *node.CC) {
		// Voice turns run the same toolset the MQTT dispatcher serves.
		_ = voice.Serve(ctx, vs.options(cc, served, log, nil))
	}
	sw.persist = func(on bool) error { return saveVoice(*f.config, on) }
	sw.available = voice.Supported
	swReady.Store(sw)
	if voice.Supported {
		if hd != nil {
			sw.addIndicator(&ledCue{leds: hd.leds, log: log})
		}
		r.OnConnected = sw.Serve
	} else if *voiceOn {
		log.Info("voice: not supported on " + runtime.GOOS + " yet")
	}
	setup := openSetupPage(log)
	// Signed out (from Add-ons, an admin, or the menu's Sign out): drop node.json only, keep
	// agent.json and apps.json, and open the setup page so someone can sign in again.
	runNode := func(ctx context.Context) error { return signedOutToSetup(r.Run(ctx), path, log, setup) }
	ic := &trayIcon{off: *f.noTray, log: log, sw: sw, h: h, configURL: c.ConfigURL, quit: quit, setup: setup, signOut: r.SignOut}
	sw.changed = ic.voiceChanged
	var note atomic.Pointer[func(string)] // the island's note, once it is up
	followServer(ctx, r, !*f.noAutoUpdate, func(text string) {
		ic.setUpdate(text)
		if n := note.Load(); n != nil {
			(*n)(text)
		}
	}, log)
	if *noOverlay {
		pill.Attach(nil)
		if ic.off {
			return runNode(ctx)
		}
		// No island, but macOS's menu-bar icon still needs AppKit's loop on the main thread.
		return island.RunLoop(ctx, log, appEvents(pill, log), func(loop bool) error {
			ic.mainLoop = loop
			defer ic.show(ctx)()
			return runNode(ctx)
		})
	}
	// The island needs the main thread on macOS: island.Run keeps it and runs the agent on
	// other goroutines. Where it can't show one, it says why once and runs the agent as is.
	// It runs with voice off too, for the "Jarvis is running" pill; it shows voice states and
	// dictation's notes only while listening is on.
	return island.RunWith(ctx, log, appEvents(pill, log), func(i *island.Island) error {
		if i == nil {
			pill.Attach(nil)
		} else {
			hideFromScreenshots(tools, i)
			n := func(text string) { i.Note(text, 8*time.Second) }
			note.Store(&n)
			if voice.Supported {
				sw.addIndicator(islandCue{i})
				attachIsland(tools, i, sw.On)
			}
			pill.Attach(i.ShowRunning)
		}
		ic.mainLoop = i != nil // macOS: the island means AppKit's loop runs
		defer ic.show(ctx)()
		return runNode(ctx)
	})
}

// hideFromScreenshots keeps the island out of desktop_read_screen's pictures.
func hideFromScreenshots(tools node.Multi, i *island.Island) {
	for _, t := range tools {
		if x, ok := t.(*screen.Tools); ok {
			x.Hide = i.Suspend
		}
	}
}

// attachIsland keeps the island out of desktop_read_screen's pictures and shows on it what
// dictation types, fills, clicks or asks, while listening is on.
func attachIsland(tools node.Multi, i *island.Island, listening func() bool) {
	note := func(text string, d time.Duration) {
		if listening() {
			i.Note(text, d)
		}
	}
	for _, t := range tools {
		switch x := t.(type) {
		case *screen.Tools:
			x.Hide = i.Suspend
		case *dictate.Tools:
			x.Show = note
			if x.Confirm != nil {
				x.Confirm.Show = note
			}
		case *browser.Tools:
			x.Show = note
		}
	}
}

// toolFlags choose the node's client tools; `run` and `listen` share them.
type toolFlags struct {
	anyOpen    *bool
	noBrowser  *bool
	confirm    *string
	py, cmdDir *string
	// origins is this agent's server, for the Jarvis page guard (jarvispage.go): build makes
	// it, the caller fills it once it knows the server.
	origins *jarvispage.Origins
}

func addToolFlags(fs *flag.FlagSet) *toolFlags {
	return &toolFlags{
		anyOpen:   fs.Bool("allow-open-any", false, "let desktop_open open any path, app or URI scheme (default: http, https, mailto)"),
		noBrowser: fs.Bool("no-browser", false, "don't serve the browser extension (no browser_* tools)"),
		confirm: fs.String("confirm-dictation", dictate.ConfirmOff,
			"ask the user before dictation acts: off, submit (before Enter, a click, a submitting fill) or all"),
		py:     fs.String("sidecar-python", "", "python interpreter for the package sidecar (spike)"),
		cmdDir: fs.String("sidecar-commands", "", "directory of IJarvisCommand modules for the sidecar (spike)"),
	}
}

// build is the one toolset of this node: the desktop tools, read-the-screen, the browser tools (offered while
// the extension is connected), then the sidecar's commands. The MQTT dispatcher and voice
// turns both serve it. promptAX asks macOS for Accessibility (the
// long-running `run` does; `listen`, a test command often started over SSH, doesn't).
//
// st, when set, is the node's settings store: the sidecar's packages read their secrets and
// keep their data there (DESIGN §16).
func (f *toolFlags) build(ctx context.Context, log *slog.Logger, promptAX bool, st *nodesettings.Store) (*host.Host, node.Multi, func(), error) {
	h, err := nativeHost()
	if err != nil {
		return nil, nil, nil, err
	}
	// JARVIS_AGENT_NO_AX_PROMPT=1: a throwaway test copy (its own bundle id) mustn't ask.
	if promptAX && macax.Supported() && !macax.Trusted() && os.Getenv("JARVIS_AGENT_NO_AX_PROMPT") == "" {
		// macOS adds this app to Privacy & Security → Accessibility and shows its prompt. Window
		// tools appear once the user turns it on (capabilities are computed per call).
		macax.Prompt()
		log.Warn("Accessibility not granted: window control is off until you enable Jarvis Automation Agent in System Settings → Privacy & Security → Accessibility")
	}
	mode, err := dictate.ParseConfirmMode(*f.confirm)
	if err != nil {
		return nil, nil, nil, err
	}
	dt, err := dictationTools(h, mode, log)
	if err != nil {
		return nil, nil, nil, err
	}
	f.origins = &jarvispage.Origins{}
	guard := &jarvispage.Guard{Origins: f.origins}
	dt.JarvisPage = guard.InFront
	tools := node.Multi{openThenType(desktop.New(h, desktop.OpenPolicy{AllowAny: *f.anyOpen}, appCatalogue(log)), dt), &screen.Tools{C: screen.Native(), Log: log}, dt}
	closeAll := func() {}
	if !*f.noBrowser {
		hub, stopBrowser := startBrowser(ctx, log)
		bt := browser.NewTools(hub)
		bt.Confirm = dt.Confirm
		bt.Origins = f.origins
		guard.Tab = hub.FocusedTab
		tools = append(tools, bt)
		closeAll = stopBrowser
	}
	if *f.py != "" && *f.cmdDir != "" {
		var storage sidecar.Storage
		if st != nil {
			storage = st
		}
		sc, err := sidecar.Start(ctx, *f.py, *f.cmdDir, log, storage)
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

// dictationTools types and presses keys on this desktop (DESIGN.md §14). One Confirmer serves
// them and the browser's fill and click.
func dictationTools(h *host.Host, mode string, log *slog.Logger) (*dictate.Tools, error) {
	kb, win, err := dictate.Native(h, macax.Trusted)
	if err != nil {
		return nil, err
	}
	return &dictate.Tools{KB: kb, Win: win, Confirm: &dictate.Confirmer{Mode: mode}, Log: log, Apps: appCatalogue(log)}, nil
}

// openThenType shares one launch record between desktop_open and dictation, so "open X and
// type Y" waits for X's window (DESIGN.md §14).
func openThenType(d *desktop.Tools, dt *dictate.Tools) *desktop.Tools {
	tr := &launches.Tracker{}
	if dt.Win != nil {
		win := dt.Win
		tr.Windows = func(ctx context.Context) ([]string, error) {
			if ok, why := win.Available(ctx); !ok {
				return nil, errors.New(why)
			}
			ws, err := win.List(ctx)
			ids := make([]string, len(ws))
			for i, w := range ws {
				ids[i] = w.ID
			}
			return ids, err
		}
	}
	d.Launched, dt.Launched = tr, tr
	return d
}

// dictationStatus is status's line: the typing backend and whether named apps work.
func dictationStatus(ctx context.Context, h *host.Host) string {
	kb, win, err := dictate.Native(h, macax.Trusted)
	if err != nil {
		return err.Error()
	}
	ok, why := kb.Available(ctx)
	if !ok {
		return "off: " + why
	}
	line := "on (" + kb.Name() + ")"
	if wok, wwhy := win.Available(ctx); wok {
		line += "; typing into a named app: on"
	} else {
		line += "; typing into a named app: off (" + wwhy + ")"
	}
	return line
}

// --- status ---

func status(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cp := fs.String("creds", "", "credentials file")
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	h, err := nativeHost()
	if err != nil {
		return err
	}
	caps, err := h.Capabilities(ctx)
	if err != nil {
		return err
	}
	if line := staleBuildLine(version, sourceDir, gitHead, gitIsAncestor); line != "" {
		fmt.Println(line)
	}
	fmt.Printf("platform:     %s\ncapabilities: %s\n", h.Platform(), strings.Join(caps, ", "))
	if macax.Supported() {
		trusted := macax.Trusted()
		fmt.Printf("accessibility: %v\n", trusted)
		if !trusted {
			fmt.Print(`  -> Window control is OFF: this process has no Accessibility grant.
     Turn on "Jarvis Automation Agent" in System Settings > Privacy & Security > Accessibility.
     If it already looks on, it's an old entry from a differently signed build. Run
       tccutil reset Accessibility net.jarvisautomation.agent && tccutil reset AppleEvents net.jarvisautomation.agent
     then restart the agent and allow it again. (Run from a terminal, this check reports the
     terminal's grant; use scripts/agent-dev-run.sh --status for the agent's own.)
`)
		}
	}
	if macax.Supported() {
		screen := macax.ScreenTrusted()
		fmt.Printf("screen recording: %v\n", screen)
		if !screen {
			// Puts Jarvis Automation Agent in the list and shows macOS's prompt (read-the-screen needs it).
			macax.ScreenPrompt()
			fmt.Print("  -> Reading the screen is OFF: allow \"Jarvis Automation Agent\" in System Settings > Privacy & Security >\n" +
				"     Screen & System Audio Recording, then restart the agent.\n")
		}
	}
	fmt.Printf("voice:        %s\n", voiceStatus())
	fmt.Printf("menu icon:    %s\n", trayStatus())
	fmt.Printf("dictation:    %s\n", dictationStatus(ctx, h))
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
	// No capabilities: this process doesn't hold the browser connection, so it must not report
	// the extension's state over the running agent's.
	if err := node.NewCC(ccURL, c).Heartbeat(ctx, node.Beat{Version: version, InstallMode: installMode, NodeKind: nodeKind, Busy: true}); err != nil {
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
	server := fs.String("server", "", "the Jarvis server whose web page dictation and browser_fill/click won't act on (default: the enrolled one's)")
	_ = fs.Parse(args)
	h, err := nativeHost()
	if err != nil {
		return err
	}
	dt, err := dictationTools(h, dictate.ConfirmOff, nil)
	if err != nil {
		return err
	}
	origins := tryOrigins(ctx, *server)
	guard := &jarvispage.Guard{Origins: origins}
	dt.JarvisPage = guard.InFront
	ts := node.Multi{openThenType(desktop.New(h, desktop.OpenPolicy{AllowAny: *anyOpen}, appCatalogue(nil)), dt), &screen.Tools{C: screen.Native()}, dt, &clock.Tool{}}
	if fs.NArg() == 0 || strings.HasPrefix(fs.Arg(0), "browser_") {
		// Serve the extension for this one call (fails over to "not connected" when a running
		// agent holds the socket: stop it, or use that agent).
		hub, stopBrowser := startBrowser(ctx, logger())
		defer stopBrowser()
		bt := browser.NewTools(hub)
		bt.Origins = origins
		guard.Tab = hub.FocusedTab
		ts = append(ts, bt)
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
	// TOOL [ARGS] [TOOL [ARGS] ...]: several calls run in order in this one process, as a
	// model's chain would ("open X" then "type into X").
	rest := fs.Args()
	for len(rest) > 0 {
		name := rest[0]
		rest = rest[1:]
		a := map[string]any{}
		if len(rest) > 0 && strings.HasPrefix(strings.TrimSpace(rest[0]), "{") {
			if err := json.Unmarshal([]byte(rest[0]), &a); err != nil {
				return fmt.Errorf("arguments must be a JSON object: %w", err)
			}
			rest = rest[1:]
		}
		out, ok := ts.Call(ctx, name, a, node.Request{})
		if !ok {
			return fmt.Errorf("unknown tool %q", name)
		}
		if err := elideAttachments(out, *saveImages); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
	}
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
	if len(args) == 1 && args[0] == "--vocabulary" {
		// What a voice turn sends for speech-to-text (without the open windows' apps).
		fmt.Println(apps.Vocabulary(nil, list, dictate.MaxVocabulary))
		return nil
	}
	if len(args) == 0 {
		for _, a := range list {
			fmt.Printf("%-40s %s\n", a.Name, a.Path)
		}
		return nil
	}
	p, err := apps.Find(list, strings.Join(args, " "))
	if err != nil {
		return err
	}
	how := ""
	if p.Fuzzy {
		how = fmt.Sprintf(" (a near or sound-alike name for %q)", p.HeardAs)
	}
	fmt.Printf("%s -> %s%s\n", p.App.Name, p.App.Path, how)
	return nil
}
