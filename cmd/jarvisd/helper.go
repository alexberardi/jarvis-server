package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// The privileged upgrade helper (ID11, 00-installers §8.2): `jarvisd upgrade --prestart`
// (the systemd unit's root ExecStartPre) and `jarvisd upgrade --helper` (the root macOS
// LaunchDaemon and the LocalSystem Windows service). Both run as an administrator on behalf
// of the unprivileged service, so they take nothing from the data directory on trust (see
// update.PrivilegedStep): in particular they never load its jarvisd.env, and never exec a
// program found through PATH.

// runHelper is the helper's entry point. home is the --home the service definition passes
// (never the environment's); owner the service account the home must belong to (unix).
// onDemand is --helper: wake-up driven, it restarts jarvisd after changing its binary.
func runHelper(ctx context.Context, home, owner string, onDemand bool, w io.Writer) error {
	tag := "--prestart"
	if onDemand {
		tag = "--helper"
	}
	say := func(format string, a ...any) {
		fmt.Fprintf(w, "%s jarvisd upgrade %s: %s\n", time.Now().UTC().Format(time.RFC3339), tag, fmt.Sprintf(format, a...))
	}
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("the upgrade helper needs an absolute --home")
	}
	exe, err := selfExe()
	if err != nil {
		return err
	}
	o := update.HelperOptions{Version: version}
	if owner != "" && runtime.GOOS != "windows" {
		uid, err := lookupUID(owner)
		if err != nil {
			say("%v", err)
			return nil
		}
		o.OwnerUID, o.CheckOwner = uid, true
	}
	paths := update.Paths{Home: home, Exe: exe}
	if !onDemand {
		// The systemd pre-start never fails the start: report and let serve run.
		action, m, err := update.PrivilegedStep(ctx, paths, o)
		reportStep(say, action, m, err)
		return nil
	}
	// On demand: a request that arrives while this runs is picked up by another round.
	for round := 0; round < 3; round++ {
		if runtime.GOOS == "darwin" {
			if _, err := update.DrainRequests(paths, o); err != nil {
				say("%v", err)
			}
		}
		admitted := appPermitted(ctx, exe)
		action, m, err := update.PrivilegedStep(ctx, paths, o)
		reportStep(say, action, m, err)
		if action != update.ActionSwapped && action != update.ActionBinaryRestored {
			return nil
		}
		if admitted && !appPermitted(ctx, exe) {
			// macOS: the new build has another ad-hoc signature; keep it admitted.
			say("re-admitting %s to the application firewall", exe)
			if err := readmitApp(ctx, exe); err != nil {
				say("firewall: %v; run `sudo jarvisd doctor --fix`", err)
			}
		}
		say("restarting jarvisd")
		if err := service.RestartFromHelper(ctx); err != nil {
			say("restart failed: %v", err)
			return nil
		}
		if runtime.GOOS == "windows" {
			update.CleanupOld(paths) // the old exe is free once jarvisd stopped
		}
	}
	return nil
}

func reportStep(say func(string, ...any), action string, m *update.Marker, err error) {
	switch {
	case action == update.ActionSwapped:
		say("installed %s (was %s)", m.To, m.From)
	case action == update.ActionBinaryRestored:
		say("restored the previous binary (rolling back %s to %s)", m.To, m.From)
	case action == update.ActionRefused:
		say("refused %s: %v", m.To, err)
	case err != nil:
		say("%v", err)
	}
}

// lookupUID resolves --owner (an account name or a numeric uid).
func lookupUID(owner string) (int, error) {
	if n, err := strconv.Atoi(owner); err == nil {
		return n, nil
	}
	u, err := user.Lookup(owner)
	if err != nil {
		return 0, fmt.Errorf("--owner %s: %w", owner, err)
	}
	return strconv.Atoi(u.Uid)
}

// helperWait bounds how long jarvisd waits for an on-demand helper to act.
const helperWait = 3 * time.Minute

// triggerHelper wakes an on-demand helper: a request file for launchd, a service start for the
// SCM.
func triggerHelper(ctx context.Context, p update.Paths, h service.Helper) error {
	switch h {
	case service.HelperLaunchd:
		return update.Request(p)
	case service.HelperSCM:
		return service.StartHelper(ctx)
	}
	return fmt.Errorf("no on-demand upgrade helper (%q)", h)
}

// awaitHelper wakes the on-demand helper and waits until it has moved the upgrade on from
// state (the helper restarts jarvisd next, which may end this process first). It reports
// whether serve should exit now: the helper acted, or a stop arrived; false means it timed out.
func awaitHelper(ctx context.Context, log *slog.Logger, p update.Paths, h service.Helper, state string) bool {
	log.Info("upgrade: asking the privileged helper", "helper", h, "state", state)
	deadline := time.Now().Add(helperWait)
	var next time.Time
	for time.Now().Before(deadline) {
		if time.Now().After(next) {
			if err := triggerHelper(ctx, p, h); err != nil {
				log.Error("upgrade: waking the helper failed", "err", err)
			}
			next = time.Now().Add(15 * time.Second)
		}
		if m, err := update.ReadMarker(p); err == nil && (m == nil || m.State != state) {
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(250 * time.Millisecond):
		}
	}
	log.Error("upgrade: the privileged helper did not act", "helper", h, "waited", helperWait, "log", helperLogHint(p, h))
	return false
}

// helperLogHint says where the helper logs.
func helperLogHint(p update.Paths, h service.Helper) string {
	switch h {
	case service.HelperLaunchd:
		return service.HelperLog
	case service.HelperSCM:
		return service.HelperLogPath(p.Exe)
	}
	return "journalctl -u jarvisd"
}

// The macOS application firewall keys its allow entry on the binary's signature (§8.2), so
// the helper re-admits a swapped binary that replaced an admitted one. By absolute path, and
// nothing else from the doctor: the helper runs as root.
const socketfilterfw = "/usr/libexec/ApplicationFirewall/socketfilterfw"

func appPermitted(ctx context.Context, exe string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	out, err := exec.CommandContext(ctx, socketfilterfw, "--getappblocked", exe).CombinedOutput()
	return err == nil && strings.Contains(strings.ToLower(string(out)), "permitted")
}

func readmitApp(ctx context.Context, exe string) error {
	for _, flag := range []string{"--add", "--unblockapp"} {
		if out, err := exec.CommandContext(ctx, socketfilterfw, flag, exe).CombinedOutput(); err != nil {
			return fmt.Errorf("%s %s: %w: %s", flag, exe, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// helperArgs reports whether args (after --home) run the on-demand helper, which the Windows
// SCM starts as its own service.
func helperArgs(args []string) bool {
	if len(args) == 0 || args[0] != "upgrade" {
		return false
	}
	for _, a := range args[1:] {
		if a == "--helper" || a == "-helper" {
			return true
		}
	}
	return false
}

// runWindowsHelper is main for the Windows updater service: its log goes next to the binary
// (not into the home, which the service account can write).
func runWindowsHelper(flagHome string, args []string) int {
	if exe, err := selfExe(); err == nil {
		_ = service.RedirectStderr(service.HelperLogPath(exe))
	}
	err := service.RunHelperService(func(ctx context.Context) error {
		return runUpgrade(ctx, flagHome, args[1:], os.Stderr)
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "jarvisd:", err)
	}
	return service.ExitCode(err)
}
