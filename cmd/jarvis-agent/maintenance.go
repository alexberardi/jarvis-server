package main

import (
	"context"
	"flag"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/maintenance"
	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
)

// maintFlags are the Pi's maintenance restart (agent.json's maintenance_restart*, the app's
// maintenance_restart_* node settings).
type maintFlags struct {
	off       bool
	at        string
	ceilingMB int
}

func addMaintenanceFlags(fs *flag.FlagSet) *maintFlags {
	m := &maintFlags{}
	fs.BoolVar(&m.off, "no-maintenance-restart", false, "a Pi doesn't restart itself once a day or when its memory passes the ceiling")
	fs.StringVar(&m.at, "maintenance-restart-at", maintenance.DefaultAt, `a Pi's daily restart, "HH:MM" local time`)
	fs.IntVar(&m.ceilingMB, "maintenance-rss-ceiling-mb", -1,
		"a Pi restarts when its resident memory reaches this many MB (0: never; -1: the larger of 320 MB and 35% of RAM)")
	return m
}

func (m *maintFlags) settings() maintenance.Settings {
	return maintenance.Settings{Enabled: !m.off, At: m.at, CeilingMB: m.ceilingMB}
}

// memTotalMB is the machine's RAM, for the default ceiling the snapshot reports (tests set it).
var memTotalMB = maintenance.TotalMB

// startMaintenance runs a Pi's maintenance restart until ctx ends: when one is due it stops the
// agent (quit), which exits 0 and systemd (Restart=always) starts again. state is the node
// settings as the app last changed them.
func startMaintenance(ctx context.Context, state func() pinode.State, quit func(), log *slog.Logger) {
	r := &maintenance.Restarter{Settings: func() maintenance.Settings { return state().Maintenance }, Log: log,
		Exit: func(reason string) {
			log.Warn("maintenance restart: stopping the agent; systemd starts it again", "reason", reason)
			quit()
		}}
	go r.Run(ctx)
}
