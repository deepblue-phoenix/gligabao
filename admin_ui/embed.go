// Package adminui embeds the built admin UI web assets (admin_ui/dist) into the binary.
//
// The assets are produced by `npm run build` in this directory (run_build.sh does it). dist/ holds only a
// .gitkeep on a fresh checkout, so the Go build always works; Available then reports false and the server
// does not register the admin UI handlers.
package adminui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the built web assets rooted at the dist directory.
func Assets() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil { // cannot happen: dist is embedded
		panic(err)
	}
	return sub
}

// Available reports whether the web assets were built into this binary.
func Available() bool {
	_, err := fs.Stat(distFS, "dist/index.html")
	return err == nil
}
