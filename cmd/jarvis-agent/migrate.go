package main

import (
	"log/slog"
	"os"

	"github.com/alexberardi/jarvis-server/internal/agent/portal"
)

// migrateLegacyID cleans up what a dev install made under the agent's old app id
// (io.jarvis.agent, renamed to net.jarvisautomation.agent before the first release). Today
// that is the GNOME/KDE typing permission: its grant was keyed on the old id, so it is forgotten
// and the user is told to allow it again. The macOS LaunchAgent and app bundle are
// scripts/agent-dev-run.sh's, and it moves them itself.
func migrateLegacyID(log *slog.Logger) {
	did, err := portal.MigrateLegacy(os.Getenv, nil)
	switch {
	case err != nil:
		log.Warn("cleaning up the old app id's typing permission", "err", err)
	case did:
		log.Warn("the agent's app id changed, so the desktop's typing permission must be granted again: run `" + portal.AllowCommand + "`")
	}
}
