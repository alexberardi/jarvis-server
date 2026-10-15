// jarvis-server-app is the executable of "Jarvis Automation Server.app" (01-no-cli-plan item
// 2.2), the macOS server installer: a small launcher beside the release's own signed jarvisd
// (Contents/MacOS/jarvisd). The logic stays in jarvisd (IQ3); this only asks and hands over.
//
//   - First open: a dialog ("runs in the background", a ticked "Let devices on my network reach
//     it" checkbox), the standard administrator password dialog, then
//     `<bundle>/jarvisd install-from-app --run-as <user> [--no-firewall]` as root, which puts
//     jarvisd in /usr/local/bin, registers the LaunchDaemon, opens the firewall for the LAN and
//     prints the setup link, which the app opens in the browser.
//   - Later opens: the admin (the setup link until setup is done). After an admin Stop it offers
//     Start; when this app carries a newer jarvisd than the installed one, Update.
//   - Option-open: Open, or Uninstall… (the service, the binary, and the data only if asked).
//
// Not SMAppService: a daemon registered from the bundle runs the bundle's binary, which the
// updater can't swap without breaking the bundle's signature (00-installers §8.3).
//
// --non-interactive (CI, MDM) answers every dialog itself, runs the root step through
// `sudo -n` and prints the URL instead of opening it. --check-ui builds the dialogs without
// showing them (the macOS CI checks the AppKit calls).
package main

import (
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jarvis-server-app:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// Finder passed -psn_… to apps on old macOS versions.
	if len(args) > 0 && strings.HasPrefix(args[0], "-psn_") {
		args = args[1:]
	}
	fl := flag.NewFlagSet("jarvis-server-app", flag.ContinueOnError)
	nonInteractive := fl.Bool("non-interactive", false, "answer the dialogs, use sudo -n, print the URL")
	noFirewall := fl.Bool("no-firewall", false, "with --non-interactive: don't open the firewall")
	uninstall := fl.Bool("uninstall", false, "with --non-interactive: uninstall")
	purge := fl.Bool("purge", false, "with --uninstall: also delete the data")
	checkUI := fl.Bool("check-ui", false, "build the dialogs without showing them, then exit")
	showVersion := fl.Bool("version", false, "print the version")
	if err := fl.Parse(args); err != nil {
		return err
	}
	switch {
	case *showVersion:
		fmt.Println(version)
		return nil
	case *checkUI:
		return checkDialogs()
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	u, err := user.Current()
	if err != nil {
		return err
	}
	if u.Uid == "0" {
		return fmt.Errorf("open %s as yourself, not as root: the server runs as the account that installs it", productName)
	}
	a := &app{
		bundled:   filepath.Join(filepath.Dir(exe), "jarvisd"),
		installed: "/usr/local/bin/jarvisd",
		plist:     "/Library/LaunchDaemons/" + service.LaunchdLabel + ".plist",
		user:      u.Username,
		exists:    func(p string) bool { _, err := os.Stat(p); return err == nil },
		run:       runUser,
		admin:     osascriptAdmin,
		open:      func(url string) error { return exec.Command("/usr/bin/open", url).Run() },
		dataSize:  dirSize,
		log:       os.Stderr,
	}
	if *nonInteractive {
		auto := autoDialogs{firewall: !*noFirewall, answers: []string{btnInstall, btnUpdate, btnStart, btnOK}}
		if *uninstall {
			a.manage = true
			drop := btnKeepData
			if *purge {
				drop = btnDropData
			}
			auto.answers = []string{btnUninstall, btnRemove, drop, btnOK}
		}
		a.ui = auto
		a.admin = sudoAdmin
		a.open = func(url string) error { _, err := fmt.Println(url); return err }
	} else {
		ui, err := nativeDialogs()
		if err != nil {
			return err
		}
		a.ui = ui
		a.manage = optionKeyDown()
	}
	return a.main(context.Background())
}

// autoDialogs answers each dialog with the first of its buttons named in answers (else the
// last, Cancel), and the install dialog with yes and its firewall choice.
type autoDialogs struct {
	firewall bool
	answers  []string
}

func (d autoDialogs) confirmInstall(title, _, _ string) (bool, bool) {
	fmt.Fprintf(os.Stderr, "[%s] %s, firewall %v\n", title, btnInstall, d.firewall)
	return true, d.firewall
}

func (d autoDialogs) choose(title, _ string, buttons ...string) int {
	for _, want := range d.answers {
		for i, b := range buttons {
			if b == want {
				fmt.Fprintf(os.Stderr, "[%s] %s\n", title, b)
				return i
			}
		}
	}
	fmt.Fprintf(os.Stderr, "[%s] %s\n", title, buttons[len(buttons)-1])
	return len(buttons) - 1
}

// dirSize is the total size of the files under dir (what a purge deletes).
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
