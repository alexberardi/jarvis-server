package apps

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is the slice of the environment default sources depend on, so tests can point them at a
// fake home on any OS.
type Env struct {
	GOOS   string
	Home   string
	Getenv func(string) string
}

// DefaultSources are the platform's app locations: "anything in your apps folder".
func DefaultSources(e Env) []Source {
	get := e.Getenv
	if get == nil {
		get = func(string) string { return "" }
	}
	j := filepath.Join
	switch e.GOOS {
	case "darwin":
		return []Source{
			{j("/Applications", "**", "*.app"), MacApp},
			{j("/System/Applications", "**", "*.app"), MacApp},
			{j(e.Home, "Applications", "**", "*.app"), MacApp},
		}
	case "windows":
		var out []Source
		// Per-user first, so a user's own shortcut wins over the all-users one of the same name.
		if a := get("APPDATA"); a != "" {
			out = append(out, Source{j(a, "Microsoft", "Windows", "Start Menu", "Programs", "**", "*.lnk"), Shortcut})
		}
		if p := get("ProgramData"); p != "" {
			out = append(out, Source{j(p, "Microsoft", "Windows", "Start Menu", "Programs", "**", "*.lnk"), Shortcut})
		}
		return out
	default: // linux and other freedesktop systems
		dataHome := get("XDG_DATA_HOME")
		if dataHome == "" {
			dataHome = j(e.Home, ".local", "share")
		}
		dirs := []string{dataHome}
		dataDirs := get("XDG_DATA_DIRS")
		if dataDirs == "" {
			dataDirs = "/usr/local/share:/usr/share"
		}
		// XDG uses ':'; filepath.SplitList is ':' on Unix (and ';' on Windows, where only the
		// cross-OS tests run this branch, with Windows paths that contain ':').
		for _, d := range filepath.SplitList(dataDirs) {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
		var out []Source
		seen := map[string]bool{}
		for _, d := range dirs {
			if seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, Source{j(d, "applications", "**", "*.desktop"), DesktopFile})
		}
		return out
	}
}

// DefaultDeny keeps uninstallers out of the catalogue: a misheard "open" must not start one.
var DefaultDeny = []string{"*uninstall*"}

// Config is the user-edited apps.json next to the node credentials:
//
//	{
//	  "allow": ["~/Games/**/*.app", "/opt/tools/*.desktop"],
//	  "deny":  ["Terminal", "*Activity Monitor*", "/Applications/Utilities/**"]
//	}
//
// "allow" adds glob patterns of app paths (the platform's kind: .app bundles, .desktop files or
// .lnk shortcuts) to the defaults. "deny" removes apps whose name or path matches a glob.
// Matching is case-insensitive; "**" spans directories; "~/" is the home directory.
type Config struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// ConfigPath is <user config dir>/jarvis-agent/apps.json.
func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "jarvis-agent", "apps.json"), nil
}

// LoadConfig reads the config; a missing file is an empty config.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func kindFor(goos string) Kind {
	switch goos {
	case "darwin":
		return MacApp
	case "windows":
		return Shortcut
	}
	return DesktopFile
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// Build is the effective catalogue: config allow patterns first (so they win on a name clash),
// then the platform defaults, minus the default and configured deny lists.
func Build(e Env, c Config) Catalog {
	var srcs []Source
	for _, a := range c.Allow {
		srcs = append(srcs, Source{expandHome(a, e.Home), kindFor(e.GOOS)})
	}
	srcs = append(srcs, DefaultSources(e)...)
	deny := append([]string{}, DefaultDeny...)
	for _, d := range c.Deny {
		deny = append(deny, expandHome(d, e.Home))
	}
	return Catalog{Sources: srcs, Deny: deny}
}
