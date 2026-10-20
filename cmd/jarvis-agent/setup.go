package main

import (
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

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/deviceid"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/evdev"
	"github.com/alexberardi/jarvis-server/internal/agent/firstrun"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
	"github.com/alexberardi/jarvis-server/internal/agent/macax"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/portal"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/tray"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
	"github.com/alexberardi/jarvis-server/internal/platform/mdns"
)

// setupOptions are `jarvis-agent setup`'s flags.
type setupOptions struct {
	noOpen  bool // print the page's address instead of opening the browser
	noStart bool // don't start the agent once the computer is added (tests)
}

// setupCmd is `jarvis-agent setup`: the setup page (installers plan item 1.5), what `start`
// opens on a computer that isn't set up and the status icon's "Set up / Settings…" opens later.
func setupCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var o setupOptions
	fs.BoolVar(&o.noOpen, "no-open", false, "print the page's address instead of opening the browser")
	fs.BoolVar(&o.noStart, "no-start", false, "don't start the agent once this computer is added")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	return runSetup(ctx, o)
}

// firstRun is where a start with nothing set up goes: the setup page.
func firstRun(ctx context.Context) error { return runSetup(ctx, setupOptions{}) }

// setupSocketPath is the setup page's lock: one page per user. A second `setup` asks the one
// running to open its page again (control's show-running), rather than starting another.
func setupSocketPath() (string, error) {
	p, err := browser.ControlSocketPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "setup.sock"), nil
}

func runSetup(ctx context.Context, o setupOptions) error {
	log := logger()
	lockPath, err := setupSocketPath()
	if err != nil {
		return err
	}
	dial := func() (net.Conn, error) { return browser.Dial(lockPath) }
	lock, err := browser.Listen(lockPath)
	if errors.Is(err, browser.ErrInUse) {
		// The page is already open somewhere: show it again.
		return control.Send(dial, control.ShowRunning)
	}
	if err != nil {
		log.Warn("setup page: no lock; a second launch may open a second page", "err", err)
	}
	h, err := nativeHost()
	if err != nil {
		return err
	}
	srv, err := firstrun.New(setupDeps(log, o))
	if err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	open := func() {
		if o.noOpen {
			fmt.Println("Setup page:", srv.URL())
			return
		}
		octx, ocancel := context.WithTimeout(ctx, 15*time.Second)
		defer ocancel()
		if err := h.Open(octx, srv.URL()); err != nil {
			// Never log the address: it carries the page's secret.
			fmt.Fprintf(os.Stderr, "Couldn't open your browser (%v). Open this address in it:\n%s\n", err, srv.URL())
		}
	}
	if lock != nil {
		go func() {
			_ = control.Serve(ctx, lock, func(cmd string) error {
				switch cmd {
				case control.ShowRunning:
					open()
				case control.Quit:
					cancel()
				}
				return nil
			})
		}()
	}
	open()
	why := srv.Serve(ctx)
	log.Info("setup page closed", "why", why)
	return nil
}

// openSetupPage is the status icon's "Set up / Settings…": `jarvis-agent setup` in a process
// of its own (it serves the page until it is closed; a second one only reopens the first).
func openSetupPage(log *slog.Logger) func() {
	return func() {
		exe, err := agentExe()
		if err != nil {
			log.Warn("setup page: can't find this program", "err", err)
			return
		}
		s, err := installSys(io.Discard)
		if err != nil {
			log.Warn("setup page", "err", err)
			return
		}
		if err := s.Spawn(exe, "setup"); err != nil {
			log.Warn("setup page: couldn't start it", "err", err)
		}
	}
}

// setupDeps is the page's view of this computer.
func setupDeps(log *slog.Logger, o setupOptions) firstrun.Deps {
	name, _ := os.Hostname()
	name = mdns.HostLabel(name)
	d := firstrun.Deps{
		Log:          log,
		GOOS:         runtime.GOOS,
		ComputerName: name,
		Identity:     node.Registration{DeviceID: deviceid.Get(), NodeKind: desktopProfile.nodeKind}, // the setup page is a computer's
		Icon:         tray.IconPNG(),
		Creds: func() (node.Creds, bool) {
			p, err := node.DefaultCredsPath()
			if err != nil {
				return node.Creds{}, false
			}
			c, err := node.LoadCreds(p)
			return c, err == nil
		},
		SaveCreds: func(c node.Creds) error {
			p, err := node.DefaultCredsPath()
			if err != nil {
				return err
			}
			return node.SaveCreds(p, c)
		},
		AfterJoin: func(ctx context.Context) error { return afterJoin(ctx, log, o.noStart) },
		Browse:    firstrun.BrowseMDNS,
		OpenURL: func(ctx context.Context, u string) error {
			h, err := nativeHost()
			if err != nil {
				return err
			}
			return h.Open(ctx, u)
		},
		Remove:     removeAgent,
		RemoveNote: removeNote(),
	}
	if voice.Supported {
		d.Voice = &firstrun.Voice{On: voiceSetting, Set: setVoice}
		d.Mic = func() (device.Capture, error) { return device.OpenCapture(micName(), 16000, 1) }
	}
	if runtime.GOOS == "darwin" && macax.Supported() {
		d.Permissions = macPermissions()
	} else {
		d.Permissions = linuxPermissions(runtime.GOOS, (&dictate.LinuxWindows{X: dictate.OSExec{}}).Compositor(), inputAccess)
	}
	if p := typingPortal(); p != nil {
		d.Portal = p
	}
	return d
}

// afterJoin: the browsers' native host (registered by default, DESIGN §13), then the agent
// started as `start` would, with its pill.
func afterJoin(ctx context.Context, log *slog.Logger, noStart bool) error {
	if exe, err := agentExe(); err == nil {
		if benv, err := browserEnv(); err == nil {
			if err := (browserHosts{benv}).Register(exe); err != nil {
				log.Warn("setup page: browser control's native host", "err", err)
			}
		}
	}
	if noStart {
		return nil
	}
	e, err := installEnv()
	if err != nil {
		return err
	}
	s, err := installSys(io.Discard)
	if err != nil {
		return err
	}
	return newStarter(e, s).start(ctx, false)
}

// setVoice saves the wake-word choice and tells the running agent (the status icon's toggle).
func setVoice(on bool) error {
	if err := saveVoice("", on); err != nil {
		return err
	}
	cmd := control.VoiceOff
	if on {
		cmd = control.VoiceOn
	}
	if err := control.Send(controlDial, cmd); err != nil && !errors.Is(err, control.ErrNotRunning) {
		// Saved: the agent picks it up when it starts.
		logger().Info("setup page: the running agent didn't take the voice change", "err", err)
	}
	return nil
}

// micName is the microphone listening uses (agent.json voice_input), "default" otherwise.
func micName() string {
	if p, err := settings.DefaultPath(); err == nil {
		if s, _, err := settings.Load(p); err == nil && s.VoiceInput != nil {
			return *s.VoiceInput
		}
	}
	return "default"
}

// removeAgent is `jarvis-agent uninstall --purge`: the login item, menu entries, link handler,
// browser host, the running agent and its sign-in and settings.
func removeAgent(ctx context.Context) error {
	e, err := installEnv()
	if err != nil {
		return err
	}
	ops, err := install.UninstallPlan(e, install.UninstallOptions{Purge: true})
	if err != nil {
		return err
	}
	s, err := installSys(io.Discard)
	if err != nil {
		return err
	}
	return install.Apply(ctx, ops, s, install.ApplyOptions{BestEffort: true})
}

func removeNote() string {
	switch runtime.GOOS {
	case "darwin":
		return "To finish, drag " + appid.DisplayName + " from Applications to the Trash."
	case "windows":
		return "To remove the program too, use Settings, Apps."
	}
	return ""
}

// --- macOS permissions ---

// The System Settings panes (Privacy & Security).
const (
	paneAccessibility = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
	paneMicrophone    = "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone"
	paneAutomation    = "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation"
	paneScreen        = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
	paneInput         = "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent"
	systemEventsID    = "com.apple.systemevents"
)

// macPermissions are the five grants the agent uses, each with a live status and what asks
// for it. macOS only.
func macPermissions() []firstrun.Permission {
	open := func(pane string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "open", pane).Run()
	}
	boolStatus := func(ok bool) string {
		if ok {
			return firstrun.Granted
		}
		return firstrun.NotAsked // macOS doesn't say whether it was refused: ask, then the pane
	}
	return []firstrun.Permission{
		{ID: "accessibility", Label: "Accessibility", Pane: paneAccessibility,
			Why:    "So Jarvis can type what you dictate, and focus, minimise or close windows.",
			Status: func() string { return boolStatus(macax.Trusted()) },
			Ask:    func() { macax.Prompt(); open(paneAccessibility) }},
		{ID: "microphone", Label: "Microphone", Pane: paneMicrophone,
			Why:    `So Jarvis can hear "Hey Jarvis" and what you say next.`,
			Status: macax.MicStatus,
			Ask: func() {
				// Opening the microphone is what makes macOS ask.
				if c, err := device.OpenCapture("default", 16000, 1); err == nil {
					buf := make([]int16, 160)
					_ = c.Read(buf)
					_ = c.Close()
				}
			}},
		{ID: "automation", Label: "Automation (System Events)", Pane: paneAutomation,
			Why:    "So Jarvis can see which windows are open and switch between them.",
			Status: func() string { return macax.AutomationStatus(systemEventsID) },
			Ask: func() {
				// The same Apple event the window tools send: macOS asks the first time.
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				_ = exec.CommandContext(ctx, "osascript", "-e", `tell application id "`+systemEventsID+`" to count processes`).Run()
			}},
		{ID: "screen", Label: "Screen Recording", Pane: paneScreen,
			Why: "So Jarvis can read what's on your screen when you ask it to.",
			// A process sees a new Screen Recording grant only after it restarts: ask a fresh one.
			Status: func() string { return boolStatus(freshStatus("screen")) },
			Ask:    func() { macax.ScreenPrompt(); open(paneScreen) }},
		{ID: "input-monitoring", Label: "Input Monitoring", Pane: paneInput,
			Why: "So Jarvis can watch what you do in an app when you ask it to learn a task (never what you type into a password field).",
			// Like Screen Recording, the grant applies to a fresh process.
			Status: func() string { return boolStatus(freshStatus("input-monitoring")) },
			Ask:    func() { macax.ListenPrompt(); open(paneInput) }},
	}
}

// inputGroupCmd adds the user to the input group: they run it themselves (the agent never
// asks for sudo), then log out and back in. computer.ErrInputGroup says the same.
const inputGroupCmd = "sudo usermod -aG input $USER"

// linuxPermissions is the Linux step, on Hyprland and sway (where the desktop can be recorded):
// the input group, Linux's Input Monitoring, granted once access says the keyboard opens.
func linuxPermissions(goos, compositor string, access func() error) []firstrun.Permission {
	if goos != "linux" || compositor == "" {
		return nil
	}
	return []firstrun.Permission{{ID: "input-monitoring", Label: "Keyboard and mouse (the input group)",
		Why: "So Jarvis can watch what you do on the desktop when you ask it to learn a task (never what you type " +
			"into a password field it can see). Run the command in a terminal, then log out and back in.",
		How: inputGroupCmd,
		Status: func() string {
			if access() == nil {
				return firstrun.Granted
			}
			return firstrun.NotAsked
		}}}
}

// inputAccess is nil when this process can read a keyboard under /dev/input.
func inputAccess() error {
	devs, err := evdev.Devices(evdev.SysRoot, evdev.DevRoot)
	if err != nil {
		return err
	}
	return evdev.Access(devs)
}

// inputMonitoring: macOS's Input Monitoring grant, Linux's input group.
func inputMonitoring() bool {
	if runtime.GOOS == "linux" {
		return inputAccess() == nil
	}
	return macax.ListenTrusted()
}

// freshChecks are the grants macOS shows a process only after it restarts, which
// `jarvis-agent permission <name>` checks in a fresh one (on Linux input-monitoring is the
// input group, which a login applies).
var freshChecks = map[string]func() bool{"screen": macax.ScreenTrusted, "input-monitoring": inputMonitoring}

// freshStatus asks a new process of this binary (`jarvis-agent permission <name>`), which
// macOS answers with the current grant; this process's own answer is fixed at its start.
func freshStatus(name string) bool {
	exe, err := agentExe()
	if err != nil {
		return freshChecks[name]()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "permission", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == firstrun.Granted
}

// permissionCmd is `jarvis-agent permission screen|input-monitoring`: the setup page's fresh
// Screen Recording or Input Monitoring check (prints granted or not_asked; on Linux,
// input-monitoring is whether the input group lets it read the keyboard, and not_asked also
// prints the command that adds it). Hidden: not in the usage.
func permissionCmd(args []string, w io.Writer) error {
	var check func() bool
	if len(args) == 1 {
		check = freshChecks[args[0]]
	}
	if check == nil {
		return errors.New("usage: jarvis-agent permission screen|input-monitoring")
	}
	if check() {
		fmt.Fprintln(w, firstrun.Granted)
		return nil
	}
	fmt.Fprintln(w, firstrun.NotAsked)
	if runtime.GOOS == "linux" && args[0] == "input-monitoring" {
		fmt.Fprintf(w, "Recording the desktop needs to read the keyboard and mouse: run `%s`, then log out and back in.\n", inputGroupCmd)
	}
	return nil
}

// --- GNOME/KDE typing ---

// typingPortal is the "Allow typing" step on GNOME and KDE on Wayland, the desktops whose only
// way to type is the RemoteDesktop portal (DESIGN.md §14); nil elsewhere.
func typingPortal() *firstrun.Portal {
	if !needsTypingPortal(runtime.GOOS, os.Getenv) {
		return nil
	}
	_, _ = portal.MigrateLegacy(os.Getenv, nil)
	rd := &portal.RemoteDesktop{}
	return &firstrun.Portal{
		Status: func(ctx context.Context) (bool, bool, string) {
			st := rd.Probe(ctx)
			return st.Allowed, st.Portal, st.Why
		},
		Allow: func(ctx context.Context) error {
			// Before the portal connection: it registers as the agent's app id only if this exists.
			_ = desktopFile{}.Write()
			return rd.Allow(ctx)
		},
	}
}

// needsTypingPortal: Linux, a Wayland session, GNOME or KDE.
func needsTypingPortal(goos string, getenv func(string) string) bool {
	if goos != "linux" {
		return false
	}
	if getenv("XDG_SESSION_TYPE") != "wayland" && getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	d := strings.ToLower(getenv("XDG_CURRENT_DESKTOP"))
	return strings.Contains(d, "gnome") || strings.Contains(d, "kde")
}
