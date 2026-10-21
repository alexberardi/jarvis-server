package main

import (
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// handOff is the command that starts the agent in place of the waiting login item, or nil to
// exec in place (the same PID, so systemd keeps supervising it). job is XPC_SERVICE_NAME, the
// launchd label this process runs under.
//
// macOS never execs in place (#407): the exec'd image sets AppKit up again in a process that
// already checked in with the window server, and its menu-bar icon never shows. Under the
// agent's own job launchd restarts it, a fresh process like the one a later login starts
// (kickstart -k: this one gets SIGTERM, or has exited 0 by then); elsewhere a fresh `run` is
// started detached, as on Windows.
func handOff(goos, job string, uid int, exe string, args []string) []string {
	switch {
	case goos == "darwin" && job == appid.ID:
		return []string{"launchctl", "kickstart", "-k", "gui/" + strconv.Itoa(uid) + "/" + appid.ID}
	case goos == "darwin":
		return append([]string{exe}, args...)
	}
	return nil
}
