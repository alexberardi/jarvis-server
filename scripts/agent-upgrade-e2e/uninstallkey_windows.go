package main

import (
	"golang.org/x/sys/windows/registry"

	"github.com/alexberardi/jarvis-server/internal/agent/install"
)

// fakeUninstallEntry stands in for the Windows installer's Apps & features entry (HKCU, the
// runner's own profile), at version v, so the e2e can check the update rewrites DisplayVersion.
// It returns a function that reads DisplayVersion and one that removes the entry.
func fakeUninstallEntry(v string) (read func() string, remove func()) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, install.UninstallKey, registry.SET_VALUE)
	must(err)
	must(k.SetStringValue("DisplayName", "Jarvis Automation Agent"))
	must(k.SetStringValue("DisplayVersion", v))
	k.Close()
	read = func() string {
		k, err := registry.OpenKey(registry.CURRENT_USER, install.UninstallKey, registry.QUERY_VALUE)
		if err != nil {
			return ""
		}
		defer k.Close()
		s, _, _ := k.GetStringValue("DisplayVersion")
		return s
	}
	return read, func() { _ = registry.DeleteKey(registry.CURRENT_USER, install.UninstallKey) }
}
