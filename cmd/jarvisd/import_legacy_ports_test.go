package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// An import means legacy clients exist, so it leaves the legacy ports on: into a fresh home,
// and into one where jarvisd already started once on the empty database (the documented
// migration order), which stored server.legacy_ports=false.
func TestImportLegacyTurnsLegacyPortsOn(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	legacyPorts := func(t *testing.T, home string) bool {
		t.Helper()
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		d, err := migratedDB(ctx, cfg, filepath.Join(home, "jarvis.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		on, err := configmod.LegacyPorts(ctx, d, quiet, "")
		if err != nil {
			t.Fatal(err)
		}
		return on
	}
	for _, startedOnce := range []bool{false, true} {
		name := map[bool]string{false: "fresh home", true: "jarvisd started once"}[startedOnce]
		t.Run(name, func(t *testing.T) {
			home := legacyEnv(t)
			if startedOnce && legacyPorts(t, home) {
				t.Fatal("a fresh database decided legacy ports on")
			}
			var out bytes.Buffer
			if err := run(ctx, []string{"import-legacy", "--from", "postgres://jarvis:x@127.0.0.1:5432", "--apply"}, &out); err != nil {
				t.Fatalf("apply: %v\n%s", err, out.String())
			}
			if !strings.Contains(out.String(), configmod.LegacyPortsKey+" = true") {
				t.Fatalf("the report doesn't say legacy ports are on:\n%s", out.String())
			}
			if !legacyPorts(t, home) {
				t.Fatal("after import-legacy --apply, jarvisd's next start decides legacy_ports=false (want true)")
			}
		})
	}
}
