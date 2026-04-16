// Package ui provides the embedded SvelteKit frontend.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:build
var embedded embed.FS

// FS returns the SvelteKit build filesystem rooted at the static output directory.
// Safe to pass directly to http.FS for static file serving.
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "build")
}
