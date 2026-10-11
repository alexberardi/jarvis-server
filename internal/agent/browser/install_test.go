package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testEnv(t *testing.T, goos string) Env {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
	}
	return Env{GOOS: goos, Home: home, DataDir: filepath.Join(home, "data", "jarvis-agent"), Getenv: func(k string) string { return vars[k] }}
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func targetByName(t *testing.T, e Env, name string) Target {
	t.Helper()
	for _, tg := range Targets(e) {
		if tg.Name == name {
			return tg
		}
	}
	t.Fatalf("no target %s on %s", name, e.GOOS)
	return Target{}
}

func TestTargetLocations(t *testing.T) {
	lin := testEnv(t, "linux")
	for name, want := range map[string]string{
		"firefox":  ".mozilla/native-messaging-hosts/io.jarvis.agent.browser.json",
		"chrome":   ".config/google-chrome/NativeMessagingHosts/io.jarvis.agent.browser.json",
		"chromium": ".config/chromium/NativeMessagingHosts/io.jarvis.agent.browser.json",
		"edge":     ".config/microsoft-edge/NativeMessagingHosts/io.jarvis.agent.browser.json",
		"brave":    ".config/BraveSoftware/Brave-Browser/NativeMessagingHosts/io.jarvis.agent.browser.json",
	} {
		if got := targetByName(t, lin, name).ManifestPath(lin); got != filepath.Join(lin.Home, want) {
			t.Errorf("linux %s: %s", name, got)
		}
	}
	mac := testEnv(t, "darwin")
	as := filepath.Join(mac.Home, "Library", "Application Support")
	for name, want := range map[string]string{
		"firefox": "Mozilla/NativeMessagingHosts",
		"chrome":  "Google/Chrome/NativeMessagingHosts",
		"edge":    "Microsoft Edge/NativeMessagingHosts",
		"brave":   "BraveSoftware/Brave-Browser/NativeMessagingHosts",
		"arc":     "Arc/User Data/NativeMessagingHosts",
	} {
		if got := targetByName(t, mac, name).ManifestPath(mac); got != filepath.Join(as, want, HostName+".json") {
			t.Errorf("darwin %s: %s", name, got)
		}
	}
	win := testEnv(t, "windows")
	for name, key := range map[string]string{
		"firefox": `Software\Mozilla\NativeMessagingHosts\io.jarvis.agent.browser`,
		"chrome":  `Software\Google\Chrome\NativeMessagingHosts\io.jarvis.agent.browser`,
		"edge":    `Software\Microsoft\Edge\NativeMessagingHosts\io.jarvis.agent.browser`,
	} {
		tg := targetByName(t, win, name)
		if tg.RegistryKey != key || !strings.HasPrefix(tg.ManifestPath(win), win.DataDir) {
			t.Errorf("windows %s: key %s, manifest %s", name, tg.RegistryKey, tg.ManifestPath(win))
		}
	}
	// Nothing is ever written under a system location.
	for _, e := range []Env{lin, mac, win} {
		for _, tg := range Targets(e) {
			p := tg.ManifestPath(e)
			if !strings.HasPrefix(p, e.Home) {
				t.Errorf("%s %s writes outside the home: %s", e.GOOS, tg.Name, p)
			}
		}
	}
}

// Firefox reads only ~/.mozilla/native-messaging-hosts, even when its profiles are in the XDG
// dir: an XDG-only Firefox must still be registered there.
func TestFirefoxXDGProfileRegistersUnderDotMozilla(t *testing.T) {
	e := testEnv(t, "linux")
	mkdirs(t, filepath.Join(e.Home, ".config", "mozilla", "firefox"))
	res := Register(e, "/opt/jarvis/jarvis-agent", false, nil)
	for _, r := range res {
		if r.Target.Name == "firefox" {
			if !r.Done || r.Path != filepath.Join(e.Home, ".mozilla", "native-messaging-hosts", HostName+".json") {
				t.Fatalf("firefox %+v", r)
			}
			return
		}
	}
	t.Fatal("no firefox result")
}

func TestRegisterPresentOnly(t *testing.T) {
	e := testEnv(t, "linux")
	mkdirs(t, filepath.Join(e.Home, ".mozilla"), filepath.Join(e.Home, ".config", "chromium"))
	exe := "/home/u/.local/bin/jarvis-agent"
	done := map[string]bool{}
	for _, r := range Register(e, exe, false, nil) {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Target.Name, r.Err)
		}
		done[r.Target.Name] = r.Done
	}
	if !done["firefox"] || !done["chromium"] || done["chrome"] || done["edge"] {
		t.Fatalf("done %v", done)
	}
	if _, err := os.Stat(filepath.Join(e.Home, ".config", "google-chrome")); !os.IsNotExist(err) {
		t.Fatal("created a profile dir for a browser that isn't installed")
	}
	var ff, cr map[string]any
	b, _ := os.ReadFile(filepath.Join(e.Home, ".mozilla", "native-messaging-hosts", HostName+".json"))
	_ = json.Unmarshal(b, &ff)
	b, _ = os.ReadFile(filepath.Join(e.Home, ".config", "chromium", "NativeMessagingHosts", HostName+".json"))
	_ = json.Unmarshal(b, &cr)
	if ff["name"] != HostName || ff["path"] != exe || ff["type"] != "stdio" || ff["allowed_origins"] != nil {
		t.Fatalf("firefox manifest %v", ff)
	}
	if exts := ff["allowed_extensions"].([]any); len(exts) != 1 || exts[0] != FirefoxExtensionID {
		t.Fatalf("firefox admits %v", exts)
	}
	if origins := cr["allowed_origins"].([]any); len(origins) != 1 || origins[0] != "chrome-extension://"+ChromeExtensionID+"/" {
		t.Fatalf("chromium admits %v", origins)
	}
	if cr["allowed_extensions"] != nil {
		t.Fatal("chromium manifest has firefox keys")
	}

	// --all registers everywhere.
	for _, r := range Register(e, exe, true, nil) {
		if !r.Done {
			t.Errorf("--all skipped %s", r.Target.Name)
		}
	}
}

func TestHostManifestRefusesRelativePath(t *testing.T) {
	if _, err := HostManifest(TargetFirefox, "jarvis-agent"); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := HostManifest("safari", "/x"); err == nil {
		t.Fatal("unknown family accepted")
	}
}

func TestInspectAndUnregister(t *testing.T) {
	e := testEnv(t, "linux")
	mkdirs(t, filepath.Join(e.Home, ".mozilla"), filepath.Join(e.Home, ".config", "chromium"))
	exe := filepath.Join(e.Home, "bin", "jarvis-agent")
	mkdirs(t, filepath.Dir(exe))
	if err := os.WriteFile(exe, []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	Register(e, exe, false, nil)
	st := map[string]TargetStatus{}
	for _, s := range Inspect(e, nil) {
		st[s.Target.Name] = s
	}
	if s := st["firefox"]; !s.Registered || !s.HostOK || s.Problem != "" || s.HostPath != exe {
		t.Fatalf("firefox %+v", s)
	}
	if s := st["chrome"]; s.Registered || s.Present {
		t.Fatalf("chrome %+v", s)
	}
	// The binary moved: status says why it won't work.
	_ = os.Remove(exe)
	for _, s := range Inspect(e, nil) {
		if s.Target.Name == "firefox" && (s.HostOK || !strings.Contains(s.Problem, "gone")) {
			t.Fatalf("missing binary not reported: %+v", s)
		}
	}
	// Someone else's manifest under our name is reported, not trusted.
	p := targetByName(t, e, "chromium").ManifestPath(e)
	_ = os.WriteFile(p, []byte(`{"name":"io.jarvis.agent.browser","path":"/bin/sh","allowed_origins":["chrome-extension://evil/"]}`), 0o644)
	for _, s := range Inspect(e, nil) {
		if s.Target.Name == "chromium" && !strings.Contains(s.Problem, "admit") {
			t.Fatalf("foreign origin not reported: %+v", s)
		}
	}
	for _, r := range Unregister(e, nil) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	for _, s := range Inspect(e, nil) {
		if s.Registered {
			t.Fatalf("%s still registered", s.Target.Name)
		}
	}
}

type fakeRegistry map[string]string

func (f fakeRegistry) SetDefault(k, v string) error        { f[k] = v; return nil }
func (f fakeRegistry) GetDefault(k string) (string, error) { return f[k], nil }
func (f fakeRegistry) DeleteKey(k string) error            { delete(f, k); return nil }

func TestWindowsRegistry(t *testing.T) {
	e := testEnv(t, "windows")
	mkdirs(t, filepath.Join(e.getenv("APPDATA"), "Mozilla", "Firefox"), filepath.Join(e.getenv("LOCALAPPDATA"), "Microsoft", "Edge", "User Data"))
	reg := fakeRegistry{}
	exe := `C:\Users\u\AppData\Local\Programs\Jarvis\jarvis-agent.exe`
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(e.Home, "jarvis-agent.exe") // off Windows, a C:\ path isn't absolute
	}
	for _, r := range Register(e, exe, false, reg) {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	ffKey := `Software\Mozilla\NativeMessagingHosts\` + HostName
	edgeKey := `Software\Microsoft\Edge\NativeMessagingHosts\` + HostName
	if len(reg) != 2 || reg[ffKey] == "" || reg[edgeKey] == "" {
		t.Fatalf("registry %v", reg)
	}
	b, err := os.ReadFile(reg[ffKey])
	if err != nil || !strings.Contains(string(b), FirefoxExtensionID) {
		t.Fatalf("firefox manifest at %s: %v", reg[ffKey], err)
	}
	if b, _ := os.ReadFile(reg[edgeKey]); !strings.Contains(string(b), ChromeExtensionID) {
		t.Fatal("edge manifest doesn't admit the extension")
	}
	for _, s := range Inspect(e, reg) {
		if (s.Target.Name == "firefox" || s.Target.Name == "edge") && !s.Registered {
			t.Errorf("%s not registered per Inspect", s.Target.Name)
		}
	}
	Unregister(e, reg)
	if len(reg) != 0 {
		t.Fatalf("keys left: %v", reg)
	}
	// Without a registry (not Windows) a Windows target fails instead of half-registering.
	for _, r := range Register(e, exe, false, nil) {
		if r.Present && r.Err == nil {
			t.Fatalf("%s registered without a registry", r.Target.Name)
		}
	}
}
