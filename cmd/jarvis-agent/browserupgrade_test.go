package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
)

// An outdated Firefox is offered the signed package once per version: the agent writes it and
// opens it in Firefox, and doesn't reopen it on the next connect. A newer package is offered
// again; a Chromium browser is never handed the .xpi.
func TestExtUpgraderOffersFirefoxOncePerVersion(t *testing.T) {
	dir := t.TempDir()
	var opened []string
	u := &extUpgrader{dir: dir, log: slog.New(slog.DiscardHandler),
		open: func(p string) error { opened = append(opened, p); return nil },
		xpi:  func() ([]byte, bool) { return []byte("PK-signed"), true }}
	ff := browser.BrowserInfo{Browser: "Firefox", Origin: browser.FirefoxExtensionID, ExtensionVersion: "0.1.0"}

	u.outdated(ff, "0.3.4")
	u.outdated(ff, "0.3.4")
	if len(opened) != 1 || opened[0] != filepath.Join(dir, browser.SignedXPIName) {
		t.Fatalf("opened %v, want the package once", opened)
	}
	if b, _ := os.ReadFile(opened[0]); string(b) != "PK-signed" {
		t.Errorf("wrote %q", b)
	}
	u.outdated(ff, "0.3.5")
	if len(opened) != 2 {
		t.Errorf("a newer version wasn't offered: %v", opened)
	}
	u.outdated(browser.BrowserInfo{Browser: "Google Chrome", Origin: "chrome-extension://" + browser.ChromeDevExtensionID + "/", ExtensionVersion: "0.1.0"}, "0.3.6")
	if len(opened) != 2 {
		t.Errorf("Chromium was handed the .xpi: %v", opened)
	}
}

// At start the agent rewrites an unpacked Chromium folder that holds an older extension, and
// leaves a missing folder (Chromium never installed) alone.
func TestExtUpgraderRefreshesChromiumFolder(t *testing.T) {
	dir := t.TempDir()
	u := &extUpgrader{dir: dir, log: slog.New(slog.DiscardHandler)}
	u.refreshChromium()
	if _, err := os.Stat(filepath.Join(dir, "extension-chromium")); !os.IsNotExist(err) {
		t.Fatalf("created a folder nobody installed: %v", err)
	}
	cdir := filepath.Join(dir, "extension-chromium")
	if err := os.MkdirAll(cdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cdir, "manifest.json"), []byte(`{"version": "0.1.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u.refreshChromium()
	if v := browser.InstalledVersion(cdir); v != browser.ExtensionVersion() {
		t.Errorf("folder is %q after the refresh, want %q", v, browser.ExtensionVersion())
	}
}

// browser status says when the signed Firefox package is behind the source, and per connected
// browser which newer extension it can have.
func TestBrowserStatusVersions(t *testing.T) {
	if got := extensionLine("0.3.4", "0.1.0"); !strings.Contains(got, "signed Firefox package 0.1.0, behind the source") {
		t.Errorf("behind: %q", got)
	}
	if got := extensionLine("0.3.4", "0.3.4"); got != "Extension 0.3.4: signed Firefox package 0.3.4 included" {
		t.Errorf("current: %q", got)
	}
	if got := extensionLine("0.3.4", ""); !strings.Contains(got, "no signed Firefox package") {
		t.Errorf("none: %q", got)
	}
	got := liveText(browser.StatusReply{Browsers: []browser.BrowserInfo{
		{Browser: "Google Chrome", Version: "130", ExtensionVersion: "0.1.0", Origin: "chrome-extension://" + browser.ChromeDevExtensionID + "/"},
	}})
	if want := "Google Chrome 130 (extension 0.1.0), update available: " + browser.ExtensionVersion(); !strings.Contains(got, want) {
		t.Errorf("status lacks %q:\n%s", want, got)
	}
}
