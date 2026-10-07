package service

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PurgePlan is what `jarvisd service uninstall --purge` deletes after the service is gone:
// the data directory, the env file's directory when it lives outside the home (Linux system
// mode: /etc/jarvisd), and the service account when jarvisd created one (Linux `jarvisd`).
type PurgePlan struct {
	Home    string   `json:"home"`
	Extra   []string `json:"extra,omitempty"`
	Account string   `json:"account,omitempty"`
}

// CheckPurgeHome refuses to delete a directory that is not plainly jarvisd's: a relative
// path, a filesystem root, a user's home itself, the legacy stack's ~/.jarvis (00-installers
// §2.0: the naming keeps them apart; this makes it a check too), or a directory that neither
// is named for jarvisd nor holds its database.
func CheckPurgeHome(home string) error {
	clean := filepath.Clean(home)
	switch {
	case home == "" || !filepath.IsAbs(clean):
		return fmt.Errorf("refusing to purge %q: not an absolute path", home)
	case filepath.Dir(clean) == clean:
		return fmt.Errorf("refusing to purge %s: a filesystem root", clean)
	case strings.EqualFold(filepath.Base(clean), ".jarvis"):
		return fmt.Errorf("refusing to purge %s: that is the legacy Jarvis stack's directory, not jarvisd's", clean)
	}
	if h, err := os.UserHomeDir(); err == nil && filepath.Clean(h) == clean {
		return fmt.Errorf("refusing to purge %s: a home directory", clean)
	}
	if !strings.Contains(strings.ToLower(filepath.Base(clean)), Name) {
		if _, err := os.Stat(filepath.Join(clean, "jarvis.db")); err != nil {
			return fmt.Errorf("refusing to purge %s: it is not named for jarvisd and holds no jarvis.db", clean)
		}
	}
	return nil
}

// DirSize is the total size of the regular files under dir (0 when it doesn't exist).
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// removeAll deletes p, treating "already gone" as done.
func removeAll(p string) error {
	if err := os.RemoveAll(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// purgeFiles deletes the plan's home and extra paths, checking the home first.
func purgeFiles(p PurgePlan) error {
	if err := CheckPurgeHome(p.Home); err != nil {
		return err
	}
	for _, path := range append([]string{p.Home}, p.Extra...) {
		if err := removeAll(path); err != nil {
			return err
		}
	}
	return nil
}
