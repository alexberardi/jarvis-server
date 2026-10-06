//go:build windows && amd64 && !nosherpa

package sherpa

import (
	"embed"
	"io/fs"
)

// Populated by scripts/fetch-sherpa-libs.sh windows-amd64 before building.
//
//go:embed libs/windows-amd64
var embedded embed.FS

var embeddedLibs fs.FS = embedded
