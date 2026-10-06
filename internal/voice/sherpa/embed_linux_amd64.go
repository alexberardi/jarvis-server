//go:build linux && amd64 && !nosherpa

package sherpa

import (
	"embed"
	"io/fs"
)

// Populated by scripts/fetch-sherpa-libs.sh linux-amd64 before building.
//
//go:embed libs/linux-amd64
var embedded embed.FS

var embeddedLibs fs.FS = embedded
