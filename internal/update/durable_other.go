//go:build !windows

package update

import (
	"errors"
	"os"
)

func syncDirOS(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// syncRootDir is syncDir for a directory inside root (the privileged helper's home).
func syncRootDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}
