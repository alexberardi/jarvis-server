//go:build nosherpa || !((linux && (amd64 || arm64)) || (darwin && arm64) || (windows && amd64))

package sherpa

import "io/fs"

// No native voice libraries for this platform (or built with -tags nosherpa).
var embeddedLibs fs.FS
