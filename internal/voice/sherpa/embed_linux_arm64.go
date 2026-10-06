//go:build linux && arm64 && !nosherpa

package sherpa

import (
	"embed"
	"io/fs"
)

// Populated by scripts/fetch-sherpa-libs.sh linux-arm64 before building.
//
//go:embed libs/linux-arm64
var embedded embed.FS

var embeddedLibs fs.FS = embedded
