//go:build !windows

package blob

import "os"

// On unix, rename over an open file and removing an open file both just work.

func openShared(path string) (*os.File, error) { return os.Open(path) }
func replaceFile(from, to string) error        { return os.Rename(from, to) }
func removeFile(path string) error             { return os.Remove(path) }
