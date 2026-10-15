//go:build !windows

package service

// RecordInstalledVersion: only Windows has an installer's uninstall entry.
func RecordInstalledVersion(string) (bool, error) { return false, nil }
