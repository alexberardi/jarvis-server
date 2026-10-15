//go:build !windows

package main

// fakeUninstallEntry: only Windows has an uninstall entry.
func fakeUninstallEntry(string) (read func() string, remove func()) { return nil, func() {} }
