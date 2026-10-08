//go:build !unix

package main

import "io/fs"

func homeOwner(fs.FileInfo) (int, bool) { return 0, false }
