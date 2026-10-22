package browser

import (
	"encoding/json"
	"errors"
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
	res := Register(e, filepath.Join(e.Home, "bin", "jarvis-agent"), false, nil)
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
	exe := filepath.Join(e.Home, ".local", "bin", "jarvis-agent") // absolute on every OS
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
	// Both Chromium builds: the unpacked one (key-derived id) and the Chrome Web Store listing.
	wantOrigins := []string{"chrome-extension://hdjdhmmlggbjjdokpofkghjnjnmjfhmk/", "chrome-extension://abkbjmelelgelkhneobebgoilejlgbob/"}
	if origins := cr["allowed_origins"].([]any); len(origins) != len(wantOrigins) {
		t.Fatalf("chromium admits %v, want %v", origins, wantOrigins)
	} else {
		for i, o := range origins {
			if o != wantOrigins[i] {
				t.Fatalf("chromium admits %v, want %v", origins, wantOrigins)
			}
		}
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
	_ = os.WriteFile(exe, []byte("#!"), 0o755)
	p := targetByName(t, e, "chromium").ManifestPath(e)
	foreign, _ := json.Marshal(map[string]any{"name": HostName, "path": exe, "allowed_origins": []string{"chrome-extension://evil/"}})
	_ = os.WriteFile(p, foreign, 0o644)
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
	if b, _ := os.ReadFile(reg[edgeKey]); !strings.Contains(string(b), ChromeDevExtensionID) || !strings.Contains(string(b), ChromeStoreExtensionID) {
		t.Fatal("edge manifest doesn't admit both builds of the extension")
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

// Every admitted Chromium id is a real extension id (32 letters a-p), the unpacked build's
// comes first, and none repeats: a typo'd store id would silently lock the listing out.
func TestChromeExtensionIDs(t *testing.T) {
	if len(ChromeExtensionIDs) == 0 || ChromeExtensionIDs[0] != ChromeDevExtensionID {
		t.Fatalf("ChromeExtensionIDs %v must start with the unpacked id %s", ChromeExtensionIDs, ChromeDevExtensionID)
	}
	if !contains(ChromeExtensionIDs, ChromeStoreExtensionID) || ChromeStoreExtensionID == ChromeDevExtensionID {
		t.Fatalf("ChromeExtensionIDs %v must admit the Web Store listing %s", ChromeExtensionIDs, ChromeStoreExtensionID)
	}
	seen := map[string]bool{}
	for _, id := range ChromeExtensionIDs {
		if len(id) != 32 || strings.Trim(id, "abcdefghijklmnop") != "" {
			t.Errorf("%q is not a Chromium extension id", id)
		}
		if seen[id] {
			t.Errorf("%q listed twice", id)
		}
		seen[id] = true
	}
	if len(AllowedOrigins) != len(ChromeExtensionIDs) {
		t.Fatalf("AllowedOrigins %v vs ids %v", AllowedOrigins, ChromeExtensionIDs)
	}
	for i, id := range ChromeExtensionIDs {
		if AllowedOrigins[i] != "chrome-extension://"+id+"/" {
			t.Errorf("origin %d = %q for id %q", i, AllowedOrigins[i], id)
		}
	}
}

// Adding an id (as the Web Store id once was) makes a host registered before it stale: status must say so (the
// store build would be refused), and a fresh install admits both builds.
func TestInspectFlagsHostMissingAnAddedID(t *testing.T) {
	e := testEnv(t, "linux")
	mkdirs(t, filepath.Join(e.Home, ".config", "chromium"))
	exe := filepath.Join(e.Home, "bin", "jarvis-agent")
	mkdirs(t, filepath.Dir(exe))
	if err := os.WriteFile(exe, []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	Register(e, exe, false, nil) // admits the current ids only

	saved := AllowedOrigins
	t.Cleanup(func() { AllowedOrigins = saved })
	AllowedOrigins = chromeOrigins(append(append([]string{}, ChromeExtensionIDs...), "abcdefghijklmnopabcdefghijklmnop"))

	chromium := func() TargetStatus {
		for _, s := range Inspect(e, nil) {
			if s.Target.Name == "chromium" {
				return s
			}
		}
		t.Fatal("no chromium status")
		return TargetStatus{}
	}
	if s := chromium(); !strings.Contains(s.Problem, "admit") {
		t.Fatalf("host without the added id not reported: %+v", s)
	}
	Register(e, exe, false, nil)
	if s := chromium(); s.Problem != "" {
		t.Fatalf("after re-install: %+v", s)
	}
	var m struct {
		AllowedOrigins []string `json:"allowed_origins"`
	}
	b, _ := os.ReadFile(targetByName(t, e, "chromium").ManifestPath(e))
	if err := json.Unmarshal(b, &m); err != nil || len(m.AllowedOrigins) != len(ChromeExtensionIDs)+1 ||
		m.AllowedOrigins[0] != "chrome-extension://"+ChromeDevExtensionID+"/" {
		t.Fatalf("re-installed host admits %v (%v)", m.AllowedOrigins, err)
	}
}

// A host registered by an agent from before the Web Store listing admits only the unpacked
// build, so Chrome would refuse the store-installed extension: status must say so.
func TestInspectFlagsPreStoreHost(t *testing.T) {
	e := testEnv(t, "linux")
	mkdirs(t, filepath.Join(e.Home, ".config", "google-chrome"))
	exe := filepath.Join(e.Home, "bin", "jarvis-agent")
	mkdirs(t, filepath.Dir(exe))
	if err := os.WriteFile(exe, []byte("#!"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := targetByName(t, e, "chrome").ManifestPath(e)
	mkdirs(t, filepath.Dir(p))
	old, _ := json.Marshal(map[string]any{"name": HostName, "path": exe, "type": "stdio",
		"allowed_origins": []string{"chrome-extension://" + ChromeDevExtensionID + "/"}})
	if err := os.WriteFile(p, old, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range Inspect(e, nil) {
		if s.Target.Name == "chrome" && !strings.Contains(s.Problem, "admit") {
			t.Fatalf("pre-store host not reported: %+v", s)
		}
	}
}

func translocatedExe(e Env) string {
	return filepath.Join(e.Home, "private", "var", "folders", "xx", "T", "AppTranslocation", "ABC", "d",
		"Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent")
}

func TestTranslocated(t *testing.T) {
	for p, want := range map[string]bool{
		"/private/var/folders/xx/T/AppTranslocation/ABC/d/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent": true,
		"/Applications/Jarvis Automation Agent.app/Contents/MacOS/jarvis-agent":                                    false,
		"/home/a/.local/bin/jarvis-agent": false,
	} {
		if got := Translocated(p); got != want {
			t.Errorf("Translocated(%q) = %v", p, got)
		}
	}
}

func TestRegisterRefusesATranslocatedExe(t *testing.T) {
	e := testEnv(t, "darwin")
	exe := translocatedExe(e)
	mkdirs(t, filepath.Dir(exe))
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, r := range Register(e, exe, true, nil) {
		if !errors.Is(r.Err, ErrTranslocated) || r.Done {
			t.Errorf("%s: err=%v done=%v, want ErrTranslocated", r.Target.Name, r.Err, r.Done)
		}
		if _, err := os.Stat(r.Path); err == nil {
			t.Errorf("%s: wrote %s", r.Target.Name, r.Path)
		}
	}
}

// writeManifest registers target name in e as pointing at exe, bypassing Register's checks.
func writeManifest(t *testing.T, e Env, name, exe string) {
	t.Helper()
	tg := targetByName(t, e, name)
	if err := register(e, tg, exe, nil); err != nil {
		t.Fatal(err)
	}
}

func fakeBinary(t *testing.T, p string) string {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func statusOf(t *testing.T, e Env, name string) TargetStatus {
	t.Helper()
	for _, s := range Inspect(e, nil) {
		if s.Target.Name == name {
			return s
		}
	}
	t.Fatalf("no status for %s", name)
	return TargetStatus{}
}

func TestInspectFlagsATranslocatedHost(t *testing.T) {
	e := testEnv(t, "darwin")
	writeManifest(t, e, "firefox", fakeBinary(t, translocatedExe(e)))
	s := statusOf(t, e, "firefox")
	if !strings.Contains(s.Problem, "temporary copy") {
		t.Errorf("problem = %q, want the translocation explained", s.Problem)
	}
}

func TestInspectWarnsAboutAnotherCopy(t *testing.T) {
	e := testEnv(t, "darwin")
	old := fakeBinary(t, filepath.Join(e.Home, "Downloads", "old", "jarvis-agent"))
	e.Exe = fakeBinary(t, filepath.Join(e.Home, "Applications", "Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent"))
	writeManifest(t, e, "firefox", old)
	writeManifest(t, e, "chrome", e.Exe)
	if s := statusOf(t, e, "firefox"); s.Problem != "" || !strings.Contains(s.Warning, old) {
		t.Errorf("firefox: problem=%q warning=%q, want a warning naming %s", s.Problem, s.Warning, old)
	}
	if s := statusOf(t, e, "chrome"); s.Problem != "" || s.Warning != "" {
		t.Errorf("chrome: problem=%q warning=%q, want healthy", s.Problem, s.Warning)
	}
	e.Exe = ""
	if s := statusOf(t, e, "firefox"); s.Warning != "" {
		t.Errorf("no running exe given: warning=%q", s.Warning)
	}
}

func TestRefreshRewritesOnlyRegisteredTargetsPointingElsewhere(t *testing.T) {
	e := testEnv(t, "darwin")
	as := filepath.Join(e.Home, "Library", "Application Support")
	mkdirs(t, filepath.Join(as, "Firefox"), filepath.Join(as, "Google", "Chrome"), filepath.Join(as, "BraveSoftware", "Brave-Browser"))
	old := fakeBinary(t, translocatedExe(e))
	exe := fakeBinary(t, filepath.Join(e.Home, "Applications", "Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent"))
	writeManifest(t, e, "firefox", old)
	writeManifest(t, e, "chrome", exe)
	// brave is present but was never registered (or the user removed it): it stays that way.

	res := Refresh(e, exe, nil)
	if len(res) != 1 || res[0].Target.Name != "firefox" || !res[0].Done || res[0].Err != nil {
		t.Fatalf("refresh results = %+v, want just firefox rewritten", res)
	}
	for name, want := range map[string]string{"firefox": exe, "chrome": exe} {
		if s := statusOf(t, e, name); s.HostPath != want || s.Problem != "" {
			t.Errorf("%s: host=%q problem=%q", name, s.HostPath, s.Problem)
		}
	}
	if s := statusOf(t, e, "brave"); s.Registered {
		t.Errorf("brave got registered")
	}
	if res := Refresh(e, exe, nil); len(res) != 0 {
		t.Errorf("second refresh rewrote %+v", res)
	}
}

func TestRefreshFromATranslocatedExeChangesNothing(t *testing.T) {
	e := testEnv(t, "darwin")
	good := fakeBinary(t, filepath.Join(e.Home, "Applications", "Jarvis Automation Agent.app", "Contents", "MacOS", "jarvis-agent"))
	writeManifest(t, e, "firefox", good)
	res := Refresh(e, fakeBinary(t, translocatedExe(e)), nil)
	if len(res) != 1 || !errors.Is(res[0].Err, ErrTranslocated) {
		t.Errorf("results = %+v, want one ErrTranslocated", res)
	}
	if s := statusOf(t, e, "firefox"); s.HostPath != good {
		t.Errorf("firefox now points at %q", s.HostPath)
	}
}
