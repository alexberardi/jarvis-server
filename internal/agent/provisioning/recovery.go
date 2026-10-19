package provisioning

import (
	"context"
	"log/slog"
	"strconv"
	"time"
)

// Recovery is the AP↔STA recovery cycle (N:provisioning/recovery_watcher.py), for a node that
// was provisioned before and is back in setup mode. Inside AP mode no probe can see the home
// network come back (the hotspot's captive DNS answers every name with the node itself, which
// is how the kitchen node stayed stranded on 2026-07-05), so it alternates physically: the
// hotspot for RetryInterval, so a user who changed their Wi-Fi can pair; then, unless a phone is
// mid-attempt, the hotspot goes down for STAWindow while NM autoconnects to the committed
// profile. Joined: reboot into normal operation. Not joined: the hotspot again, forever.
type Recovery struct {
	WiFi    WiFi
	APSSID  string
	Pairing func() bool  // a provisioning attempt is in flight: keep the hotspot
	Reboot  func() error // after rejoining
	Log     *slog.Logger

	RetryInterval time.Duration // hotspot time between STA retries
	STAWindow     time.Duration // how long a retry waits for the join
	Poll          time.Duration // between looks inside the window

	sleep func(ctx context.Context, d time.Duration) bool // false once ctx ends; tests fake it
}

// RecoveryFromEnv reads N:'s knobs (the config service isn't reachable in AP mode), with its
// defaults and floors: JARVIS_RECOVERY_RETRY_INTERVAL (600 s, ≥30), _STA_WINDOW (120 s, ≥15),
// _POLL_SECONDS (5 s, ≥1). False when JARVIS_RECOVERY_DISABLED is set.
func RecoveryFromEnv(getenv func(string) string) (Recovery, bool) {
	if getenv("JARVIS_RECOVERY_DISABLED") != "" {
		return Recovery{}, false
	}
	secs := func(key string, def, floor float64) time.Duration {
		v, err := strconv.ParseFloat(getenv(key), 64)
		if err != nil {
			v = def
		}
		return time.Duration(max(v, floor) * float64(time.Second))
	}
	return Recovery{
		RetryInterval: secs("JARVIS_RECOVERY_RETRY_INTERVAL", 600, 30),
		STAWindow:     secs("JARVIS_RECOVERY_STA_WINDOW", 120, 15),
		Poll:          secs("JARVIS_RECOVERY_POLL_SECONDS", 5, 1),
	}, true
}

// Run cycles until ctx ends or the node rejoined and Reboot was called.
func (r Recovery) Run(ctx context.Context) {
	log := r.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sleep := r.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	log.Info("provisioning: AP/STA recovery cycle started", "ap_ssid", r.APSSID,
		"retry_interval", r.RetryInterval, "sta_window", r.STAWindow)
	for {
		if !sleep(ctx, r.RetryInterval) {
			return
		}
		if r.Pairing != nil && r.Pairing() {
			log.Info("provisioning: STA retry skipped, a pairing attempt is in progress")
			continue
		}
		log.Info("provisioning: dropping the hotspot to retry the known Wi-Fi", "sta_window", r.STAWindow)
		if err := r.WiFi.StopAPMode(ctx); err != nil {
			log.Error("provisioning: stopping the hotspot for the STA retry", "err", err)
			continue
		}
		joined := ""
		for waited := time.Duration(0); waited < r.STAWindow; waited += r.Poll {
			if ssid, err := r.WiFi.CurrentSSID(ctx); err == nil && ssid != "" {
				joined = ssid
				break
			}
			if !sleep(ctx, r.Poll) {
				return
			}
		}
		if joined != "" {
			log.Warn("provisioning: known Wi-Fi rejoined, rebooting into normal operation", "ssid", joined)
			if r.Reboot != nil {
				if err := r.Reboot(); err != nil {
					log.Error("provisioning: reboot", "err", err)
				}
			}
			return
		}
		log.Info("provisioning: known Wi-Fi still unavailable, restoring the hotspot")
		if err := r.WiFi.StartAPMode(ctx, r.APSSID); err != nil {
			log.Error("provisioning: restoring the hotspot after the STA retry", "err", err)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
