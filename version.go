package gligabao

import (
	_ "embed"

	"github.com/deepblue-phoenix/gligabao/internal/buildinfo"
)

// VERSION is embedded at build time. It is edited with every change together with CHANGELOG.md, see AGENTS.md.

//go:embed VERSION
var versionFile string

// App describes the program that embeds gligabao, so that the server can report its version next to gligabao's.
type App struct {
	Name    string
	Version string
	Commit  string // optional; see Commit()
}

func init() {
	buildinfo.Set(buildinfo.Component{
		Name:    "gligabao",
		Version: buildinfo.Parse(versionFile),
		Commit:  buildinfo.Commit(),
	})
}

// Version returns the version of gligabao (the contents of the VERSION file embedded at build time).
func Version() string { return buildinfo.Library().Version }

// Commit returns the git revision this binary was built from ("+dirty" if the tree had uncommitted changes), or ""
// if the Go toolchain did not stamp one (e.g. a build outside a git checkout).
func Commit() string { return buildinfo.Commit() }
