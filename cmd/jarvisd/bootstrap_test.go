package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

func TestTakeHome(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		home string
		rest []string
	}{
		{[]string{"serve", "--home", "/x", "--no-browser"}, "/x", []string{"serve", "--no-browser"}},
		{[]string{"--home=/y", "doctor", "--json"}, "/y", []string{"doctor", "--json"}},
		{[]string{"service", "install", "-home", "/z"}, "/z", []string{"service", "install"}},
		{[]string{"version"}, "", []string{"version"}},
	} {
		home, rest := takeHome(tc.in)
		if home != tc.home || !slices.Equal(rest, tc.rest) {
			t.Errorf("%q: %q %q", tc.in, home, rest)
		}
	}
}

// unsetForTest clears variables for the test and restores them afterwards.
func unsetForTest(t *testing.T, keys ...string) {
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// --home beats JARVIS_HOME, and <home>/jarvisd.env fills in what the environment leaves unset.
func TestBootstrapHomeAndEnvFile(t *testing.T) {
	unsetForTest(t, "JARVIS_HOME", "JARVIS_PORT_ADMIN", "JARVIS_LOG_LEVEL")
	t.Setenv("JARVIS_HOME", t.TempDir())
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, config.EnvFileName), []byte("JARVIS_PORT_ADMIN=17710\nJARVIS_LOG_LEVEL=debug\n"), 0o600)
	t.Setenv("JARVIS_LOG_LEVEL", "warn") // the process environment wins

	if err := bootstrap(home, true, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Home != home || cfg.Ports[config.ListenerAdmin] != 17710 || os.Getenv("JARVIS_LOG_LEVEL") != "warn" {
		t.Fatalf("home %q admin %d level %q", cfg.Home, cfg.Ports[config.ListenerAdmin], os.Getenv("JARVIS_LOG_LEVEL"))
	}
	// The doctor sees the env file's ports too.
	found := false
	for _, p := range doctorPorts(cfg) {
		found = found || (p.Name == config.ListenerAdmin && p.Port == 17710)
	}
	if !found {
		t.Error("doctor ignores the env file's port")
	}
}

func TestBootstrapBadEnvFile(t *testing.T) {
	unsetForTest(t, "JARVIS_HOME")
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, config.EnvFileName), []byte("garbage\n"), 0o600)
	if err := bootstrap(home, true, &bytes.Buffer{}); err == nil {
		t.Fatal("serve must refuse a broken env file")
	}
	var warn bytes.Buffer
	if err := bootstrap(home, false, &warn); err != nil || warn.Len() == 0 {
		t.Fatalf("CLI: %v %q", err, warn.String())
	}
}

// Starting on a home made world-readable tightens it, and the DB is owner-only.
func TestHomePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ACL protects the home on Windows")
	}
	unsetForTest(t, "JARVIS_HOME")
	home := filepath.Join(t.TempDir(), "h")
	os.Mkdir(home, 0o755)
	os.Chmod(home, 0o755)
	if err := run(context.Background(), []string{"migrate", "status", "--home", home}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(home)
	if st.Mode().Perm() != 0o700 {
		t.Errorf("home %v", st.Mode().Perm())
	}
	db, err := os.Stat(filepath.Join(home, "jarvis.db"))
	if err != nil || db.Mode().Perm() != 0o600 {
		t.Errorf("db %v %v", db, err)
	}
}
