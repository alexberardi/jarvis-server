package apps

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every layout is built in a temp dir, so all three platforms' scanners are exercised on every
// OS in the CI matrix.

func touch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkApp(t *testing.T, path string) {
	t.Helper()
	touch(t, filepath.Join(path, "Contents", "Info.plist"), "<plist/>")
}

func desktop(name, extra string) string {
	return "[Desktop Entry]\nType=Application\nName=" + name + "\nName[de]=Lokalisiert\nExec=" + strings.ToLower(name) + " %U\n" + extra
}

func appNames(as []App) string {
	var n []string
	for _, a := range as {
		n = append(n, a.Name)
	}
	return strings.Join(n, ",")
}

// macTree mimics /Applications, /System/Applications and ~/Applications under root.
func macTree(t *testing.T) (Env, string) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mkApp(t, filepath.Join(root, "Applications", "Steam.app"))
	mkApp(t, filepath.Join(root, "Applications", "Visual Studio Code.app"))
	mkApp(t, filepath.Join(root, "Applications", "Utilities", "Terminal.app")) // nested folder
	// Helpers nested in a bundle are not user apps.
	mkApp(t, filepath.Join(root, "Applications", "Google Chrome.app", "Contents", "Frameworks", "Google Chrome Helper.app"))
	mkApp(t, filepath.Join(root, "System", "Applications", "Calculator.app"))
	mkApp(t, filepath.Join(home, "Applications", "Spotify.app"))
	touch(t, filepath.Join(root, "Applications", "README.app"), "a file, not a bundle")
	return Env{GOOS: "darwin", Home: home}, root
}

// macSources rebases the darwin defaults under root (the real ones are absolute system paths).
func macSources(root string, e Env) []Source {
	var out []Source
	for _, s := range DefaultSources(e) {
		if !strings.HasPrefix(s.Pattern, e.Home) {
			s.Pattern = filepath.Join(root, s.Pattern)
		}
		out = append(out, s)
	}
	return out
}

func TestMacScanFindsBundlesNotHelpers(t *testing.T) {
	e, root := macTree(t)
	list, err := Catalog{Sources: macSources(root, e)}.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := appNames(list); got != "Calculator,Google Chrome,Spotify,Steam,Terminal,Visual Studio Code" {
		t.Fatalf("apps = %s", got)
	}
	for _, a := range list {
		if fi, err := os.Stat(a.Path); err != nil || !fi.IsDir() {
			t.Fatalf("%s: path %s is not a bundle", a.Name, a.Path)
		}
	}
}

func TestLinuxScanHonoursNoDisplayHiddenAndXDGPrecedence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	usr := filepath.Join(root, "usr", "share")
	user := filepath.Join(home, ".local", "share")
	touch(t, filepath.Join(usr, "applications", "steam.desktop"), desktop("Steam", ""))
	touch(t, filepath.Join(usr, "applications", "spotify.desktop"), desktop("Spotify", ""))
	touch(t, filepath.Join(usr, "applications", "helper.desktop"), desktop("Helper", "NoDisplay=true\n"))
	touch(t, filepath.Join(usr, "applications", "link.desktop"), "[Desktop Entry]\nType=Link\nName=Docs\nURL=x\n")
	touch(t, filepath.Join(usr, "applications", "kde4", "kate.desktop"), desktop("Kate", ""))
	// The user hides the system Spotify and renames Steam: the user dir wins by desktop id.
	touch(t, filepath.Join(user, "applications", "spotify.desktop"), desktop("Spotify", "Hidden=true\n"))
	touch(t, filepath.Join(user, "applications", "steam.desktop"), desktop("Steam (Runtime)", ""))
	e := Env{GOOS: "linux", Home: home, Getenv: func(k string) string {
		if k == "XDG_DATA_DIRS" {
			return usr
		}
		return ""
	}}
	list, err := Build(e, Config{}).List()
	if err != nil {
		t.Fatal(err)
	}
	if got := appNames(list); got != "Kate,Steam (Runtime)" {
		t.Fatalf("apps = %s", got)
	}
	kate, err := Resolve(list, "kde4-kate")
	if err != nil || kate.Name != "Kate" {
		t.Fatalf("desktop id of a subdir entry: %v %v", kate, err)
	}
}

func TestWindowsScanReadsBothStartMenus(t *testing.T) {
	root := t.TempDir()
	appdata := filepath.Join(root, "AppData", "Roaming")
	pd := filepath.Join(root, "ProgramData")
	menu := func(base string, rel ...string) string {
		return filepath.Join(append([]string{base, "Microsoft", "Windows", "Start Menu", "Programs"}, rel...)...)
	}
	touch(t, menu(pd, "Steam", "Steam.lnk"), "")
	touch(t, menu(pd, "Steam", "Uninstall Steam.lnk"), "")
	touch(t, menu(pd, "Notepad++.lnk"), "")
	touch(t, menu(appdata, "Spotify.lnk"), "")
	touch(t, menu(pd, "desktop.ini"), "")
	e := Env{GOOS: "windows", Home: root, Getenv: envMap{"APPDATA": appdata, "ProgramData": pd}.get}
	list, err := Build(e, Config{}).List()
	if err != nil {
		t.Fatal(err)
	}
	if got := appNames(list); got != "Notepad++,Spotify,Steam" {
		t.Fatalf("apps = %s (uninstallers must be denied by default)", got)
	}
}

type envMap map[string]string

func (m envMap) get(k string) string { return m[k] }

func TestConfigAllowAndDeny(t *testing.T) {
	e, root := macTree(t)
	games := filepath.Join(e.Home, "Games")
	mkApp(t, filepath.Join(games, "Factorio.app"))
	cfg := Config{Allow: []string{"~/Games/*.app"}, Deny: []string{"spotify", "**/Utilities/**"}}
	c := Build(e, cfg)
	// Rebase the system defaults under the fake root; keep the config source as is.
	for i, s := range c.Sources {
		if !strings.HasPrefix(s.Pattern, e.Home) {
			c.Sources[i].Pattern = filepath.Join(root, s.Pattern)
		}
	}
	list, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	got := appNames(list)
	if !strings.Contains(got, "Factorio") || strings.Contains(got, "Terminal") || strings.Contains(got, "Spotify") {
		t.Fatalf("apps = %s", got)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadConfig(filepath.Join(dir, "missing.json"))
	if err != nil || len(c.Allow)+len(c.Deny) != 0 {
		t.Fatalf("missing file: %v %v", c, err)
	}
	p := filepath.Join(dir, "apps.json")
	touch(t, p, `{"allow": ["~/Games/**/*.app"], "deny": ["Terminal"]}`)
	if c, err = LoadConfig(p); err != nil || c.Allow[0] != "~/Games/**/*.app" || c.Deny[0] != "Terminal" {
		t.Fatalf("%v %v", c, err)
	}
	touch(t, p, `{"allow": `)
	if _, err = LoadConfig(p); err == nil {
		t.Fatal("broken JSON accepted")
	}
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		glob, path string
		want       bool
	}{
		{"/Applications/**/*.app", "/Applications/Steam.app", true},
		{"/Applications/**/*.app", "/Applications/Utilities/Terminal.app", true},
		{"/Applications/**/*.app", "/Applications/Steam.app/Contents", false},
		{"/Applications/*.app", "/Applications/Utilities/Terminal.app", false},
		{"*uninstall*", "Uninstall Steam", true},
		{"st?am", "STEAM", true},
		{"a.b", "axb", false}, // dots are literal
	} {
		re, err := globRegexp(tc.glob)
		if err != nil {
			t.Fatal(err)
		}
		if re.MatchString(tc.path) != tc.want {
			t.Errorf("%q vs %q: want %v", tc.glob, tc.path, tc.want)
		}
	}
}

var sample = []App{
	{Name: "Steam"}, {Name: "Spotify"}, {Name: "Visual Studio Code"}, {Name: "Visual Studio"},
	{Name: "Google Chrome"}, {Name: "Firefox"}, {Name: "Calculator"}, {Name: "Notepad++"},
	{Name: "Microsoft Word"}, {Name: "Microsoft Excel"}, {Name: "Discord", ID: "com.discordapp.Discord"},
}

func TestResolve(t *testing.T) {
	for q, want := range map[string]string{
		"Steam":                  "Steam",
		"steam":                  "Steam",
		"the steam app":          "Steam",
		"Steam.app":              "Steam",
		"spotify":                "Spotify",
		"chrome":                 "Google Chrome", // word match
		"visual studio code":     "Visual Studio Code",
		"Visual Studio":          "Visual Studio", // exact beats the longer prefix
		"notepad plus plus":      "",              // no guessing from spelled-out symbols
		"firefx":                 "Firefox",       // one typo
		"calculater":             "Calculator",
		"word":                   "Microsoft Word",
		"com.discordapp.Discord": "Discord", // desktop id
	} {
		a, err := Resolve(sample, q)
		if want == "" {
			if err == nil {
				t.Errorf("%q resolved to %q; want no match", q, a.Name)
			}
			continue
		}
		if err != nil || a.Name != want {
			t.Errorf("%q -> %q, %v; want %q", q, a.Name, err, want)
		}
	}
}

func TestResolveReportsAmbiguityAndSuggestions(t *testing.T) {
	_, err := Resolve(sample, "microsoft")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || strings.Join(amb.Candidates, ",") != "Microsoft Excel,Microsoft Word" {
		t.Fatalf("err = %v", err)
	}
	_, err = Resolve(sample, "photoshop")
	var nf *NotFoundError
	if !errors.As(err, &nf) || !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Resolve(sample, "   "); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blank: %v", err)
	}
}

func TestResolveNeverTakesPathsOrArguments(t *testing.T) {
	for _, q := range []string{"/usr/bin/xterm", `C:\Windows\System32\cmd.exe`, "steam -applaunch 570 --evil"} {
		if a, err := Resolve(sample, q); err == nil {
			t.Errorf("%q resolved to %q", q, a.Name)
		}
	}
}

// "open the calculator" on Omarchy: the app is Omacalc, a Calculator by GenericName and Keywords.
func TestLinuxGenericNameAndKeywordsResolve(t *testing.T) {
	root := t.TempDir()
	usr := filepath.Join(root, "usr", "share")
	touch(t, filepath.Join(usr, "applications", "omacalc.desktop"),
		desktop("Omacalc", "GenericName=Calculator\nKeywords=calculator;calc;math;arithmetic;\n"))
	touch(t, filepath.Join(usr, "applications", "libreoffice-calc.desktop"),
		desktop("LibreOffice Calc", "GenericName=Spreadsheet\nKeywords=Accounting;Stats;ods;xls;xlsx;\n"))
	touch(t, filepath.Join(usr, "applications", "gnumeric.desktop"), desktop("Gnumeric", "GenericName=Spreadsheet\n"))
	e := Env{GOOS: "linux", Home: filepath.Join(root, "home"), Getenv: func(k string) string {
		if k == "XDG_DATA_DIRS" {
			return usr
		}
		return ""
	}}
	list, err := Build(e, Config{}).List()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"calculator", "the calculator", "math"} {
		if p, err := Find(list, q); err != nil || p.App.Name != "Omacalc" || p.Fuzzy {
			t.Errorf("%q: %+v %v", q, p, err)
		}
	}
	var amb *AmbiguousError
	if _, err := Find(list, "spreadsheet"); !errors.As(err, &amb) {
		t.Errorf("two spreadsheets: %v", err)
	}
}
