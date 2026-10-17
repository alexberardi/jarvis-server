package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/qrterm"
)

// setupLink is the admin wizard URL carrying the first-run setup token (AD2). The token is
// in the fragment, so it never reaches a server log or a Referer.
func setupLink(cfg config.Config, token string) string {
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = doctor.LANAddr(cfg.MDNSInterfaces)
	}
	if host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Ports[config.ListenerAdmin])) + "/setup#token=" + token
}

// setupLinkOptions are `jarvisd setup-link`'s flags.
type setupLinkOptions struct {
	urlOnly bool         // --url: print only the URL, for installers (machine output)
	qr      bool         // --qr: also draw the URL as a QR code
	qrStyle qrterm.Style // how to draw it (qrStyleFor's choice)
}

// parseSetupLinkArgs reads `jarvisd setup-link [--url] [--qr]`.
func parseSetupLinkArgs(args []string, stderr io.Writer) (setupLinkOptions, error) {
	fs := flag.NewFlagSet("setup-link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	urlOnly := fs.Bool("url", false, "print only the URL (the setup link, or the admin URL once set up)")
	qr := fs.Bool("qr", false, "also print the URL as a QR code, to open it on a phone")
	if err := fs.Parse(args); err != nil {
		return setupLinkOptions{}, err
	}
	if fs.NArg() > 0 {
		return setupLinkOptions{}, fmt.Errorf("setup-link: unexpected argument %q", fs.Arg(0))
	}
	return setupLinkOptions{urlOnly: *urlOnly, qr: *qr}, nil
}

// printSetupLink is `jarvisd setup-link`: the install scripts' last word. While no admin
// account exists it prints the setup link and token from <home>/setup-token (written by the
// running service, 0600, so the scripts run it with sudo for a system service); afterwards
// the admin URL. --url prints the URL alone (installers open it); --qr adds a QR code of it.
func printSetupLink(cfg config.Config, w io.Writer, opt setupLinkOptions) error {
	url, msg, err := setupLinkMessage(cfg)
	if err != nil {
		return err
	}
	if opt.urlOnly {
		fmt.Fprintln(w, url)
	} else {
		fmt.Fprint(w, msg)
	}
	if opt.qr {
		if !opt.urlOnly {
			fmt.Fprintln(w, "\nOr scan this with a phone on the same network:")
		}
		fmt.Fprintln(w)
		if err := qrterm.Write(w, url, opt.qrStyle); err != nil {
			return err
		}
	}
	return nil
}

// qrStyleFor picks how to draw the QR code on w: black on white with escape codes on a
// terminal (it scans on light and dark themes alike), else plain dark-on-light blocks (a log
// or a file). NO_COLOR, or a Windows console that can't take escape codes, gets light-on-dark
// blocks, since terminals are mostly dark.
func qrStyleFor(w io.Writer) qrterm.Style {
	f, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return qrterm.DarkOnLight
	}
	if os.Getenv("NO_COLOR") != "" || !enableANSI(f) {
		return qrterm.LightOnDark
	}
	return qrterm.ANSI
}

// setupLinkMessage is the URL to open and the text explaining it.
func setupLinkMessage(cfg config.Config) (url, msg string, err error) {
	b, err := os.ReadFile(authmod.SetupTokenPath(cfg.Home))
	if tok := strings.TrimSpace(string(b)); err == nil && tok != "" {
		link := setupLink(cfg, tok)
		return link, fmt.Sprintf("Finish setup in a browser:\n\n    %s\n\nSetup token: %s\n", link, tok), nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", "", err
	}
	link := strings.TrimSuffix(setupLink(cfg, ""), "setup#token=")
	// No token: setup is done, or jarvisd hasn't started (it writes the token when it starts
	// without an admin account). Its /health tells which.
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	health := "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Ports[config.ListenerConfig])) + "/health"
	if probeHealth(context.Background(), health) == "" {
		if wizardUnfinished(cfg.DBPath()) {
			// An admin account exists (an imported one, or the wizard was left after Account),
			// but the wizard's later steps never ran.
			return link, fmt.Sprintf("Finish setup in a browser: sign in at %s with your admin account; the wizard "+
				"continues with hardware, models and privacy choices.\n", link), nil
		}
		return link, fmt.Sprintf("jarvisd is set up. The admin is at %s\n", link), nil
	}
	return link, fmt.Sprintf("jarvisd isn't answering yet; once it is, `jarvisd setup-link` prints the setup link if setup "+
		"is still to do. The admin is at %s\n", link), nil
}

// announceSetup shows the operator the setup token and link: plainly on w (stderr), since
// it is the one thing they must see on first start. The log gets the link without the token
// (it reaches journald, launchd and the logs DB, which others may read). It opens a browser at
// the link when browser is set and this looks like an interactive desktop session.
func announceSetup(w io.Writer, log *slog.Logger, cfg config.Config, token, path string, browser bool) {
	link := setupLink(cfg, token)
	fmt.Fprintf(w, "\njarvisd: no admin account yet. Finish setup in a browser:\n\n    %s\n\n", link)
	fmt.Fprintf(w, "Setup token: %s\n", token)
	if path != "" {
		fmt.Fprintf(w, "(also in %s; it is deleted once setup is done)\n", path)
	}
	fmt.Fprintln(w)
	log.Info("first-run setup", "url", strings.TrimSuffix(setupLink(cfg, ""), "#token="), "token_file", path)
	if browser && interactiveDesktop(os.Getenv, os.Stderr) {
		if err := openBrowser(link); err != nil {
			log.Debug("could not open a browser", "err", err)
		}
	}
}

// interactiveDesktop reports whether jarvisd was started by a person at a desktop: stderr is
// a terminal, not a service manager (systemd sets INVOCATION_ID; launchd and Windows
// services have no terminal), not over SSH, and on Linux/BSD a display is available.
func interactiveDesktop(getenv func(string) string, stderr *os.File) bool {
	if getenv("INVOCATION_ID") != "" || getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return false
	}
	fi, err := stderr.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	default:
		return getenv("DISPLAY") != "" || getenv("WAYLAND_DISPLAY") != ""
	}
}

// openBrowser starts the platform's URL opener without waiting for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
