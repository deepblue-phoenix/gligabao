package gligabao

import (
	"os"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/buildinfo"
)

// The version rules (AGENTS.md) are enforced here: VERSION is semver, and CHANGELOG.md's newest entry is VERSION.
func TestVersionMatchesChangelog(t *testing.T) {
	v := Version()
	b, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	changelogFile := string(b)
	if !buildinfo.SemVer.MatchString(v) {
		t.Fatalf("VERSION %q is not a semantic version (x.y.z)", v)
	}
	versions := buildinfo.ChangelogVersions(changelogFile)
	if len(versions) == 0 {
		t.Fatal("CHANGELOG.md has no '## [x.y.z] - date' entries")
	}
	if versions[0] != v {
		t.Fatalf("newest CHANGELOG.md entry is %q but VERSION is %q: add or fix the entry", versions[0], v)
	}
	seen := map[string]bool{}
	for _, c := range versions {
		if !buildinfo.SemVer.MatchString(c) {
			t.Errorf("CHANGELOG.md entry %q is not a semantic version", c)
		}
		if seen[c] {
			t.Errorf("CHANGELOG.md has a duplicate entry for %q", c)
		}
		seen[c] = true
	}
}
