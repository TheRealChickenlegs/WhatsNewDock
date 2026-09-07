// Package webui embeds the built frontend assets so the server binary is
// fully self-contained.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded web assets rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
