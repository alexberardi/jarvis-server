package service

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// RecordInstalledVersion sets the Windows installer's uninstall entry's DisplayVersion to tag
// once the updater has put that release in place, so Apps & features shows the version that
// runs, not the installer's. It needs the administrator rights the updater runs with. Nothing
// is written when no installer made the entry (install.ps1, the archive).
func RecordInstalledVersion(tag string) (bool, error) {
	return recordVersion(registry.LOCAL_MACHINE, UninstallKey, tag)
}

func recordVersion(root registry.Key, path, tag string) (bool, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer k.Close()
	if _, _, err := k.GetStringValue("DisplayName"); errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return true, k.SetStringValue("DisplayVersion", displayVersion(tag))
}
