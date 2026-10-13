// Package buildinfo holds the version that is embedded into the binary at build time.
//
// The root package (version.go) embeds the VERSION file and registers it here with Set, so that packages under
// internal/ (the server, the CLI) can report it. A program embedding gligabao may also
// register itself with SetApp; the server then reports both.
package buildinfo

import (
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
)

// Component is the version information of one program or library.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"` // VCS revision the binary was built from (+dirty), if known
	// CommitURL is the GitHub URL of the commit (gligabao library only; empty if the commit is unknown).
	CommitURL string `json:"commit_url,omitempty"`
}

// RepoURL is the GitHub repository of gligabao.
const RepoURL = "https://github.com/deepblue-phoenix/gligabao"

// CommitURL returns the GitHub URL of the commit the running binary was built from, or "" if unknown.
func CommitURL() string {
	if rev := vcsRevision(); rev != "" {
		return RepoURL + "/commit/" + rev
	}
	return ""
}

var (
	mu      sync.RWMutex
	library Component
	app     *Component
)

// Set registers the gligabao library itself. It is called from the root package's init.
func Set(c Component) {
	mu.Lock()
	defer mu.Unlock()
	library = c
}

// SetApp registers the program that embeds gligabao; nil unregisters it.
func SetApp(c *Component) {
	mu.Lock()
	defer mu.Unlock()
	app = c
}

// Library returns the gligabao library's version information.
func Library() Component {
	mu.RLock()
	defer mu.RUnlock()
	return library
}

// App returns the embedding program's version information, if registered.
func App() (Component, bool) {
	mu.RLock()
	defer mu.RUnlock()
	if app == nil {
		return Component{}, false
	}
	return *app, true
}

// Parse returns the version from the contents of a VERSION file (surrounding whitespace removed).
func Parse(versionFile string) string { return strings.TrimSpace(versionFile) }

// Commit returns the VCS revision the running binary was built from, as stamped by `go build` inside a git
// checkout (shortened to 12 characters, "+dirty" appended for uncommitted changes), or "" if not available.
func Commit() string {
	rev, dirty := vcs()
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev != "" && dirty {
		rev += "+dirty"
	}
	return rev
}

// vcsRevision returns the full VCS revision stamped by `go build`, or "".
func vcsRevision() string { rev, _ := vcs(); return rev }

func vcs() (rev string, dirty bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return rev, dirty
}

// String formats the component as "name version (commit)".
func (c Component) String() string {
	s := c.Name + " " + c.Version
	if c.Commit != "" {
		s += " (" + c.Commit + ")"
	}
	return s
}

//---------------------------------------------------------------------------------------------

// SemVer matches a semantic version without a leading "v".
var SemVer = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

var changelogHeading = regexp.MustCompile(`(?m)^## \[([^\]]+)\]`)

// ChangelogVersions returns the versions of the "## [x.y.z] - date" headings of a CHANGELOG.md, newest first
// (the order in the file).
func ChangelogVersions(changelog string) []string {
	var out []string
	for _, m := range changelogHeading.FindAllStringSubmatch(changelog, -1) {
		out = append(out, m[1])
	}
	return out
}
