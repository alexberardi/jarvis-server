// Package adminui embeds the admin SPA (web/admin) into jarvisd.
//
// The Vite build writes to dist/ui, which is gitignored; dist/placeholder.html is committed.
// So `go build` works without Node and embeds only the placeholder, and a release build
// runs `npm run build` first and embeds the real UI (the release_ui test enforces that).
package adminui

import (
	"embed"
	"io/fs"
)

// all: keeps files whose names start with _ or ., which Vite may emit.
//
//go:embed all:dist
var dist embed.FS

// UI returns the built SPA rooted at dist/ui and true, or nil and false when the binary was
// built without it (only the placeholder is embedded).
func UI() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist/ui")
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// Placeholder is the page served for every UI path when the SPA was not built.
func Placeholder() []byte {
	b, err := dist.ReadFile("dist/placeholder.html")
	if err != nil {
		panic(err) // unreachable: the file is committed and embedded
	}
	return b
}
