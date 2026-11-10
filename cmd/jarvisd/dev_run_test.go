package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestDevRunMigrationFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dev-run supports Linux and macOS")
	}
	script, err := os.ReadFile("../../scripts/dev-run.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual startup/restore sequence, without building a binary or
	// touching a real service. Only its external service and binary calls are faked.
	_, startup, found := strings.Cut(string(script), "# Stop the installed service;")
	if !found {
		t.Fatal("dev-run startup section missing")
	}
	startup = "# Stop the installed service;" + startup
	for _, active := range []string{"0", "1"} {
		t.Run("active="+active, func(t *testing.T) {
			prelude := `set -euo pipefail
MODE=system HOMEDIR=/unused OWNHOME= BIN=stub VERSION=test
say() { echo "dev-run: $*" >&2; }
die() { say "$*"; exit 1; }
systemctl() { [ "$ACTIVE" = 1 ]; }
sudo() { echo "service: $*"; }
as() { echo "migration failed: duplicate version 360" >&2; return 1; }
`
			cmd := exec.Command("bash", "-c", prelude+startup)
			cmd.Env = append(os.Environ(), "ACTIVE="+active)
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatal("migration failure must prevent startup")
			}
			text := string(output)
			if !strings.Contains(text, "migration failed: duplicate version 360") {
				t.Errorf("migration diagnostic hidden: %s", text)
			}
			if strings.Contains(text, "dev-run: running") {
				t.Errorf("started after migration failure: %s", text)
			}
			restored := strings.Contains(text, "service: systemctl start jarvisd")
			if restored != (active == "1") {
				t.Errorf("incorrect service restoration: %s", text)
			}
		})
	}
}
