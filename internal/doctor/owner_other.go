//go:build !unix

package doctor

import "io/fs"

const unixModes = false

func fileOwner(fs.FileInfo) (int, bool) { return 0, false }
