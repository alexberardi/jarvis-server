// Package ocr is the jarvisd ocr module. For now it holds only the schema baseline.
package ocr

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the module's goose migrations, rooted at the migrations directory.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}
