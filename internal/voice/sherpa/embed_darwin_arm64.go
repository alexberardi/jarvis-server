//go:build darwin && arm64 && !nosherpa

package sherpa

import (
	"embed"
	"io/fs"
)

// Populated by scripts/fetch-sherpa-libs.sh darwin-arm64 before building.
//
//go:embed libs/darwin-arm64
var embedded embed.FS

var embeddedLibs fs.FS = embedded
