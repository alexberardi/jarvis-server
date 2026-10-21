package config

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// storedLegacy is the system row of server.legacy_ports, if any.
func storedLegacy(t *testing.T, d *db.DB) (any, bool) {
	t.Helper()
	svc, err := settings.New(d, "config", Definitions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, found, err := svc.GetExact(context.Background(), "server.legacy_ports", settings.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	return v, found
}

func setLegacy(t *testing.T, d *db.DB, on bool) {
	t.Helper()
	svc, _ := settings.New(d, "config", Definitions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := svc.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Set(context.Background(), "server.legacy_ports", on, settings.Scope{}); err != nil {
		t.Fatal(err)
	}
}

// synced is a registry an earlier jarvisd wrote: an upgrade.
func synced(t *testing.T, d *db.DB) {
	t.Helper()
	_, err := d.Write.Exec(`INSERT INTO config_services (name, host, port, scheme, health_path, description)
		VALUES ('jarvis-command-center', 'localhost', 7703, 'http', '/health', 'served by jarvisd')`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyPortsDecision(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name    string
		prep    func(t *testing.T, d *db.DB)
		env     string
		want    bool
		wantErr bool
		stored  any // the system row afterwards; nil = leave unchecked
	}{
		{name: "fresh DB is off and stored", want: false, stored: false},
		{name: "synced rows are an upgrade: on and stored", prep: synced, want: true, stored: true},
		{name: "stored false wins over synced rows", prep: func(t *testing.T, d *db.DB) {
			synced(t, d)
			setLegacy(t, d, false)
		}, want: false, stored: false},
		{name: "env 1 over stored false", prep: func(t *testing.T, d *db.DB) { setLegacy(t, d, false) },
			env: "1", want: true, stored: false},
		{name: "env 0 over stored true", prep: func(t *testing.T, d *db.DB) { setLegacy(t, d, true) },
			env: "0", want: false, stored: true},
		{name: "bad env value", env: "maybe", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := openDB(t)
			if c.prep != nil {
				c.prep(t, d)
			}
			got, err := LegacyPorts(ctx, d, log, c.env)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.wantErr {
				return
			}
			if got != c.want {
				t.Fatalf("LegacyPorts = %v, want %v", got, c.want)
			}
			if c.stored != nil {
				v, found := storedLegacy(t, d)
				if !found || v != c.stored {
					t.Fatalf("stored = %v (found %v), want %v", v, found, c.stored)
				}
			}
		})
	}
}
