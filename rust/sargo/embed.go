// Package sargoembed embeds sargo into the gligabao binary: the default config (config.toml of this directory, so there is one
// copy only) and the compiled static sargo binary (bin/sargo).
//
// bin/sargo is put there by `./run_build_rust.sh --static --embed` (run_build.sh --sargo does it). bin/ holds only a .gitkeep on a
// fresh checkout, so the Go build always works; Available then reports false. internal/sargo runs the embedded binary as a server.
package sargoembed

import (
	"embed"
	"io/fs"
)

// Config is the default sargo config, the contents of config.toml.
//
//go:embed config.toml
var Config string

//go:embed all:bin
var binFS embed.FS

const binaryPath = "bin/sargo"

// Available reports whether the sargo binary was built into this gligabao binary.
func Available() bool {
	_, err := fs.Stat(binFS, binaryPath)
	return err == nil
}

// Binary returns the embedded sargo executable (a static Linux binary of the architecture gligabao was built on).
func Binary() ([]byte, error) {
	return binFS.ReadFile(binaryPath)
}
