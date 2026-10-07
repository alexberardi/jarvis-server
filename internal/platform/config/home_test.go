package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveHomePrecedence(t *testing.T) {
	user, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no user home")
	}
	abs := func(p string) string { a, _ := filepath.Abs(p); return a }
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, tc := range []struct {
		name, flag, service string
		env                 map[string]string
		want                string
	}{
		{"flag wins", "flagdir", "svcdir", map[string]string{"JARVIS_HOME": "envdir"}, abs("flagdir")},
		{"env next", "", "svcdir", map[string]string{"JARVIS_HOME": "envdir"}, abs("envdir")},
		{"installed service next", "", "svcdir", nil, abs("svcdir")},
		{"default ~/.jarvisd", "", "", nil, filepath.Join(user, ".jarvisd")},
	} {
		got, err := ResolveHome(tc.flag, tc.service, env(tc.env))
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// The code default is no longer the legacy stack's ~/.jarvis (ID1).
func TestLoadDefaultHomeIsJarvisd(t *testing.T) {
	c, err := load(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(c.Home) != ".jarvisd" {
		t.Fatalf("default home %q", c.Home)
	}
}

func TestParseEnv(t *testing.T) {
	in := "\xef\xbb\xbf# comment\r\n\nA=1\r\nexport B = two words \nC=\"quoted # not a comment\"\nD='single'\nE=\nA=dup\nF=\"unbalanced'\n"
	got, err := ParseEnv(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"A", "1"}, {"B", "two words"}, {"C", "quoted # not a comment"}, {"D", "single"}, {"E", ""}, {"F", "\"unbalanced'"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	for _, bad := range []string{"novalue\n", "=x\n", "A B=1\n"} {
		if _, err := ParseEnv(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

// Process environment > first file (<home>/jarvisd.env) > second file (/etc/jarvisd/jarvisd.env).
func TestLoadEnvFilesPrecedence(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home.env")
	system := filepath.Join(dir, "system.env")
	os.WriteFile(home, []byte("JDTEST_A=home\nJDTEST_B=home\nJARVIS_HOME=/ignored\n"), 0o600)
	os.WriteFile(system, []byte("JDTEST_A=system\nJDTEST_B=system\nJDTEST_C=system\n"), 0o600)
	for _, k := range []string{"JDTEST_A", "JDTEST_B", "JDTEST_C", "JARVIS_HOME"} {
		t.Setenv(k, "") // restored after the test
		os.Unsetenv(k)
	}
	t.Setenv("JDTEST_A", "process")

	set, err := LoadEnvFiles(home, filepath.Join(dir, "missing.env"), system)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"JDTEST_A": "process", "JDTEST_B": "home", "JDTEST_C": "system"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s=%q, want %q", k, got, want)
		}
	}
	if _, ok := os.LookupEnv("JARVIS_HOME"); ok {
		t.Error("JARVIS_HOME from an env file must be ignored")
	}
	slices.Sort(set)
	if !slices.Equal(set, []string{"JDTEST_B", "JDTEST_C"}) {
		t.Errorf("set %v", set)
	}
	// An empty value in the environment still counts as set.
	t.Setenv("JDTEST_B", "")
	if _, err := LoadEnvFiles(home); err != nil || os.Getenv("JDTEST_B") != "" {
		t.Errorf("empty env value overridden: %q %v", os.Getenv("JDTEST_B"), err)
	}
}

func TestLoadEnvFilesBadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.env")
	os.WriteFile(p, []byte("not an assignment\n"), 0o600)
	if _, err := LoadEnvFiles(p); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("err %v", err)
	}
}

func TestEnvFilesOrder(t *testing.T) {
	f := EnvFiles("/h")
	if f[0] != filepath.Join("/h", EnvFileName) {
		t.Fatalf("%v", f)
	}
}
