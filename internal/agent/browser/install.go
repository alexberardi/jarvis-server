package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Env is where a user's browsers keep their native-messaging registrations.
type Env struct {
	GOOS    string
	Home    string
	DataDir string // the agent's own dir (<user config dir>/jarvis-agent): Windows manifests, extension copies
	Getenv  func(string) string
}

func (e Env) getenv(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

func (e Env) xdgConfig() string {
	if d := e.getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	return filepath.Join(e.Home, ".config")
}

// Target is one browser (or one browser's registration location).
type Target struct {
	Name        string   // stable id: firefox, chrome, chromium, edge, brave, vivaldi, arc
	Label       string   // for people
	Family      string   // TargetFirefox or TargetChromium (which extension and manifest shape)
	Detect      []string // the browser counts as present if any of these exists
	ManifestDir string   // where the host manifest goes (Linux, macOS)
	RegistryKey string   // HKCU key whose default value names the manifest (Windows)
}

// Targets lists every browser location this OS supports. Linux and macOS register by writing
// the host manifest into the browser's per-user NativeMessagingHosts directory; Windows by a
// per-user (HKCU) registry key pointing at a manifest in the agent's data dir. Nothing here
// needs admin rights or touches browser profiles or policies.
func Targets(e Env) []Target {
	cfg := e.xdgConfig()
	chromium := func(name, label, root string) Target {
		return Target{Name: name, Label: label, Family: TargetChromium, Detect: []string{root},
			ManifestDir: filepath.Join(root, "NativeMessagingHosts")}
	}
	switch e.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		return []Target{
			// Firefox reads only ~/.mozilla/native-messaging-hosts, also when its profiles live
			// in the XDG dir ($XDG_CONFIG_HOME/mozilla, newer installs): verified on Firefox 155,
			// which ignores $XDG_CONFIG_HOME/mozilla/native-messaging-hosts.
			{Name: "firefox", Label: "Firefox", Family: TargetFirefox,
				Detect: []string{filepath.Join(e.Home, ".mozilla"), filepath.Join(cfg, "mozilla"),
					filepath.Join(e.Home, "snap", "firefox")},
				ManifestDir: filepath.Join(e.Home, ".mozilla", "native-messaging-hosts")},
			chromium("chrome", "Google Chrome", filepath.Join(cfg, "google-chrome")),
			chromium("chromium", "Chromium", filepath.Join(cfg, "chromium")),
			chromium("edge", "Microsoft Edge", filepath.Join(cfg, "microsoft-edge")),
			chromium("brave", "Brave", filepath.Join(cfg, "BraveSoftware", "Brave-Browser")),
			chromium("vivaldi", "Vivaldi", filepath.Join(cfg, "vivaldi")),
		}
	case "darwin":
		as := filepath.Join(e.Home, "Library", "Application Support")
		return []Target{
			{Name: "firefox", Label: "Firefox", Family: TargetFirefox,
				Detect:      []string{filepath.Join(as, "Firefox"), filepath.Join(as, "Mozilla"), "/Applications/Firefox.app"},
				ManifestDir: filepath.Join(as, "Mozilla", "NativeMessagingHosts")},
			chromium("chrome", "Google Chrome", filepath.Join(as, "Google", "Chrome")),
			chromium("chromium", "Chromium", filepath.Join(as, "Chromium")),
			chromium("edge", "Microsoft Edge", filepath.Join(as, "Microsoft Edge")),
			chromium("brave", "Brave", filepath.Join(as, "BraveSoftware", "Brave-Browser")),
			chromium("arc", "Arc", filepath.Join(as, "Arc", "User Data")),
			chromium("vivaldi", "Vivaldi", filepath.Join(as, "Vivaldi")),
		}
	case "windows":
		local, roaming := e.getenv("LOCALAPPDATA"), e.getenv("APPDATA")
		key := func(vendor string) string { return `Software\` + vendor + `\NativeMessagingHosts\` + HostName }
		win := func(name, label, family, vendor string, detect ...string) Target {
			return Target{Name: name, Label: label, Family: family, Detect: detect, RegistryKey: key(vendor)}
		}
		return []Target{
			win("firefox", "Firefox", TargetFirefox, "Mozilla", filepath.Join(roaming, "Mozilla", "Firefox")),
			win("chrome", "Google Chrome", TargetChromium, `Google\Chrome`, filepath.Join(local, "Google", "Chrome", "User Data")),
			win("chromium", "Chromium", TargetChromium, "Chromium", filepath.Join(local, "Chromium", "User Data")),
			win("edge", "Microsoft Edge", TargetChromium, `Microsoft\Edge`, filepath.Join(local, "Microsoft", "Edge", "User Data")),
			win("brave", "Brave", TargetChromium, `BraveSoftware\Brave-Browser`, filepath.Join(local, "BraveSoftware", "Brave-Browser", "User Data")),
		}
	}
	return nil
}

// Present reports whether the browser seems installed for this user.
func (t Target) Present() bool {
	for _, d := range t.Detect {
		if d == "" {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			return true
		}
	}
	return false
}

// ManifestPath is where this target's host manifest lives.
func (t Target) ManifestPath(e Env) string {
	if t.RegistryKey != "" {
		return filepath.Join(e.DataDir, "browser", "native-host-"+t.Family+".json")
	}
	return filepath.Join(t.ManifestDir, HostName+".json")
}

// HostManifest is the native-messaging host manifest for a browser family, naming exe (the
// jarvis-agent binary, recognised as a host by its argv) and admitting only our extension.
func HostManifest(family, exe string) ([]byte, error) {
	if !filepath.IsAbs(exe) {
		return nil, fmt.Errorf("host path must be absolute: %q", exe)
	}
	m := map[string]any{
		"name":        HostName,
		"description": "Jarvis desktop agent (browser control)",
		"path":        exe,
		"type":        "stdio",
	}
	switch family {
	case TargetFirefox:
		m["allowed_extensions"] = []string{FirefoxExtensionID}
	case TargetChromium:
		m["allowed_origins"] = AllowedOrigins // the unpacked build and the Web Store listing
	default:
		return nil, fmt.Errorf("unknown browser family %q", family)
	}
	return json.MarshalIndent(m, "", "  ")
}

// Registry is the per-user (HKCU) registry, for Windows. Keys are relative to HKCU.
type Registry interface {
	SetDefault(key, value string) error
	GetDefault(key string) (string, error)
	DeleteKey(key string) error
}

// Result is what happened to one target.
type Result struct {
	Target  Target
	Present bool
	Path    string // manifest path
	Done    bool   // registered / removed
	Err     error
}

// Register writes the host manifest for every present browser (all with all=true), pointing
// at exe. On Windows reg must be the real registry (NativeRegistry).
func Register(e Env, exe string, all bool, reg Registry) []Result {
	var out []Result
	for _, t := range Targets(e) {
		r := Result{Target: t, Present: t.Present(), Path: t.ManifestPath(e)}
		if r.Present || all {
			r.Err = register(e, t, exe, reg)
			r.Done = r.Err == nil
		}
		out = append(out, r)
	}
	return out
}

func register(e Env, t Target, exe string, reg Registry) error {
	b, err := HostManifest(t.Family, exe)
	if err != nil {
		return err
	}
	p := t.ManifestPath(e)
	if err := writeFileAtomic(p, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if t.RegistryKey != "" {
		if reg == nil {
			return errors.New("no registry on this platform")
		}
		return reg.SetDefault(t.RegistryKey, p)
	}
	return nil
}

// Unregister removes every host manifest (and registry key) this package writes.
func Unregister(e Env, reg Registry) []Result {
	var out []Result
	for _, t := range Targets(e) {
		r := Result{Target: t, Present: t.Present(), Path: t.ManifestPath(e)}
		if t.RegistryKey != "" && reg != nil {
			if v, err := reg.GetDefault(t.RegistryKey); err == nil && v != "" {
				r.Err = reg.DeleteKey(t.RegistryKey)
				r.Done = r.Err == nil
			}
		}
		if err := os.Remove(r.Path); err == nil {
			r.Done = true
		} else if !errors.Is(err, os.ErrNotExist) && r.Err == nil {
			r.Err = err
		}
		out = append(out, r)
	}
	return out
}

// TargetStatus is one target's registration as found on disk.
type TargetStatus struct {
	Target     Target
	Present    bool
	Registered bool   // a manifest for our host exists (and, on Windows, the key names it)
	HostPath   string // the binary that manifest starts
	HostOK     bool   // that binary exists
	Problem    string // why the registration won't work, if it won't
}

// Inspect reports each target's registration.
func Inspect(e Env, reg Registry) []TargetStatus {
	var out []TargetStatus
	for _, t := range Targets(e) {
		s := TargetStatus{Target: t, Present: t.Present()}
		p := t.ManifestPath(e)
		if t.RegistryKey != "" {
			v := ""
			if reg != nil {
				v, _ = reg.GetDefault(t.RegistryKey)
			}
			if v == "" {
				out = append(out, s)
				continue
			}
			p = v
		}
		b, err := os.ReadFile(p)
		if err != nil {
			out = append(out, s)
			continue
		}
		s.Registered = true
		var m struct {
			Name              string   `json:"name"`
			Path              string   `json:"path"`
			AllowedExtensions []string `json:"allowed_extensions"`
			AllowedOrigins    []string `json:"allowed_origins"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			s.Problem = "manifest is not valid JSON"
		} else {
			s.HostPath = m.Path
			_, err := os.Stat(m.Path)
			s.HostOK = err == nil
			switch {
			case m.Name != HostName:
				s.Problem = "manifest names another host"
			case !s.HostOK:
				s.Problem = "the binary it starts is gone (re-run: jarvis-agent browser install)"
			case t.Family == TargetFirefox && !contains(m.AllowedExtensions, FirefoxExtensionID),
				t.Family == TargetChromium && !containsAll(m.AllowedOrigins, AllowedOrigins):
				s.Problem = "manifest doesn't admit the Jarvis extension (re-run: jarvis-agent browser install)"
			}
		}
		out = append(out, s)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func containsAll(list, want []string) bool {
	for _, s := range want {
		if !contains(list, s) {
			return false
		}
	}
	return true
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(b)
	cerr := f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, mode)
	}
	if werr == nil {
		werr = os.Rename(tmp, path)
	}
	if werr != nil {
		_ = os.Remove(tmp)
	}
	return werr
}

// ChromiumSteps are the manual steps for a Chromium browser: Chrome Web Store installs aside,
// a browser only loads an unpacked extension the user picks in its own UI.
// WebsiteAccessSteps is how to give the extension access to every site in browser (its name as
// the extension reports it), the permission Firefox lets the user turn off; without it the
// extension can't read or act on pages.
func WebsiteAccessSteps(browser string) string {
	if strings.Contains(strings.ToLower(browser), "firefox") {
		return "click the Jarvis Browser Control toolbar button -> \"Turn on\" (or about:addons -> Jarvis Browser Control -> Permissions -> turn on \"Access your data for all websites\")"
	}
	return "click the Jarvis Browser Control toolbar button -> \"Turn on\" (or the extensions page -> Jarvis Browser Control -> Details -> Site access -> \"On all sites\")"
}

func ChromiumSteps(dir string) string {
	return strings.Join([]string{
		"In Chrome, Edge, Brave, Arc or Vivaldi:",
		"  1. Open the extensions page (chrome://extensions, edge://extensions, brave://extensions).",
		"  2. Turn on \"Developer mode\".",
		"  3. Click \"Load unpacked\" and choose: " + dir,
		"  4. \"Jarvis Browser Control\" appears (id " + ChromeDevExtensionID + "). Keep that folder: the browser loads it from there.",
	}, "\n")
}
