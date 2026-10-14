package service

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// RefreshResult is what RefreshDefinition did (01-no-cli-plan item 1.3): the privileged
// upgrade helper, right after it installed a release it verified, has that release rewrite the
// service definitions it renders differently, so an admin-button upgrade doesn't leave one
// stale until someone runs `sudo jarvisd service install`.
type RefreshResult struct {
	// Changed lists what was rewritten: definition files, or Windows service names.
	Changed []string `json:"changed,omitempty"`
	// Reload: the jarvisd definition changed and the restart that follows must load it
	// (launchd: bootout + bootstrap, since kickstart keeps the loaded plist). systemd and the
	// SCM need nothing more: their restart reads the new definition.
	Reload bool `json:"reload,omitempty"`
	// Note says when a change applies, or what to run when it couldn't be applied.
	Note string `json:"note,omitempty"`
}

// replaceFile writes data to path atomically with exactly perm: a fresh temporary file
// beside it (created exclusively, so nothing planted there is written through), synced, then
// renamed over path. Definitions live in root-owned directories; a crash mid-write must not
// leave a half-written one.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".jarvisd-new"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, perm)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}
