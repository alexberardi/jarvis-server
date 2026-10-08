package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

func TestHelperArgs(t *testing.T) {
	for args, want := range map[string]bool{
		"upgrade --helper":           true,
		"upgrade --owner x --helper": true,
		"upgrade --prestart":         false,
		"serve --helper":             false,
		"":                           false,
	} {
		if got := helperArgs(strings.Fields(args)); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}
}

func TestLookupUID(t *testing.T) {
	if n, err := lookupUID("501"); err != nil || n != 501 {
		t.Fatal(n, err)
	}
	if _, err := lookupUID("no-such-account-jarvisd-test"); err == nil {
		t.Fatal("unknown account accepted")
	}
}

// The helper needs its home from the service definition, never the environment.
func TestRunHelperNeedsAbsoluteHome(t *testing.T) {
	t.Setenv("JARVIS_HOME", t.TempDir())
	if err := runHelper(context.Background(), "", "", true, &bytes.Buffer{}); err == nil {
		t.Fatal("no --home accepted")
	}
	if err := runHelper(context.Background(), "rel", "", false, &bytes.Buffer{}); err == nil {
		t.Fatal("relative --home accepted")
	}
}

// lockedInstall is a home with a staged marker for a binary this process can't replace.
func lockedInstall(t *testing.T, state string) update.Paths {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory this process can't write (unix, not root)")
	}
	bin, home := t.TempDir(), t.TempDir()
	exe := filepath.Join(bin, "jarvisd")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bin, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(bin, 0o700) })
	p := update.Paths{Home: home, Exe: exe}
	if err := update.WriteMarker(p, &update.Marker{State: state, From: "v1.0.0", To: "v9.0.0", Exe: exe}); err != nil {
		t.Fatal(err)
	}
	return p
}

// Serve's start step with an on-demand helper (macOS): a staged upgrade it can't swap wakes
// the helper (a request file) and, once the helper has moved the marker on, exits for the
// restart.
func TestUpgradeStartWaitsForHelper(t *testing.T) {
	p := lockedInstall(t, update.StateStaged)
	log := logging.New(&bytes.Buffer{}, logging.ParseLevel(""), nil)
	go func() {
		for i := 0; i < 400; i++ {
			if ents, _ := os.ReadDir(p.RequestsDir()); len(ents) > 0 {
				m, _ := update.ReadMarker(p)
				m.State = update.StateSwapped
				_ = update.WriteMarker(p, m)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	gate, restart, err := upgradeStart(context.Background(), log, p, true, service.HelperLaunchd)
	if err != nil || !restart || gate != nil {
		t.Fatalf("%+v %v %v", gate, restart, err)
	}
}

// A rollback the helper began (binary restored) is finished by the unprivileged start: the
// result is recorded and serve carries on with the restored binary.
func TestUpgradeStartFinishesHelperRollback(t *testing.T) {
	p := lockedInstall(t, update.StateBinaryRestored)
	log := logging.New(&bytes.Buffer{}, logging.ParseLevel(""), nil)
	gate, restart, err := upgradeStart(context.Background(), log, p, true, service.HelperLaunchd)
	if err != nil || restart || gate != nil {
		t.Fatalf("%+v %v %v", gate, restart, err)
	}
	res, _ := update.ReadResult(p)
	if res == nil || res.Outcome != update.ResultRolledBack {
		t.Fatalf("result %+v", res)
	}
	if m, _ := update.ReadMarker(p); m != nil {
		t.Fatal("marker kept")
	}
}
