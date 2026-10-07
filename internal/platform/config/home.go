package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultHomeName is the data directory's name under the user's home when nothing else picks
// one (ID1). It is not ".jarvis": the legacy Docker stack owns ~/.jarvis.
const DefaultHomeName = ".jarvisd"

// EnvFileName is the optional bootstrap env file jarvisd reads from its home (ID3).
const EnvFileName = "jarvisd.env"

// SystemEnvFile is the Linux system service's env file (00-installers §2.0). On unix it is
// read after <home>/jarvisd.env.
const SystemEnvFile = "/etc/jarvisd/jarvisd.env"

// ResolveHome picks the data directory, in order: the --home flag, JARVIS_HOME, the home of
// an installed jarvisd service (serviceHome, "" when none), then ~/.jarvisd. The result is
// absolute.
func ResolveHome(flag, serviceHome string, getenv func(string) string) (string, error) {
	h := flag
	if h == "" {
		h = getenv("JARVIS_HOME")
	}
	if h == "" {
		h = serviceHome
	}
	if h == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: no --home, no JARVIS_HOME and no home dir: %w", err)
		}
		h = filepath.Join(u, DefaultHomeName)
	}
	return filepath.Abs(h)
}

// EnvFiles lists the env files for a home, highest precedence first.
func EnvFiles(home string) []string {
	files := []string{filepath.Join(home, EnvFileName)}
	if runtime.GOOS != "windows" {
		files = append(files, SystemEnvFile)
	}
	return files
}

// LoadEnvFiles sets every variable from the files (highest precedence first) that is not
// already in the process environment: the environment always wins, then the first file that
// names a key. Missing files are skipped. JARVIS_HOME in a file is ignored, because the home
// is resolved before its env file can be read. It returns the keys it set.
func LoadEnvFiles(paths ...string) ([]string, error) {
	var set []string
	for _, p := range paths {
		f, err := os.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return set, fmt.Errorf("config: env file: %w", err)
		}
		kvs, err := ParseEnv(f)
		f.Close()
		if err != nil {
			return set, fmt.Errorf("config: %s: %w", p, err)
		}
		for _, kv := range kvs {
			if kv[0] == "JARVIS_HOME" {
				continue
			}
			if _, ok := os.LookupEnv(kv[0]); ok {
				continue
			}
			if err := os.Setenv(kv[0], kv[1]); err != nil {
				return set, fmt.Errorf("config: %s: %s: %w", p, kv[0], err)
			}
			set = append(set, kv[0])
		}
	}
	return set, nil
}

// ParseEnv reads KEY=VALUE lines. Blank lines and lines starting with # are skipped, an
// optional "export " prefix is dropped, and a value wrapped in matching single or double
// quotes is unwrapped (no escapes, no interpolation). The first occurrence of a key wins.
func ParseEnv(r io.Reader) ([][2]string, error) {
	var out [][2]string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\xef\xbb\xbf") // a BOM from Windows Notepad
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t\"'") {
			return nil, fmt.Errorf("line %d: want KEY=VALUE", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, [2]string{k, v})
		}
	}
	return out, sc.Err()
}

// EnvFileTemplate is what `jarvisd service install` writes as a fresh env file: comments
// only, since a fresh install needs no variables (00-installers §3).
const EnvFileTemplate = `# jarvisd bootstrap settings. jarvisd reads this file at start for any variable that is
# not already set in its environment. A fresh install needs none; runtime settings live in
# the admin UI. Restart after editing: jarvisd service restart
#
# Examples:
# JARVIS_LOG_LEVEL=debug
# JARVIS_PORT_ADMIN=7710
# JARVIS_MDNS=0
# JARVIS_MDNS_INTERFACES=eth0
`
