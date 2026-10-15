package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisplayVersion(t *testing.T) {
	for in, want := range map[string]string{"v0.2.0": "0.2.0", "v0.3.0-rc1": "0.3.0-rc1", "dev": "dev"} {
		if got := displayVersion(in); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// The Windows installer's AppId names the uninstall entry the updater rewrites.
func TestWindowsInstallerAppID(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "packaging", "windows", "server.iss"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `#define AppId "` + LaunchdLabel + `"`; !strings.Contains(string(b), want) {
		t.Errorf("server.iss doesn't have %q", want)
	}
}
