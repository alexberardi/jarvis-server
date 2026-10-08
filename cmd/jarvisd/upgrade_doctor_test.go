package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/doctor"
)

// A10d V1: v0.1.0-rc3 added the recipes listener on 7030; the upgrade from rc2 said "up and
// healthy" while ufw still dropped 7030 from the LAN, and only `jarvisd doctor` showed it.
func TestReportDoctor(t *testing.T) {
	checks := []doctor.Check{
		{Name: "listening", Status: doctor.OK, Detail: "jarvisd answers on all 13 TCP ports"},
		{Name: "firewall 10.0.0.0/24", Status: doctor.Fail, Detail: "ufw drops 7030/tcp from 10.0.0.0/24",
			Fix:     "sudo ufw allow from 10.0.0.0/24 to any port 7030 proto tcp comment jarvisd",
			FixCmds: [][]string{{"ufw", "allow", "from", "10.0.0.0/24", "to", "any", "port", "7030", "proto", "tcp", "comment", "jarvisd"}}},
		{Name: "gpu memory", Status: doctor.Warn, Detail: "tight"},
	}
	out, _ := json.Marshal(checks)
	var b strings.Builder
	reportDoctor(out, &b)
	got := b.String()
	for _, want := range []string{
		"jarvisd doctor found problems:\n",
		"FAIL  firewall 10.0.0.0/24: ufw drops 7030/tcp from 10.0.0.0/24\n",
		"      fix:\n        sudo ufw allow from 10.0.0.0/24 to any port 7030 proto tcp comment jarvisd\n",
		"jarvisd doctor --fix` applies the firewall fix\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "listening") || strings.Contains(got, "gpu memory") {
		t.Errorf("only failed checks are reported:\n%s", got)
	}

	// All passing, or no JSON (a binary that can't run its doctor): nothing.
	for _, in := range [][]byte{mustJSON(t, checks[:1]), nil, []byte("doctor: unknown flag")} {
		b.Reset()
		reportDoctor(in, &b)
		if b.Len() != 0 {
			t.Errorf("%q: printed %q", in, b.String())
		}
	}
}

// doctorAfter runs the binary at exe (the version now installed), whose doctor exits 1 when a
// check fails.
func TestDoctorAfterRunsInstalledBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	out := mustJSON(t, []doctor.Check{{Name: "firewall 10.0.0.0/24", Status: doctor.Fail, Detail: "ufw drops 7030/tcp"}})
	exe := filepath.Join(t.TempDir(), "jarvisd")
	script := "#!/bin/sh\n[ \"$1 $2\" = \"doctor --json\" ] || exit 2\ncat <<'EOF'\n" + string(out) + "\nEOF\nexit 1\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	doctorAfter(context.Background(), exe, &b)
	if !strings.Contains(b.String(), "FAIL  firewall 10.0.0.0/24: ufw drops 7030/tcp\n") {
		t.Fatalf("%q", b.String())
	}
	b.Reset()
	doctorAfter(context.Background(), filepath.Join(t.TempDir(), "missing"), &b)
	if b.Len() != 0 {
		t.Fatalf("a binary that can't run: %q", b.String())
	}
}

// A10d V3: rc3 → rc2 by hand left a database rc3 had migrated (recipes) and rc2 started fine,
// although the note said a migrated database makes jarvisd refuse to start.
func TestRestoredNote(t *testing.T) {
	got := restoredNote("/b/jarvisd.prev", "/h/backups")
	for _, want := range []string{
		"restored /b/jarvisd.prev; the database is unchanged.",
		"Tables of modules this version doesn't have stay for the next upgrade",
		"if the newer version migrated a module it does have, jarvisd refuses to start",
		"restore a snapshot from /h/backups\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
