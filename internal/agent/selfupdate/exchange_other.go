//go:build !darwin

package selfupdate

import "errors"

// exchange: only macOS replaces a directory (the app bundle); elsewhere swapDirs renames twice.
func exchange(string, string) error { return errors.ErrUnsupported }
