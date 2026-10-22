package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
)

// extUpgrader brings an installed browser extension up to the one this build carries (#449).
// Firefox runs the signed package `browser install` gave it, and an unlisted add-on doesn't
// update itself: when Firefox connects with an older one, the agent writes the newer package
// and opens it in Firefox once per version, which shows Firefox's own update prompt. A Chromium
// browser runs the unpacked folder `browser install` wrote: the agent rewrites it when it starts,
// and the browser loads the new files when it restarts (or on Reload in its extensions page).
type extUpgrader struct {
	dir  string // <DataDir>/browser
	log  *slog.Logger
	open func(xpi string) error // openInFirefox; tests fake it
	xpi  func() ([]byte, bool)  // browser.SignedXPI; tests fake it

	mu sync.Mutex
}

// offeredFile remembers the last signed version the agent opened in Firefox, so a Firefox that
// keeps the old extension (the user dismissed the prompt) isn't asked again on every connect.
const offeredFile = "firefox-offered-version"

func newExtUpgrader(log *slog.Logger) *extUpgrader {
	env, err := browserEnv()
	if err != nil {
		return nil
	}
	return &extUpgrader{dir: filepath.Join(env.DataDir, "browser"), log: log, open: openInFirefox, xpi: browser.SignedXPI}
}

// refreshChromium rewrites the unpacked Chromium extension when the folder holds an older one.
func (u *extUpgrader) refreshChromium() {
	cdir := filepath.Join(u.dir, "extension-chromium")
	have := browser.InstalledVersion(cdir)
	if have == "" || browser.CompareVersions(have, browser.ExtensionVersion()) >= 0 {
		return
	}
	if err := browser.WriteExtension(cdir, browser.TargetChromium); err != nil {
		u.log.Warn("browser: couldn't update the Chromium extension folder", "dir", cdir, "err", err)
		return
	}
	u.log.Info("browser: updated the Chromium extension folder; the browser loads it on its next start (or Reload in its extensions page)",
		"dir", cdir, "from", have, "to", browser.ExtensionVersion())
}

// outdated is the hub's OnOutdated.
func (u *extUpgrader) outdated(b browser.BrowserInfo, available string) {
	if browser.FamilyOf(b.Origin) != browser.TargetFirefox {
		return // the Chromium folder was rewritten at start; the browser picks it up itself
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	marker := filepath.Join(u.dir, offeredFile)
	if prev, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(prev)) == available {
		return
	}
	xpi, ok := u.xpi()
	if !ok {
		return
	}
	p := filepath.Join(u.dir, browser.SignedXPIName)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		u.log.Warn("browser: couldn't write the extension update", "err", err)
		return
	}
	if err := os.WriteFile(p, xpi, 0o644); err != nil {
		u.log.Warn("browser: couldn't write the extension update", "err", err)
		return
	}
	if err := u.open(p); err != nil {
		u.log.Warn("browser: couldn't open the extension update in Firefox; open it there and click Update", "xpi", p, "err", err)
		return // not marked: the next connect tries again
	}
	u.log.Info("browser: offered Firefox the extension update", "from", b.ExtensionVersion, "to", available, "xpi", p)
	_ = os.WriteFile(marker, []byte(available+"\n"), 0o644)
}
