// Package maintenance is the Pi node's maintenance restart, ported from
// N:services/maintenance_restart_service.py: once a day at a set time, or when the process's
// resident set crosses a ceiling, the agent exits cleanly and systemd (Restart=always) starts it
// again. A Go process fragments far less than CPython, so the ceiling is the belt and the daily
// restart the braces; both are there for parity and the app's settings.
//
// Unlike the Python node, a process started after today's restart time does not restart that
// day (systemd brings it back within the same minute), and the ceiling waits for an hour up (a
// ceiling under the baseline otherwise loops into the unit's start limit). The Go node says
// nothing on boot, so there is no Python-style marker to keep the next start silent.
package maintenance

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings are the node's maintenance_restart_* settings, read at every poll so the app's
// changes apply within a minute.
type Settings struct {
	Enabled bool   // maintenance_restart_enabled
	At      string // maintenance_restart_at_time, "HH:MM" node-local (malformed: DefaultAt)
	// CeilingMB is maintenance_restart_rss_ceiling_mb: 0 is off, below 0 DefaultCeilingMB.
	CeilingMB int
}

const (
	DefaultAt   = "03:00"
	FloorMB     = 320  // the default ceiling's floor (the Pi Zero 2 W's)
	RAMFraction = 0.35 // the default ceiling's share of MemTotal
	// MinUptime is how long the process runs before the RSS ceiling may fire.
	MinUptime = time.Hour
)

// Exit reasons.
const (
	Scheduled  = "scheduled"
	RSSCeiling = "rss_ceiling"
)

// DefaultCeilingMB is max(FloorMB, RAMFraction of totalMB); totalMB 0 is unknown.
func DefaultCeilingMB(totalMB int) int { return max(FloorMB, int(float64(totalMB)*RAMFraction)) }

// Ceiling is s's ceiling in MB with the default resolved (0: off).
func (s Settings) Ceiling(totalMB int) int {
	if s.CeilingMB < 0 {
		return DefaultCeilingMB(totalMB)
	}
	return s.CeilingMB
}

// ParseHHMM reads "HH:MM", 00:00 to 23:59.
func ParseHHMM(s string) (h, m int, ok bool) {
	hs, ms, found := strings.Cut(strings.TrimSpace(s), ":")
	if !found {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// Restarter polls Settings and calls Exit once a restart is due. Only Run's goroutine uses it.
type Restarter struct {
	Settings func() Settings
	// Exit stops the agent cleanly (exit status 0) for the reason given.
	Exit func(reason string)
	Log  *slog.Logger
	Poll time.Duration // 1 minute

	Now     func() time.Time   // time.Now
	RSSMB   func() (int, bool) // this process's VmRSS (/proc/self/status)
	TotalMB func() (int, bool) // MemTotal (/proc/meminfo)

	started  time.Time
	firedDay string // the day the scheduled restart fired, YYYY-MM-DD
	warnedAt string // a malformed time already logged
}

func (r *Restarter) start() {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.RSSMB == nil {
		r.RSSMB = func() (int, bool) { return procKB("/proc/self/status", "VmRSS:") }
	}
	if r.TotalMB == nil {
		r.TotalMB = TotalMB
	}
	if r.started.IsZero() {
		r.started = r.Now()
	}
}

// TotalMB is the machine's MemTotal in MB.
func TotalMB() (int, bool) { return procKB("/proc/meminfo", "MemTotal:") }

// Run polls every Poll until ctx ends or a restart is due (Exit has been called).
func (r *Restarter) Run(ctx context.Context) {
	r.start()
	poll := r.Poll
	if poll <= 0 {
		poll = time.Minute
	}
	r.Log.Info("maintenance restart: watching", "poll", poll)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if r.Check() != "" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Check is one poll: it calls Exit and returns the reason when a restart is due, else "".
func (r *Restarter) Check() string {
	r.start()
	s := r.Settings()
	if !s.Enabled {
		return ""
	}
	now := r.Now()
	total, _ := r.TotalMB()
	if ceiling := s.Ceiling(total); ceiling > 0 {
		if rss, ok := r.RSSMB(); ok && rss >= ceiling && now.Sub(r.started) >= MinUptime {
			r.Log.Warn("maintenance restart: RSS ceiling reached, exiting", "rss_mb", rss, "ceiling_mb", ceiling)
			r.Exit(RSSCeiling)
			return RSSCeiling
		}
	}
	h, m, ok := ParseHHMM(s.At)
	if !ok {
		if r.warnedAt != s.At {
			r.warnedAt = s.At
			r.Log.Warn("maintenance restart: invalid time, using the default", "configured", s.At, "default", DefaultAt)
		}
		h, m, _ = ParseHHMM(DefaultAt)
	}
	day := now.Format(time.DateOnly)
	due := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if r.firedDay == day || now.Hour() != h || now.Minute() != m || !r.started.Before(due) {
		return ""
	}
	r.firedDay = day
	r.Log.Info("maintenance restart: scheduled, exiting", "configured_time", s.At, "local_now", now.Format("15:04"))
	r.Exit(Scheduled)
	return Scheduled
}

// procKB reads a "Key: N kB" line of a /proc file, in MB.
func procKB(path, key string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), key); ok {
			f := strings.Fields(rest)
			if len(f) == 0 {
				return 0, false
			}
			kb, err := strconv.Atoi(f[0])
			if err != nil {
				return 0, false
			}
			return kb / 1024, true
		}
	}
	return 0, false
}
