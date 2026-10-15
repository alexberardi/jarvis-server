package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// The product's name in every dialog (naming rule: "Jarvis Automation" for the apps).
const productName = "Jarvis Automation Server"

// Button labels. The non-interactive mode answers by label, so the flow below is the one CI runs.
const (
	btnInstall   = "Install"
	btnCancel    = "Cancel"
	btnOpen      = "Open " + productName
	btnStart     = "Start"
	btnUninstall = "Uninstall…"
	btnRemove    = "Uninstall"
	btnKeepData  = "Keep data"
	btnDropData  = "Delete data"
	btnUpdate    = "Update"
	btnNotNow    = "Not now"
	btnOK        = "OK"
)

// errCanceled: the person closed the administrator password dialog.
var errCanceled = errors.New("canceled")

// dialogs is what the app shows. choose returns the index of the button pressed (the first is
// the default, Return); confirmInstall is the first-open dialog with its firewall checkbox.
type dialogs interface {
	confirmInstall(title, text, checkbox string) (install, firewall bool)
	choose(title, text string, buttons ...string) int
}

// serviceState is what `jarvisd service status --json` says (the fields the app reads).
type serviceState struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Healthy   bool   `json:"healthy"`
	Home      string `json:"home"`
}

// app is the server app's decisions, with everything it touches swappable for tests.
type app struct {
	bundled   string // the jarvisd inside this app (Contents/MacOS/jarvisd)
	installed string // where the service runs it from (/usr/local/bin/jarvisd)
	user      string // the account the LaunchDaemon runs as: whoever opened the app
	manage    bool   // Option held at open: the Open / Uninstall menu

	ui dialogs
	// exists reports whether a path exists (the LaunchDaemon plist, the installed binary).
	exists func(path string) bool
	plist  string
	// run runs a program as this user and returns its stdout (stdout is returned with err too).
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// admin runs a /bin/sh script as root: the standard administrator password dialog (osascript)
	// or, non-interactively, sudo -n. stdout comes back; errCanceled when the dialog was closed.
	admin    func(ctx context.Context, prompt, script string) (string, error)
	open     func(url string) error
	dataSize func(home string) int64
	log      io.Writer
}

// main is one open of the app: install on the first, then open the admin (offering Start after
// an admin Stop, an update when this app carries a newer jarvisd, and Uninstall with Option).
func (a *app) main(ctx context.Context) error {
	if !a.exists(a.plist) || !a.exists(a.installed) {
		return a.install(ctx)
	}
	st := a.status(ctx)
	if a.manage {
		switch a.ui.choose(productName, statusLine(st), btnOpen, btnUninstall, btnCancel) {
		case 0:
		case 1:
			return a.uninstall(ctx, st)
		default:
			return nil
		}
	}
	if !st.Running {
		switch a.ui.choose(productName+" is stopped",
			"It was stopped from the admin page, or it couldn't start. Start it again?",
			btnStart, btnUninstall, btnCancel) {
		case 0:
			_, err := a.admin(ctx, productName+" wants to start.", shellJoin(a.installed, "service", "start"))
			if errors.Is(err, errCanceled) {
				return nil
			}
			if err != nil {
				return a.report("couldn't start", err)
			}
			if _, err := a.run(ctx, a.installed, "service", "status", "--wait", "1m"); err != nil {
				return a.report("didn't start", err)
			}
			st = a.status(ctx)
		case 1:
			return a.uninstall(ctx, st)
		default:
			return nil
		}
	} else if have, next := a.version(ctx, a.installed), a.version(ctx, a.bundled); newer(next, have) {
		if a.ui.choose("Update "+productName+"?",
			fmt.Sprintf("This Mac runs version %s. This app has %s.", have, next), btnUpdate, btnNotNow) == 0 {
			// --no-firewall: an update doesn't change what the person chose at install.
			return a.installFromApp(ctx, "update", "--no-firewall")
		}
	}
	return a.openAdmin(ctx, st.Home)
}

// install is the first open: the dialog, the password, install-from-app, then the setup link.
func (a *app) install(ctx context.Context) error {
	ok, firewall := a.ui.confirmInstall(productName,
		productName+" runs in the background on this Mac, so your phones, voice nodes and other "+
			"computers can use it. Installing it asks for an administrator password.",
		"Let devices on my network reach it")
	if !ok {
		return nil
	}
	var extra []string
	if !firewall {
		extra = append(extra, "--no-firewall")
	}
	return a.installFromApp(ctx, "install", extra...)
}

// installFromApp runs the bundled jarvisd's install-from-app as root and opens the URL it prints
// (the setup link on a fresh install, the admin once set up).
func (a *app) installFromApp(ctx context.Context, what string, extra ...string) error {
	argv := append([]string{a.bundled, "install-from-app", "--run-as", a.user}, extra...)
	out, err := a.admin(ctx, productName+" wants to "+what+" its background service.", shellJoin(argv...))
	if errors.Is(err, errCanceled) {
		return nil
	}
	if err != nil {
		return a.report("wasn't installed", err)
	}
	url := lastLine(out)
	if !strings.HasPrefix(url, "http") {
		return a.report("wasn't installed", fmt.Errorf("the installer printed no address to open (%q)", out))
	}
	return a.open(url)
}

// uninstall stops and removes the service and the binary, asking whether to delete the data
// (default: keep it, decided Q6). The app itself stays: the person drags it to the Trash.
func (a *app) uninstall(ctx context.Context, st serviceState) error {
	if a.ui.choose("Uninstall "+productName+"?",
		"This stops the server and removes it from this Mac. Phones, voice nodes and desktop agents "+
			"that use it stop working until you install it again.", btnRemove, btnCancel) != 0 {
		return nil
	}
	purge := false
	if st.Home != "" {
		text := "Your accounts, settings, history and downloaded models are in " + st.Home
		if n := a.dataSize(st.Home); n > 0 {
			text += " (" + humanBytes(n) + ")"
		}
		text += ". Keep them to reinstall later without setting up again."
		purge = a.ui.choose("Also delete "+productName+"'s data?", text, btnKeepData, btnDropData) == 1
	}
	args := []string{a.installed, "service", "uninstall"}
	if purge {
		args = append(args, "--purge", "--yes")
	}
	// As install.sh --uninstall: the service (and the firewall rules it added), then the binary
	// with what upgrades leave beside it.
	script := shellJoin(args...) + " && " + shellJoin("rm", "-f", a.installed, a.installed+".prev",
		a.installed+".rolledback", a.installed+".new") + " && " + shellJoin("rm", "-rf", "/usr/local/lib/jarvisd")
	_, err := a.admin(ctx, productName+" wants to uninstall.", script)
	if errors.Is(err, errCanceled) {
		return nil
	}
	if err != nil {
		return a.report("wasn't uninstalled", err)
	}
	a.ui.choose(productName+" is uninstalled", "You can move this app to the Trash.", btnOK)
	return nil
}

// openAdmin opens the setup link while setup isn't done, the admin page after.
func (a *app) openAdmin(ctx context.Context, home string) error {
	args := []string{"setup-link", "--url"}
	if home != "" {
		args = append([]string{"--home", home}, args...)
	}
	out, err := a.run(ctx, a.installed, args...)
	url := lastLine(string(out))
	if err != nil || !strings.HasPrefix(url, "http") {
		return a.report("can't find its address", fmt.Errorf("jarvisd setup-link: %v %s", err, strings.TrimSpace(string(out))))
	}
	return a.open(url)
}

// status reads the installed service's state; a failure reads as stopped.
func (a *app) status(ctx context.Context) serviceState {
	var st serviceState
	out, _ := a.run(ctx, a.installed, "service", "status", "--json") // non-zero while not healthy
	if err := json.Unmarshal(out, &st); err != nil {
		fmt.Fprintf(a.log, "service status: %v\n", err)
	}
	return st
}

func (a *app) version(ctx context.Context, bin string) string {
	out, err := a.run(ctx, bin, "version")
	if err != nil {
		return ""
	}
	return lastLine(string(out))
}

// report shows an error and returns it (main's exit status).
func (a *app) report(what string, err error) error {
	a.ui.choose(productName+" "+what, errorText(err), btnOK)
	return fmt.Errorf("%s %s: %w", productName, what, err)
}

// newer reports whether version next is newer than have (both release versions).
func newer(next, have string) bool {
	n, ok1 := update.ParseVersion(next)
	h, ok2 := update.ParseVersion(have)
	return ok1 && ok2 && n.Compare(h) > 0
}

func statusLine(st serviceState) string {
	switch {
	case st.Running && st.Healthy:
		return "It is running."
	case st.Running:
		return "It is starting, or not answering."
	default:
		return "It is stopped."
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r", "\n")), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// errorText keeps a dialog readable: the last few lines of what failed.
func errorText(err error) string {
	lines := strings.Split(strings.TrimSpace(err.Error()), "\n")
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit*unit {
		return fmt.Sprintf("%d KB", (n+unit-1)/unit)
	}
	if n < unit*unit*unit {
		return fmt.Sprintf("%.0f MB", float64(n)/(unit*unit))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(unit*unit*unit))
}
