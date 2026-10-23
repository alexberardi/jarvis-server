package main

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
)

// A stale copy of the app keeps launching instead of the installed one (#483): the login item
// is registered by whichever bundle ran first, so after an update is installed in /Applications
// an old copy in ~/Downloads still owns it, and launchd keeps starting the old copy. So on
// macOS a starting agent checks /Applications first (install.DecideHandover): an equal-or-newer
// copy there is handed the start, and the installed copy re-points the login item at itself
// when it wasn't started by launchd.

// launchdJob reports whether launchd started this process as the agent's login item.
func launchdJob() bool { return os.Getenv("XPC_SERVICE_NAME") == appid.ID }

// handoverDecision is install.DecideHandover for this process; installedExe is the agent
// binary inside the installed app ("" when there is none).
func handoverDecision(e install.Env) (install.Handover, string) {
	if e.GOOS != "darwin" {
		return install.RunHere, ""
	}
	if os.Getenv(handedOverEnv) != "" {
		// This copy was started by a handover: it runs, whatever it finds, so a mistake in
		// telling the copies apart can't make them hand the start around forever.
		os.Unsetenv(handedOverEnv)
		return install.RunHere, ""
	}
	running := install.Copy{Bundle: install.BundleOf(e.Exe), Version: version}
	if running.Bundle == "" {
		return install.RunHere, "" // a bare binary (dev build, Pi profile) stays put
	}
	installed := install.FindInstalledApp(install.AppDirs(e), running, install.BinaryVersion)
	if installed.Bundle == "" {
		return install.RunHere, ""
	}
	return install.DecideHandover(running, installed, launchdJob()), install.BundleExe(installed.Bundle)
}

// handedOverEnv marks the installed copy a handover spawns: it runs and never hands over again.
const handedOverEnv = "JARVIS_AGENT_HANDED_OVER"

// handoverEnv is the environment the installed copy is spawned with. launchd's job marker is
// dropped: inherited, the spawned copy would think launchd started *it*, skip adopting the
// login item, and pick the wrong log target. And handedOverEnv is set.
func handoverEnv() {
	os.Unsetenv("XPC_SERVICE_NAME")
	os.Setenv(handedOverEnv, "1")
}

// adoptLoginItem re-points the login item (or the plain LaunchAgent's program path) at this
// copy of the app.
func adoptLoginItem(ctx context.Context, e install.Env, s install.Sys) error {
	ops, err := install.Plan(e, install.Options{Adopt: true})
	if err != nil {
		return err
	}
	return install.Apply(ctx, ops, s, install.ApplyOptions{})
}

// runHandover is `run`'s side of it, before anything else is set up. done: an equal-or-newer
// copy is installed, its own `run` was spawned (directly, never through launchd: the login item
// may still point here) and this process exits cleanly, which launchd doesn't restart.
// adopt: keep running, but re-point the login item at this copy once the control socket is held
// (adoptIfInstalled); by then this agent is the only one of this user.
func runHandover(log *slog.Logger) (done, adopt bool) {
	e, err := installEnv()
	if err != nil {
		return false, false
	}
	d, exe := handoverDecision(e)
	switch d {
	case install.HandOverToInstalled:
		log.Info("an equal-or-newer copy is installed; handing over to it", "to", exe, "running", e.Exe)
		s, err := installSys(io.Discard)
		if err == nil {
			handoverEnv()
			err = s.Spawn(exe, "run")
		}
		if err != nil {
			log.Warn("couldn't start the installed copy; running here", "err", err)
			return false, false
		}
		return true, false
	case install.AdoptLoginItem:
		return false, true
	}
	return false, false
}

// adoptIfInstalled re-points the login item at this copy, best effort: the agent runs either
// way, and a copy that was never installed for this user is left to `start`'s install.
func adoptIfInstalled(ctx context.Context, log *slog.Logger) {
	e, err := installEnv()
	if err != nil {
		return
	}
	s, err := installSys(io.Discard)
	if err != nil {
		return
	}
	if !install.Installed(e, s) {
		return
	}
	if err := adoptLoginItem(ctx, e, s); err != nil {
		log.Warn("couldn't re-point the login item at this copy", "err", err)
		return
	}
	log.Info("login item re-pointed at this copy", "bundle", install.BundleOf(e.Exe))
}
