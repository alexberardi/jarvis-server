package service

import "strings"

// UninstallKey is the Windows installer's Apps & features entry (HKLM; Inno Setup names it
// after its AppId, LaunchdLabel, plus "_is1"; scripts/packaging/windows/server.iss).
const UninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\` + LaunchdLabel + `_is1`

// displayVersion is a release tag as Apps & features shows it: v0.2.0 → 0.2.0.
func displayVersion(tag string) string { return strings.TrimPrefix(tag, "v") }
