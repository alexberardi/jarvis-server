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
	"maps"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/builtins"
	"github.com/alexberardi/jarvis-server/internal/agent/clock"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/deviceid"
	"github.com/alexberardi/jarvis-server/internal/agent/devices"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	displaynode "github.com/alexberardi/jarvis-server/internal/agent/display"
	"github.com/alexberardi/jarvis-server/internal/agent/hat"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/jarvispage"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
	"github.com/alexberardi/jarvis-server/internal/agent/music"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
	"github.com/alexberardi/jarvis-server/internal/agent/pkgagents"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
	"github.com/alexberardi/jarvis-server/internal/agent/views"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/buildinfo"
	adminui "github.com/alexberardi/jarvis-server/web/admin"
)

var version = "dev"

// Values of the heartbeat's browser_extension capability.
const (
	extConnected    = "connected"    // a browser's extension is talking to this agent
	extDisconnected = "disconnected" // browser control is on, no extension connected
	extDisabled     = "disabled"     // run --no-browser
)

// heartbeatCaps reports the browser extension's state, and with a browser connected whose
// extension says, browser_website_access "on" or "off" (off while any has it off: pages can't be
// read or acted on there) and, when off, browser_website_access_off_in naming those browsers (for
// the admin's per-browser step); it wakes a heartbeat when these change;
// with sw, also computer control's switch as computer_control "on" or "off" (its changes wake
// a heartbeat through Runner.BeatNow).
func heartbeatCaps(tools node.Multi, sw *computer.Switch) (caps func() map[string]any, changed func() <-chan struct{}) {
	var hub *browser.Hub
	for _, t := range tools {
		if bt, ok := t.(*browser.Tools); ok {
			hub = bt.Hub
		}
	}
	return func() map[string]any {
		state := extDisabled
		if hub != nil {
			state = extDisconnected
			if hub.Connected() {
				state = extConnected
			}
		}
		out := map[string]any{"browser_extension": state}
		if hub != nil {
			if a := hub.WebsiteAccess(); a != nil {
				out["browser_website_access"] = map[bool]string{true: "on", false: "off"}[*a]
			}
			if names := accessOffIn(hub.Browsers()); names != "" {
				out["browser_website_access_off_in"] = names
			}
		}
		if sw != nil {
			out["computer_control"] = map[bool]string{true: "on", false: "off"}[sw.On()]
		}
		return out
	}, hubChanged(hub)
}

// accessOffInMax is cc's limit on a capability's string (cleanCaps' maxCapLen).
const accessOffInMax = 64

// accessOffIn names the connected browsers whose extension says website access is off, once
// each ("Firefox", "Firefox, Brave"; "unknown" for one that gave no name); "" when none is off,
// or when the names don't fit a capability (the admin then shows every browser's step).
func accessOffIn(bs []browser.BrowserInfo) string {
	var names []string
	for _, b := range bs {
		if b.WebsiteAccess == nil || *b.WebsiteAccess {
			continue
		}
		name := b.Browser
		if name == "" {
			name = "unknown"
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	out := strings.Join(names, ", ")
	if len(out) > accessOffInMax {
		return ""
	}
	return out
}

// hubChanged is hub's Changed, nil without a hub.
func hubChanged(hub *browser.Hub) func() <-chan struct{} {
	if hub == nil {
		return nil
	}
	return hub.Changed
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
	case "display-player":
		err = displaynode.RunReceiverPlayer(ctx, os.Args[2:])
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
	case "wake-compare":
		err = wakeCompareCmd(ctx, os.Args[2:])
	case "apps":
		err = appsCmd(os.Args[2:])
	case "browser":
		err = browserCmd(ctx, os.Args[2:])
	case "hat":
		err = hatCmd(ctx, os.Args[2:])
	case "provision":
		err = provisionCmd(ctx, os.Args[2:])
	case "adopt-python":
		err = adoptPythonCmd(ctx, os.Args[2:])
	case "dictation":
		err = dictationCmd(ctx, os.Args[2:])
	case "computer":
		if len(os.Args) > 2 && os.Args[2] == "snapshot" {
			err = computerSnapshotCmd(ctx, os.Args[3:])
		} else if len(os.Args) > 3 && os.Args[2] == "record" && os.Args[3] != "start" && os.Args[3] != "stop" {
			err = computerRecordCmd(ctx, os.Args[3:], os.Stdin, os.Stdout)
		} else if len(os.Args) > 2 && os.Args[2] == "download" {
			err = computerDownloadCmd(ctx, os.Args[3:])
		} else {
			err = computerCmd(os.Args[2:], os.Stdout, controlDial)
		}
	case "island-demo":
		err = islandDemo(ctx, os.Args[2:])
	case "browser-host":
		os.Exit(browserHost(strings.Join(os.Args[2:], " ")))
	case "version":
		fmt.Println(buildinfo.Version(version))
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
           risky: pays, sends, deletes, shares, changes the account (default: submit)
           [--sidecar-python PY --sidecar-commands DIR]
           [--no-overlay] [--no-tray]   no on-screen island / no status icon
           [--voice-input DEV] [--voice-output DEV] [--wake-threshold 0.4] [--wake-models DIR]
           [--no-chimes]   listening for "Hey Jarvis" (Linux, macOS): on and off from the
           status icon's menu
           [--profile auto|desktop|pi]   pi: a Raspberry Pi voice node, headless, no desktop
           tools (auto: pi on a Pi with the ReSpeaker HAT or with /etc/jarvis-node; also for
           enroll, status and listen)
  provision  a Pi's setup API for the mobile app (:8080, no authentication: only while the
           setup hotspot is up), until the node is provisioned
           --wifi simulated (the NetworkManager backend is still to come) [--listen :8080]
           [--captive-listen :80] [--ap-ssid SSID] [--creds PATH]
  adopt-python  a Pi with no node.json takes over its provisioned Python node's identity (node id
           and key, room) and local state (command secrets, reminders, timers, K2), leaving the
           Python node's files as they are; nothing when there is none
           [--creds PATH] [--python-dir /opt/jarvis-node] [--secret-dir ~/.jarvis]
  listen   the voice pipeline alone (no MQTT), same voice and tool flags
           [--once] [--timeout DUR] [--no-tools]   e.g. --voice-input wav:hey_jarvis.wav --once
  wake-compare  record Pulse sources side by side and score each for "hey Jarvis" (debug; on a
           Pi with music playing it compares the raw mic with the echo-cancelled one)
           [--seconds 8] [--sources jarvis_mic_raw,jarvis_ec_source] [--floor 0.1]
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
  computer stop | status   stop what Jarvis is doing with this computer (a computer task, a
           recording; the menu's "Stop Jarvis" does the same), or say what it is doing
  computer on | off        let Jarvis use this computer, or not (the menu's "Computer control";
           off by default, and off stops a task going on)
  computer record start [--desktop] | stop   record what you do in the browser (the menu's
           "Record a macro…"), or on the desktop ("Record the desktop…"), and send it to Jarvis when you stop
  computer click X Y       debug: a left click at X, Y (global logical coordinates) through
           this desktop's input backend
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
  computer snapshot [--json] URL   open URL in a background browser window and list its
           elements as computer control sees them (debug; serves the extension like try)
  computer record --browser [--json]   record what you do in the browser until Enter or ^C,
           printing each event as it arrives (debug; serves the extension like try)
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
	prof := profileFlag(fs)
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	p, err := thisProfile(*prof)
	if err != nil {
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
		return node.WithLocalNetworkHint(runtime.GOOS, err)
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
	reg := node.Registration{Room: regRoom, DeviceID: deviceid.Get(), NodeKind: p.nodeKind}
	c, err := node.RegisterPreferring(ctx, hc, nodeCC, ccURL, *server, id, tok, reg, func(err error) {
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
	if p.desktop {
		offerBrowserSetup(*browserYes, *browserNo)
	}
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
	// Someone else may have typed the code: join only once the user here says the household
	// is theirs (loop #100).
	fmt.Print(pairingQuestion(res))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if !pairingAnswerYes(line) {
		return node.PairingResult{}, errors.New("not joined: the household was declined (start again for a new code)")
	}
	return res, nil
}

// pairingQuestion asks whether to join the household that approved the code.
func pairingQuestion(res node.PairingResult) string {
	hh := res.HouseholdName
	if hh == "" {
		hh = res.HouseholdID
	}
	by := ""
	if res.ApprovedBy != "" {
		by = " by " + res.ApprovedBy
	}
	return fmt.Sprintf("Approved%s into household %q (room %q).\nJoin this household? [y/N] ", by, hh, res.Room)
}

// pairingAnswerYes reads the join question's answer: only an explicit yes joins.
func pairingAnswerYes(line string) bool {
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes"
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
	computerOn         *bool
	noTray             *bool
	displayOn          *bool
	noAutoUpdate       *bool
	profile            *string
	vf                 *voiceFlags
	hf                 *hatFlags
	mf                 *maintFlags
}

// voiceByDefault is run's --voice default: a desktop install listens for "Hey Jarvis" out of
// the box (user, 2026-10-13). The status icon's toggle is how a user turns it off (it saves
// agent.json's `voice`, which overrides this); --voice=false is for scripts and development.
const voiceByDefault = true

func newRunFlags(errs flag.ErrorHandling) (*flag.FlagSet, *runFlags) {
	fs := flag.NewFlagSet("run", errs)
	return fs, &runFlags{
		displayOn: fs.Bool("display", false, "offer the local touch display, configured by the server; needs a Linux graphical session with a screen"),
		cp:        fs.String("creds", "", "credentials file"),
		config:    fs.String("config", "", "settings file (default: <user config dir>/jarvis-agent/agent.json); flags given here win over it"),
		tf:        addToolFlags(fs),
		voiceOn:   fs.Bool("voice", voiceByDefault, `listen for "hey jarvis" and run voice turns`),
		computerOn: fs.Bool("computer-control", false, `let Jarvis use this computer (computer tasks, recording); `+
			`off by default, the menu's "Computer control" sets it`),
		noOverlay: fs.Bool("no-overlay", false, "don't show the on-screen indicator (the island: voice states, the \"Jarvis is running\" pill)"),
		noTray:    fs.Bool("no-tray", false, "don't show the status icon and its menu (menu bar, tray, notification area)"),
		noAutoUpdate: fs.Bool("no-auto-update", false, "don't update to the server's version by itself, and refuse updates sent from Jarvis Automation "+
			"(jarvis-agent update still works)"),
		profile: profileFlag(fs),
		vf:      addVoiceFlags(fs),
		hf:      addHatFlags(fs),
		mf:      addMaintenanceFlags(fs),
	}
}

// run is the agent. A computer's run that was signed out doesn't exit: it waits for the new
// sign-in on the setup page, then runs again in place (signedOut.rejoin).
func run(ctx context.Context, args []string) error {
	err := runAgent(ctx, args)
	var so signedOut
	if errors.As(err, &so) {
		return so.rejoin(ctx, listenControl)
	}
	return err
}

func runAgent(ctx context.Context, args []string) error {
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
	p, err := thisProfile(*f.profile)
	if err != nil {
		return err
	}
	if p.desktop {
		regroupInput(log)
	}
	if !p.desktop {
		// A Pi is headless: no status icon, no island, nothing that opens a browser.
		*f.noTray, *f.noOverlay = true, true
	}
	var adoptLogin bool
	if p.desktop && runtime.GOOS == "darwin" {
		// A stale copy steps aside for the one installed in /Applications (#483); the
		// installed copy re-points the login item at itself further down, once it holds the
		// control socket.
		done, adopt := runHandover(log)
		if done {
			return nil
		}
		adoptLogin = adopt
	}
	cp, tf, voiceOn, noOverlay, vf, hf := f.cp, f.tf, f.voiceOn, f.noOverlay, f.vf, f.hf
	vf.clampFollowUp(log)
	tf.pi = !p.desktop
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
	if errors.Is(err, node.ErrNotEnrolled) && !p.desktop {
		// A Pi the Python node ran on: the same node, not a new one (install-pi.sh --activate
		// adopts it first; this is for a unit started without it).
		if py, perr := pinode.DefaultPythonNode(); perr == nil {
			c, err = loadOrAdopt(ctx, log, path, py)
		}
	}
	if err != nil {
		if errors.Is(err, node.ErrNotEnrolled) && !interactive() && p.desktop {
			// A login item on a computer that isn't set up: the status icon's "Set up", then
			// the agent once it is (setupwait.go). Typed in a terminal, it says what to do.
			return waitForSetup(ctx, log, path, *f.noTray, ctl, false)
		}
		if errors.Is(err, node.ErrNotEnrolled) && !interactive() && !p.desktop {
			// A Pi that isn't enrolled (a factory reset, an install without a code) is in setup
			// mode until the app provisions it, then the node (setupmode.go).
			if err = piSetupMode(ctx, log, path, hf); err == nil {
				c, err = node.LoadCreds(path)
			} else if ctx.Err() != nil {
				if ctl != nil {
					_ = ctl.Close()
				}
				return nil // stopped while waiting
			}
		}
	}
	if err != nil {
		if ctl != nil {
			_ = ctl.Close()
		}
		return notSetUp(err)
	}
	migrateLegacyID(log)
	if adoptLogin {
		adoptIfInstalled(ctx, log)
	}
	ctx, quit := context.WithCancel(ctx)
	defer quit()
	pill := &runningPill{}
	// What Jarvis is doing with the computer: the island and the status icon show it; the
	// icon's Stop, `jarvis-agent computer stop` and the control socket end it.
	activity := &computer.Activity{}
	// Computer control's switch (COMPUTER-CONTROL.md C11): a computer's only, off unless
	// agent.json's computer_control (the menu's toggle) turned it on.
	var computerSw *computer.Switch
	if p.desktop {
		computerSw = computer.NewSwitch(*f.computerOn, activity)
		computerSw.Persist = func(on bool) error { return saveComputer(*f.config, on) }
	}
	var swReady atomic.Pointer[voiceSwitch]
	var recReady atomic.Pointer[computer.Recorder]
	var runner atomic.Pointer[node.Runner]
	if ctl != nil {
		// Healthy (the self-update's check) once connected to jarvisd's broker.
		health := func() error {
			if r := runner.Load(); r != nil && r.MQTTConnected() {
				return nil
			}
			return errors.New("not connected to jarvisd yet")
		}
		defer serveControl(ctx, ctl, log, pill, quit, swReady.Load, health, activity, computerSw, recReady.Load)()
	}
	// K2, command secrets and data live next to node.json; another node's are dropped here.
	st, err := nodesettings.OpenStore(filepath.Dir(path), c.NodeID)
	if err != nil {
		return fmt.Errorf("node settings: %w", err)
	}
	tf.pkgRoot = filepath.Join(filepath.Dir(path), "packages")
	// The pipeline's microphone: voice-profile takes, and on a Pi what its built-ins say.
	mic := voice.NewMic()
	var duck *music.Ducker // a Pi's music: ducked for each turn, taken over by a music "play"
	if !p.desktop {
		duck = newPiMusic(hat.Detected, fileExists, log)
		tf.host = newPiHost(ctx, filepath.Dir(path), mic, log)
		tf.host.Music = duck
	}
	// jarvisd's client once connected: approval tokens are checked with it, recordings uploaded.
	var liveCC atomic.Pointer[node.CC]
	var note atomic.Pointer[func(string)] // the island's note, once it is up
	if p.desktop {
		// Computer tasks in the browser and on the desktop (plan P3 pieces 8 and 22): build
		// adds the executor's tools after the browser's.
		tf.computer = func(bt *browser.Tools, apps computer.Apps, h *host.Host) node.Toolset {
			var b computer.Browser
			if bt != nil {
				b = computer.BrowserOf(bt)
			}
			d := computer.NativeDesktop(apps)
			if d != nil {
				d.Focus = func(ctx context.Context, id string) error { return h.WindowDo(ctx, host.ActionFocus, id) }
			}
			return &computer.Tools{Browser: b, Desktop: d, Switch: computerSw, Activity: activity,
				CC: func() computer.Approver {
					if cc := liveCC.Load(); cc != nil {
						return cc
					}
					return nil
				}}
		}
	}
	tf.cc = liveCC.Load
	h, tools, closeTools, err := tf.build(ctx, log, true, st)
	if err != nil {
		return err
	}
	defer closeTools()
	// Recording what the user does (plan P3 piece 29): a computer's only. A trace stopped here
	// is uploaded once connected to jarvisd (liveCC); the island says when it waits for that.
	// computer_switch is computer control's switch for jarvisd's voice tools
	// (enable/disable_computer_control).
	var recorder *computer.Recorder
	var uploads *recordingUploads
	if p.desktop {
		recorder, uploads = newRecorder(filepath.Join(filepath.Dir(path), "recordings"), activity, tools, liveCC.Load, func(text string) {
			if n := note.Load(); n != nil {
				(*n)(text)
			}
		}, log)
		tools = append(tools, &computer.RecordTools{R: recorder}, &computer.SwitchTools{S: computerSw})
		recReady.Store(recorder)
	}
	go learnServer(ctx, c.ConfigURL, tf.origins, log)
	caps, _ := h.Capabilities(ctx)
	log.Info("jarvis-agent starting", "version", version, "creds", c, "platform", h.Platform(), "capabilities", caps)
	// Commands the household turned off in the app's node settings are neither offered nor run.
	var displayEngine *displaynode.Engine
	if *f.displayOn {
		if runtime.GOOS != "linux" {
			return errors.New("touch display requires Linux")
		}
		ui, _ := adminui.UI()
		displayEngine, err = displaynode.StartDisplay(ctx, filepath.Dir(path), ui)
		if err != nil {
			return err
		}
		displayEngine.Host = h
		defer displayEngine.Close()
	}
	var servedTools node.Toolset = tools
	if displayEngine != nil {
		servedTools = node.Multi{displayEngine, tools}
	}
	served := nodesettings.Gate{Tools: servedTools, Store: st}
	tf.routine.Tools = served // a step the household turned off doesn't run
	r := &node.Runner{Creds: c, Tools: served, Version: version, InstallMode: p.installMode, NodeKind: p.nodeKind, DeviceID: deviceid.Get(), Log: log}
	runner.Store(r)
	if p.desktop {
		// The browser extension's state is a computer's; a Pi has none to report.
		r.Capabilities, r.Changed = heartbeatCaps(tools, computerSw)
		computerSw.Watch(func(on bool) {
			log.Info("computer control turned " + map[bool]string{true: "on", false: "off"}[on])
			r.BeatNow()
		})
	}
	if !p.desktop && hat.Detected() {
		// Before voice opens the mic: the codec may have come back from a boot without the
		// installer's ADC filter and output levels.
		hat.HealCodec(ctx, hat.ExecRun, log)
	}
	if duck != nil {
		duck.NormalizePGA(ctx) // a crash mid-music must not leave the mic at the music gain
		duck.EC.Cleanup(ctx)   // nor the echo canceller loaded
	}
	var press func(*hat.LEDs) // the HAT button reads the Pi's alerts
	if tf.host != nil {
		alerts := startAlerts(ctx, tf.host, mic, tf.reminder, log)
		press = func(leds *hat.LEDs) { readAlerts(ctx, alerts, leds, log) }
	}
	hd := openHat(hf, press, log)
	defer hd.Close()
	if hd != nil && tf.host != nil {
		tf.host.Alerts.Watch(alertLED(hd.leds))
	}
	var nodeConfig func() map[string]any
	var live *voice.Live // the Pi's: the app's settings and noise measurements reach the pipeline
	if !p.desktop {
		live = voice.NewLive()
		sp, err := settingsPath(*f.config)
		if err != nil {
			return err
		}
		var audio *sdkhost.Audio
		if tf.host != nil {
			audio = tf.host.Audio
		}
		var state func() pinode.State
		nodeConfig, state = wirePiNode(r, vf, hf, f.mf, hd, live, audio, sp, log)
		startMaintenance(ctx, state, quit, log)
		if hd != nil && hd.leds != nil {
			// The admin's Nodes page shows and sets this node's LED mode.
			r.Capabilities = func() map[string]any { return map[string]any{"led_mode": state().LEDMode()} }
		}
	}
	// The device families (Pantry packages in the sidecar) answer the app's device requests and
	// are in the settings snapshot, on a Pi and a computer alike.
	// A secrets push can configure a family (an API key): it joins the heartbeat's protocols.
	var devs func(context.Context) nodesettings.Devices
	var secrets func()
	sc := liveSidecar(tools)
	if sc != nil {
		devs = sc.Devices
		secrets = func() {
			if err := sc.RefreshDevices(); err != nil {
				log.Warn("devices: families not described again after a secrets push", "err", err)
			}
			r.BeatNow()
		}
	}
	wireNodeSettings(r, st, tools, nodeConfig, devs, secrets, log)
	wirePackages(r, tf.pkgs, log)
	// action, callback and toggle_command: the app's device control, a command's buttons and
	// callbacks, run by the sidecar; a voice turn's action message is said between turns.
	wireActions(r, sc, tools, st, sayNow(mic), log)
	if sc != nil {
		wireDevices(r, sc, tf.registry, log)
		wireRoutineVerb(r, served, log)
	}
	if !p.desktop {
		defer wireBluetooth(ctx, r, filepath.Dir(path), log)()
		wirePiTasks(r, st, filepath.Dir(path), tools, tf.pkgs, tf.host.Timers, quit, log)
	}
	// Voice is an extra: if it can't start, the agent still serves its tools. The status icon's
	// toggle turns it on and off while the agent runs (voiceSwitch).
	sw := newVoiceSwitch(*voiceOn && voice.Supported, log)
	sw.load = func(ctx context.Context) (*voiceStack, error) {
		vs, err := loadVoice(ctx, vf, log)
		if vs != nil {
			vs.live = live
			vs.cfg.Greeting = !p.desktop // a Pi greets the wake word in words (row 6)
		}
		return vs, err
	}
	// A Pi's voice-profile takes go through the pipeline's microphone (voiceprofile.go).
	addVerbs(r, voiceProfileVerbs(p, mic, log))
	sw.serve = func(ctx context.Context, vs *voiceStack, cc *node.CC) {
		// Voice turns run the same toolset the MQTT dispatcher serves.
		o := vs.options(cc, served, log, nil)
		o.Mic = mic
		o.Computer = activity // the wake word pauses a computer task, "stop" ends it
		withPiMusic(&o, duck)
		_ = voice.Serve(ctx, o)
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
	if onConnected := r.OnConnected; p.desktop {
		r.OnConnected = func(ctx context.Context, cc *node.CC) {
			go uploads.serve(ctx, cc) // the recordings not sent yet
			if onConnected != nil {
				onConnected(ctx, cc)
			} else {
				<-ctx.Done()
			}
		}
	}
	if displayEngine != nil {
		sw.addIndicator(displayEngine)
		previousCaps := r.Capabilities
		r.Capabilities = func() map[string]any {
			caps := map[string]any{}
			if previousCaps != nil {
				caps = previousCaps()
			}
			for k, v := range displayEngine.Capabilities() {
				caps[k] = v
			}
			return caps
		}
		previousConnect := r.OnConnected
		r.OnConnected = func(ctx context.Context, cc *node.CC) {
			go displayEngine.Watch(ctx, cc)
			if previousConnect != nil {
				previousConnect(ctx, cc)
			} else {
				<-ctx.Done()
			}
		}
		previousMQTT := r.OnMQTTConnect
		r.OnMQTTConnect = func(ctx context.Context, cc *node.CC) {
			if previousMQTT != nil {
				previousMQTT(ctx, cc)
			}
			_ = displayEngine.Synchronise(ctx, cc)
		}
		if r.Topics == nil {
			r.Topics = map[string]func(context.Context, *node.CC, []byte){}
		}
		r.Topics["display/config"] = func(ctx context.Context, cc *node.CC, _ []byte) { _ = displayEngine.Synchronise(ctx, cc); r.BeatNow() }
	}
	connectCC(r, &liveCC)
	connectHost(r, tf.host)
	var setup func()
	if p.desktop {
		setup = openSetupPage(log)
	}
	// Signed out (from Add-ons, an admin, or the menu's Sign out): drop node.json only, keep
	// agent.json and apps.json, and open the setup page so someone can sign in again; a
	// computer's run then waits for the new sign-in (run below).
	end := nodeEnd{credsFile: path, nodeID: c.NodeID, desktop: p.desktop, noTray: *f.noTray, stop: quit, openSetup: setup, log: log}
	runNode := func(ctx context.Context) error { return end.of(r.Run(ctx)) }
	ic := &trayIcon{off: *f.noTray, log: log, sw: sw, h: h, configURL: c.ConfigURL, quit: quit, setup: setup, signOut: r.SignOut,
		activity: activity, computer: computerSw, recorder: recorder, reached: r.Reached, retryNow: r.RetryNow,
		webUI: make(chan struct{}, 1)}
	sw.changed = ic.voiceChanged
	// The agent may be started before its server: while the server doesn't answer, the menu
	// says what it is waiting for (and offers "Retry now") and the island says it in a line,
	// instead of a menu that claims it runs and a microphone that never opens (#585).
	wait := &serverWait{refresh: ic.refresh, log: log, note: func(text string) {
		if n := note.Load(); n != nil {
			(*n)(text)
		}
	}}
	r.OnWaiting = wait.waiting
	r.OnReached = func(url string) {
		wait.reached(url)
		ic.lookForWebUI() // "Open Jarvis Automation" too, without waiting out its own backoff
	}
	sw.waiting = wait.voiceWaiting
	if computerSw != nil {
		computerSw.Watch(func(bool) { ic.refresh() })
	}
	activity.Show(ic.activityChanged)
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
		return island.RunLoop(ctx, log, appEvents(pill, setup, activity, log), func(loop bool) error {
			ic.mainLoop = loop
			defer ic.show(ctx)()
			return runNode(ctx)
		})
	}
	// The island needs the main thread on macOS: island.Run keeps it and runs the agent on
	// other goroutines. Where it can't show one, it says why once and runs the agent as is.
	// It runs with voice off too, for the "Jarvis is running" pill; it shows voice states and
	// dictation's notes only while listening is on.
	return island.RunWith(ctx, log, appEvents(pill, setup, activity, log), func(i *island.Island) error {
		if i == nil {
			pill.Attach(nil)
		} else {
			hideFromScreenshots(tools, i)
			// Shown whether or not listening is on: Jarvis using the computer is always shown.
			activity.Show(func(k computer.Kind, text string) { i.SetActivity(islandActivity(k), text) })
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
// dictation types, fills, clicks or asks, and what window commands did, while listening is on.
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
		case *desktop.Tools:
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
	// pkgRoot, when the caller sets it, is where Pantry packages are installed: build starts
	// the sidecar on them and leaves pkgs to answer the app's install requests.
	pkgRoot string
	pkgs    *packages.Manager
	// pi: the Pi profile's toolset, with no desktop, screen, dictation or browser tools; its
	// sidecar also loads the node's built-in commands, whose SDK backends host answers.
	pi   bool
	host *sdkhost.Host
	// live is the sidecar in service once build ran (the Pi's reminders are polled from it).
	live *sidecar.Live
	// cc, when the caller sets it, is jarvisd's client while connected, for the Go built-ins;
	// registry is the household's direct devices, which build makes (wireDevices keeps it fresh)
	// and routine the routine built-in, whose steps run on the served toolset.
	cc       func() *node.CC
	registry *devices.Registry
	routine  *builtins.Routine
	// reminder is the reminder built-in (with a store), whose due reminders a Pi's poller fires.
	reminder *builtins.Reminder
	// computer, when the caller sets it, is the computer-task executor's tools on the browser
	// extension's (nil with --no-browser) and the desktop, whose apps launch opens through
	// desktop_open's catalogue and whose windows h focuses: build adds them after the browser tools.
	computer func(bt *browser.Tools, apps computer.Apps, h *host.Host) node.Toolset
}

func addToolFlags(fs *flag.FlagSet) *toolFlags {
	return &toolFlags{
		anyOpen:   fs.Bool("allow-open-any", false, "let desktop_open open any path, app or URI scheme (default: http, https, mailto)"),
		noBrowser: fs.Bool("no-browser", false, "don't serve the browser extension (no browser_* tools)"),
		confirm: fs.String("confirm-dictation", dictate.ConfirmSubmit,
			"ask the user before dictation acts: off, submit (before Enter, and a browser click or submit that pays, sends, deletes, shares or changes the account; the default) or all"),
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
// keep their data there (DESIGN §16). The Pi profile (f.pi) has only the sidecar and the Go built-ins.
func (f *toolFlags) build(ctx context.Context, log *slog.Logger, promptAX bool, st *nodesettings.Store) (*host.Host, node.Multi, func(), error) {
	h, err := nativeHost()
	if err != nil {
		return nil, nil, nil, err
	}
	f.origins = &jarvispage.Origins{}
	if f.pi {
		tools, closeAll, err := f.addCommon(ctx, log, st, node.Multi{}, func() {})
		return h, tools, closeAll, err
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
	guard := &jarvispage.Guard{Origins: f.origins}
	dt.JarvisPage = guard.InFront
	opener := openThenType(desktop.New(h, desktop.OpenPolicy{AllowAny: *f.anyOpen}, appCatalogue(log)), dt)
	// Tabs and sheets by name (VOICE-FLOWS.md Q26), and their names for speech-to-text.
	opener.Views = views.Native(h)
	dt.HearFirst = opener.ViewVocabulary
	tools := node.Multi{opener, &screen.Tools{C: screen.Native(), Log: log}, dt}
	closeAll := func() {}
	var bt *browser.Tools
	if !*f.noBrowser {
		if exe, err := agentExe(); err == nil {
			if benv, err := browserEnv(); err == nil {
				refreshBrowserHosts(benv, exe, log)
			}
		}
		hub, stopBrowser := startBrowser(ctx, log)
		bt = browser.NewTools(hub)
		bt.Confirm = dt.Confirm
		bt.Origins = f.origins
		guard.Tab = hub.FocusedTab
		tools = append(tools, bt)
		closeAll = stopBrowser
	}
	if f.computer != nil {
		// A desktop task's app that isn't running opens from desktop_open's catalogue, by name
		// only: never a link or a path, whatever --allow-open-any says.
		tools = append(tools, f.computer(bt, opener, h))
	}
	tools, closeAll, err = f.addCommon(ctx, log, st, tools, closeAll)
	return h, tools, closeAll, err
}

// addCommon adds what every profile has: the package sidecar's commands and the clock. On an
// error it has already run closeAll.
func (f *toolFlags) addCommon(ctx context.Context, log *slog.Logger, st *nodesettings.Store, tools node.Multi, closeAll func()) (node.Multi, func(), error) {
	// The package sidecar: the installed Pantry packages, or the spike's directory of modules.
	live := &sidecar.Live{}
	f.live = live
	var storage sidecar.Storage
	if st != nil {
		storage = st
	}
	switch {
	case *f.py != "" && *f.cmdDir != "":
		sc, err := sidecar.Start(ctx, *f.py, *f.cmdDir, log, storage)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("sidecar: %w", err)
		}
		live.Swap(sc)
	case f.pkgRoot != "":
		f.pkgs = newPackages(f.pkgRoot, live, storage, f.host, log)
		boot := func() {
			if err := f.pkgs.Boot(ctx); err != nil {
				// The agent still serves its own tools; the app shows the packages as not loaded.
				log.Warn("packages: not loaded", "err", err)
			}
		}
		if f.pi && f.host != nil {
			// The first start installs the built-ins (a download): the node comes up meanwhile,
			// and their tools join the next turn's list.
			f.pkgs.Builtins = packages.PiBuiltins(os.Getenv("JARVIS_AGENT_NODE_COMMANDS"))
			// apt packages and post_install ops through install-pi.sh's root wrappers.
			f.pkgs.System = packages.NewSystem(log)
			// The packages' background agents (comfyui_jobs) on their schedules, their alerts
			// in the Pi's queue; one that keeps failing marks its package failed in the app.
			agents := &pkgagents.Scheduler{Sidecar: live, Alerts: f.host.Alerts, Log: log}
			f.pkgs.AgentHealth = agents.Failing
			go agents.Run(ctx)
			go boot()
		} else {
			boot()
		}
	}
	stopBrowser := closeAll
	closeAll = func() { _ = live.Close(); stopBrowser() }
	tools = append(tools, live)
	// After the sidecar, so a package's command of the same name (Python's get_current_time with
	// its city table, Home Assistant's control_device) wins when present.
	cc := f.cc
	if cc == nil {
		cc = func() *node.CC { return nil }
	}
	f.registry = &devices.Registry{}
	f.routine = &builtins.Routine{Store: st, Pantry: pantryRoutineDefs(f.pkgs, log), CC: cc, Log: log}
	tools = append(tools, &builtins.ControlDevice{Devices: f.registry, Families: live, CC: cc},
		&builtins.SendLink{CC: cc}, f.routine, &builtins.Calculate{Store: st}, &builtins.Convert{Store: st},
		&builtins.Joke{CC: cc, Store: st})
	if st != nil {
		// The household's lists, in the node's store as the Python commands kept them.
		tools = append(tools, &builtins.Shopping{Store: st}, &builtins.Todo{Store: st},
			&builtins.ExportTodo{Store: st, CC: cc, Log: log}, &builtins.ExportShopping{Store: st, CC: cc, Log: log})
		f.reminder = &builtins.Reminder{Store: st, CC: cc, Log: log}
		tools = append(tools, f.reminder)
	}
	if f.host != nil && f.host.Timers != nil {
		// A Pi's timers, volume and alerts: the host's services, on its speaker.
		tools = append(tools, &builtins.Timers{Service: f.host.Timers, Store: st},
			&builtins.ControlNode{Audio: f.host.Audio, Store: st},
			&builtins.CheckAlerts{Alerts: f.host.Alerts, CC: cc, Store: st})
	}
	if f.pi {
		// The Pi's model hand-offs, as the Python node had them; a computer's prompt doesn't.
		tools = append(tools, builtins.Talk{}, builtins.ActOnItems{})
	}
	tools = append(tools, &clock.Tool{})
	return tools, closeAll, nil
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
	tr := &launches.Tracker{Log: dt.Log}
	d.Log = dt.Log
	d.Confirm = dt.Confirm // closing every window of an app asks first (DESKTOP-COMMANDS.md Q8)
	if dt.Win != nil && runtime.GOOS == "darwin" {
		// The window list is AppleScript there, seconds on a busy Mac and behind the Automation
		// grant; the window server answers in milliseconds with no grant.
		tr.Owners = windowOwners(screen.MacWindows)
	} else if dt.Win != nil {
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

// windowOwners is the launch snapshot from macOS's window server: each app window's
// CoreGraphics number and its app's pid, the part of the window list's pid/index ids
// (dictate.SameMacPid) it can be matched by.
func windowOwners(list func() ([]screen.MacWindow, error)) func(context.Context) ([]launches.Owned, error) {
	return func(context.Context) ([]launches.Owned, error) {
		ws, err := list()
		if err != nil {
			return nil, err
		}
		out := []launches.Owned{}
		for _, w := range ws {
			// Layer 0 is apps' own windows; tiny ones are helpers, never the app's window.
			if w.Layer != 0 || w.PID <= 0 || w.W < 50 || w.H < 50 {
				continue
			}
			out = append(out, launches.Owned{ID: strconv.FormatUint(uint64(w.ID), 10), Owner: strconv.Itoa(w.PID)})
		}
		return out, nil
	}
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
	prof := profileFlag(fs)
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	p, err := thisProfile(*prof)
	if err != nil {
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
	line := staleBuildLine(version, sourceDir, gitHead, gitIsAncestor)
	if sourceDir == "" { // a plain go build: find its checkout from the binary or its source
		vcs, ok := buildinfo.Read()
		if ok && buildinfo.IsDev(version) {
			line = vcsStaleLine(version, vcs, ok, findCheckout(checkoutCandidates(), mainModule()), gitHead, gitIsAncestor, gitCommitTime)
		}
	}
	if line != "" {
		fmt.Println(line)
	}
	fmt.Printf("profile:      %s\n", p.name)
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
		listen := macax.ListenTrusted()
		fmt.Printf("input monitoring: %v\n", listen)
		if !listen {
			fmt.Print("  -> Recording a task in an app is OFF: allow \"Jarvis Automation Agent\" in System Settings > Privacy & Security >\n" +
				"     Input Monitoring (the setup page asks for it), then restart the agent.\n")
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
	if err := node.NewCC(ccURL, c).Heartbeat(ctx, node.Beat{Version: version, InstallMode: p.installMode, NodeKind: p.nodeKind, Busy: true}); err != nil {
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
	// Off on purpose: this command runs the one call typed on its command line, with no model
	// in the loop to be steered and nobody to answer a question.
	dt, err := dictationTools(h, dictate.ConfirmOff, nil)
	if err != nil {
		return err
	}
	origins := tryOrigins(ctx, *server)
	guard := &jarvispage.Guard{Origins: origins}
	dt.JarvisPage = guard.InFront
	opener := openThenType(desktop.New(h, desktop.OpenPolicy{AllowAny: *anyOpen}, appCatalogue(nil)), dt)
	opener.Views = views.Native(h)
	ts := node.Multi{opener, &screen.Tools{C: screen.Native()}, dt, &clock.Tool{}}
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

// addVerbs gives the runner more command verbs (the Pi's settings verbs, its voice-profile
// takes), keeping those it has.
func addVerbs(r *node.Runner, verbs map[string]func(context.Context, *node.CC, map[string]any)) {
	if r.Verbs == nil {
		r.Verbs = map[string]func(context.Context, *node.CC, map[string]any){}
	}
	maps.Copy(r.Verbs, verbs)
}
