package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

// takeHome removes --home DIR (or --home=DIR) from anywhere in args and returns its value.
func takeHome(args []string) (string, []string) {
	var home string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--home" || a == "-home":
			if i+1 < len(args) {
				home = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--home="):
			home = strings.TrimPrefix(a, "--home=")
		case strings.HasPrefix(a, "-home="):
			home = strings.TrimPrefix(a, "-home=")
		default:
			rest = append(rest, a)
		}
	}
	return home, rest
}

// bootstrap resolves the data directory (--home, JARVIS_HOME, the installed service's, else
// ~/.jarvisd), exports it as JARVIS_HOME, and loads <home>/jarvisd.env and
// /etc/jarvisd/jarvisd.env for variables the environment doesn't set (ID3). An unreadable
// env file is fatal when strict (serve) and a warning otherwise: `jarvisd doctor` run by a
// user can't read the system service's 0640 file and still wants the default ports.
func bootstrap(flagHome string, strict bool, stderr io.Writer) error {
	var svcHome string
	if flagHome == "" && os.Getenv("JARVIS_HOME") == "" {
		svcHome = service.InstalledHome()
	}
	home, err := config.ResolveHome(flagHome, svcHome, os.Getenv)
	if err != nil {
		return err
	}
	if err := os.Setenv("JARVIS_HOME", home); err != nil {
		return err
	}
	if _, err := config.LoadEnvFiles(config.EnvFiles(home)...); err != nil {
		if strict {
			return err
		}
		fmt.Fprintf(stderr, "jarvisd: warning: %v (using the defaults for its variables)\n", err)
	}
	return nil
}

// secureHome makes everything jarvisd creates owner-only (umask 077 on unix) and the data
// directory 0700, whatever made it before (00-installers §2.0). db.Open keeps the DB files
// 0600. Windows relies on the directory's ACL instead.
func secureHome(home string) error {
	restrictUmask()
	if err := os.MkdirAll(home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", home, err)
	}
	return os.Chmod(home, 0o700)
}
