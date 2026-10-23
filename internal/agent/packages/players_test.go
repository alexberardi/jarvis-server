package packages

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// #478: after the Bluetooth output moves, the node restarts the music players that are running:
// a package's system unit (shairport-sync, Music Assistant's) through jarvis-post-install's
// restart-service, a user unit with systemctl --user. A stopped one stays stopped.
func TestRestartUnitsRestartsTheRunningPlayers(t *testing.T) {
	r := newRepos(t)
	m, _, _, fs := newPiManager(t, r)
	s := m.System
	dropin := func(svc, body string) {
		d := filepath.Join(s.DropinRoot, svc+".service.d")
		_ = os.MkdirAll(d, 0o755)
		if err := os.WriteFile(filepath.Join(d, "jarvis.conf"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dropin("shairport-sync", "# managed-by: jarvis-package music\n[Service]\n")
	dropin("mpd", "# managed-by: jarvis-package audacy\n[Service]\n")
	// systemctl is-active fails for what isn't running: spotifyd isn't, as a user or system unit.
	fs.fail = map[string]string{"is-active --quiet spotifyd": "inactive"}

	if err := s.RestartUnits(context.Background(), "shairport-sync", "spotifyd", "go-librespot"); err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(s.Sbin, PostInstallWrapper)
	want := []string{
		"systemctl is-active --quiet shairport-sync",
		"sudo -n " + w + " --package music restart-service",
		"systemctl --user is-active --quiet spotifyd",
		"systemctl --user is-active --quiet go-librespot",
		"systemctl --user restart go-librespot",
	}
	if got := fs.argvs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
	if got := fs.calls[1].stdin; !reflect.DeepEqual(got, map[string]any{"service": "shairport-sync"}) {
		t.Fatalf("restart-service payload %v", got)
	}
}

// A failed restart is reported (the caller logs it) and the other players are still restarted.
func TestRestartUnitsGoesOnPastAFailure(t *testing.T) {
	r := newRepos(t)
	m, _, _, fs := newPiManager(t, r)
	s := m.System
	d := filepath.Join(s.DropinRoot, "shairport-sync.service.d")
	_ = os.MkdirAll(d, 0o755)
	_ = os.WriteFile(filepath.Join(d, "jarvis.conf"), []byte("# managed-by: jarvis-package music\n"), 0o644)
	fs.fail = map[string]string{"restart-service": "sudo: a password is required"}
	err := s.RestartUnits(context.Background(), "shairport-sync", "go-librespot")
	if err == nil || !strings.Contains(err.Error(), "shairport-sync") || !strings.Contains(err.Error(), "a password is required") {
		t.Fatalf("err %v", err)
	}
	if got := fs.argvs(); got[len(got)-1] != "systemctl --user restart go-librespot" {
		t.Fatalf("calls:\n%s", strings.Join(got, "\n"))
	}
}
