package install

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/update"
)

// The login item is registered by whichever bundle ran first, so after an update is installed
// in /Applications an old copy in ~/Downloads (or a dmg's translocated run) still owns it, and
// launchd keeps starting the old copy: at login, and whenever setup or restart kickstarts the
// label (#483). So a starting agent decides what to do about the copy installed in
// /Applications: run here, hand the session over to it, or keep running and re-point the login
// item at this bundle (Plan with Options.Adopt).

// Copy is one copy of the agent app, as the handover decision sees it.
type Copy struct {
	Bundle  string // the .app it runs from, "" when none
	Version string // its release tag (`jarvis-agent version`), "" when unknown
}

// Handover is what a starting agent does about the copy installed in /Applications.
type Handover int

const (
	// RunHere: no reason to move: keep running this copy.
	RunHere Handover = iota
	// AdoptLoginItem: keep running, and re-point the login item at this copy (Plan with
	// Options.Adopt): it is the installed copy opened by hand, or newer than what is
	// installed.
	AdoptLoginItem
	// HandOverToInstalled: an equal-or-newer copy is installed in /Applications: start that
	// one and exit.
	HandOverToInstalled
)

// DecideHandover is the decision for a starting agent (macOS). launchd says launchd started
// this process as the login item: then the login item already points at this bundle, and a
// handover must not re-register it from here (kickstart would just start this copy again).
func DecideHandover(running, installed Copy, launchd bool) Handover {
	if running.Bundle == "" || installed.Bundle == "" {
		return RunHere // not run from a bundle, or nothing installed
	}
	if running.Bundle == installed.Bundle {
		if launchd {
			return RunHere // launchd started the installed copy: all as it should be
		}
		return AdoptLoginItem // opened by hand: the login item follows the installed app
	}
	rv, rok := update.ParseVersion(running.Version)
	iv, iok := update.ParseVersion(installed.Version)
	if !rok || !iok {
		// A dev build never hands over; and an installed copy whose version can't be read
		// may not even run, so nothing is handed to it either.
		return RunHere
	}
	if rv.Compare(iv) > 0 {
		if launchd || translocated(running.Bundle) {
			// launchd started the newest copy there is (run here, don't bounce), or this is
			// macOS's temporary translocated copy, which must not own the login item.
			return RunHere
		}
		return AdoptLoginItem
	}
	return HandOverToInstalled
}

// translocated reports whether bundle is the temporary copy macOS runs a quarantined app from
// (App Translocation).
func translocated(bundle string) bool { return strings.Contains(bundle, "/AppTranslocation/") }

// AppDirs is where the installed app may live, best first.
func AppDirs(e Env) []string {
	return []string{"/Applications", e.join(e.Home, "Applications")}
}

// BundleExe is the agent binary inside bundle.
func BundleExe(bundle string) string {
	return filepath.Join(bundle, "Contents", "MacOS", "jarvis-agent")
}

// FindInstalledApp is the app installed in one of dirs (AppDirs), for the handover decision:
// the first one found, with its version from version (BinaryVersion; the running copy's own
// version is used without asking). A zero Copy when none is installed.
func FindInstalledApp(dirs []string, running Copy, version func(exe string) string) Copy {
	for _, d := range dirs {
		bundle := filepath.Join(d, displayName+".app")
		exe := BundleExe(bundle)
		if _, err := os.Stat(exe); err != nil {
			continue
		}
		if bundle == running.Bundle {
			return Copy{Bundle: bundle, Version: running.Version}
		}
		return Copy{Bundle: bundle, Version: version(exe)}
	}
	return Copy{}
}

// BinaryVersion asks exe for its version (`jarvis-agent version`), "" when it can't say.
func BinaryVersion(exe string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
