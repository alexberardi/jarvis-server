package main

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// setupLink is the admin wizard URL carrying the first-run setup token (AD2). The token is
// in the fragment, so it never reaches a server log or a Referer.
func setupLink(cfg config.Config, token string) string {
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = doctor.LANAddr()
	}
	if host == "" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Ports[config.ListenerAdmin])) + "/setup#token=" + token
}

// announceSetup shows the operator the setup token and link: plainly on w (stderr), since
// it is the one thing they must see on first start, and in the log. It opens a browser at
// the link when browser is set and this looks like an interactive desktop session.
func announceSetup(w io.Writer, log *slog.Logger, cfg config.Config, token, path string, browser bool) {
	link := setupLink(cfg, token)
	fmt.Fprintf(w, "\njarvisd: no admin account yet. Finish setup in a browser:\n\n    %s\n\n", link)
	fmt.Fprintf(w, "Setup token: %s\n", token)
	if path != "" {
		fmt.Fprintf(w, "(also in %s; it is deleted once setup is done)\n", path)
	}
	fmt.Fprintln(w)
	log.Info("first-run setup", "url", link, "token_file", path)
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
