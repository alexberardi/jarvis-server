package browser

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// The extension's source, one Manifest V3 codebase for Firefox and Chromium browsers. Its
// manifest.json carries both browsers' keys; ExtensionFiles strips the other browser's.
//
//go:embed extension
var extensionFS embed.FS

// signed/ holds the Mozilla-signed .xpi (scripts/browser-ext-sign.sh puts it there) when this
// build has one. Release Firefox installs only signed extensions.
//
//go:embed all:signed
var signedFS embed.FS

// Extension targets.
const (
	TargetFirefox  = "firefox"
	TargetChromium = "chromium"
)

// SignedXPIName is the signed Firefox package's file name in signed/.
const SignedXPIName = "jarvis-browser.xpi"

// ExtensionFiles returns the extension for target (TargetFirefox or TargetChromium): file name
// → contents, with manifest.json reduced to that browser's keys.
func ExtensionFiles(target string) (map[string][]byte, error) {
	if target != TargetFirefox && target != TargetChromium {
		return nil, fmt.Errorf("unknown extension target %q (firefox or chromium)", target)
	}
	out := map[string][]byte{}
	err := fs.WalkDir(extensionFS, "extension", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := extensionFS.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("extension", filepath.FromSlash(p))
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	m, err := targetManifest(out["manifest.json"], target)
	if err != nil {
		return nil, err
	}
	out["manifest.json"] = m
	return out, nil
}

func targetManifest(src []byte, target string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(src, &m); err != nil {
		return nil, fmt.Errorf("extension manifest: %w", err)
	}
	bg, _ := m["background"].(map[string]any)
	switch target {
	case TargetFirefox:
		// Firefox runs background.scripts as an event page; "key" and the Chrome minimum are
		// Chrome-only.
		delete(m, "key")
		delete(m, "minimum_chrome_version")
		delete(bg, "service_worker")
	case TargetChromium:
		delete(m, "browser_specific_settings")
		delete(bg, "scripts")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // "<all_urls>", not "\u003call_urls\u003e"
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExtensionVersion is the extension's manifest version.
func ExtensionVersion() string {
	b, err := extensionFS.ReadFile("extension/manifest.json")
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Version
}

// WriteExtension writes the target's files into dir (created 0755). Files already there that
// the extension no longer has are left alone.
func WriteExtension(dir, target string) error {
	files, err := ExtensionFiles(target)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[n], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// SignedXPI returns the embedded Mozilla-signed Firefox package, if this build has one.
func SignedXPI() ([]byte, bool) {
	b, err := signedFS.ReadFile("signed/" + SignedXPIName)
	if err != nil || len(b) == 0 {
		return nil, false
	}
	return b, true
}

// SignedXPIVersion is the version in the embedded signed package's manifest, "" when this build
// has none (or it can't be read).
func SignedXPIVersion() string {
	b, ok := SignedXPI()
	if !ok {
		return ""
	}
	return xpiVersion(b)
}

func xpiVersion(xpi []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(xpi), int64(len(xpi)))
	if err != nil {
		return ""
	}
	f, err := zr.Open("manifest.json")
	if err != nil {
		return ""
	}
	defer f.Close()
	var m struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(f).Decode(&m) != nil {
		return ""
	}
	return m.Version
}

// CompareVersions compares two extension versions (dotted numbers, as manifests use): -1, 0 or
// +1. A missing part counts as 0 ("0.3" == "0.3.0"); a part that isn't a number counts as 0 too.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// FamilyOf is the extension the agent gave the browser behind a host's origin: TargetFirefox
// for the signed package (origin browser@jarvis.agent), TargetChromium for the unpacked folder
// (the development id's origin). "" for anything else, including the Chrome Web Store build,
// which the store keeps current itself.
func FamilyOf(origin string) string {
	switch origin {
	case FirefoxExtensionID:
		return TargetFirefox
	case "chrome-extension://" + ChromeDevExtensionID + "/":
		return TargetChromium
	}
	return ""
}

// AvailableVersion is the newest extension this build can give the browser behind origin: the
// signed package's version for Firefox (release Firefox installs only that), the source's for
// the unpacked Chromium folder. "" when it has none to give.
func AvailableVersion(origin string) string {
	switch FamilyOf(origin) {
	case TargetFirefox:
		return SignedXPIVersion()
	case TargetChromium:
		return ExtensionVersion()
	}
	return ""
}

// Outdated reports whether the browser's installed extension is older than the one this build
// can give it, and that version.
func Outdated(b BrowserInfo) (available string, outdated bool) {
	available = AvailableVersion(b.Origin)
	if available == "" || b.ExtensionVersion == "" {
		return available, false
	}
	return available, CompareVersions(b.ExtensionVersion, available) < 0
}

// InstalledVersion is the version of the extension files in dir (a WriteExtension directory),
// "" when there are none.
func InstalledVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Version
}
