// Package cc is the command-center module of jarvisd.
package cc

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations returns the module's goose migrations (module name "cc").
// docs/schema/cc.md describes the schema.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	return sub
}
