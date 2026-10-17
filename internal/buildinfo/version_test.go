package buildinfo

import (
	"os"
	"testing"
)

// The version rules (AGENTS.md) are enforced here: VERSION is semver, and CHANGELOG.md's newest entry is VERSION.
// Both files live at the repo root (VERSION is embedded by the root package), two levels up from here.
func TestVersionMatchesChangelog(t *testing.T) {
	vb, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	v := Parse(string(vb))
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	changelogFile := string(b)
	if !SemVer.MatchString(v) {
		t.Fatalf("VERSION %q is not a semantic version (x.y.z)", v)
	}
	versions := ChangelogVersions(changelogFile)
	if len(versions) == 0 {
		t.Fatal("CHANGELOG.md has no '## [x.y.z] - date' entries")
	}
	if versions[0] != v {
		t.Fatalf("newest CHANGELOG.md entry is %q but VERSION is %q: add or fix the entry", versions[0], v)
	}
	seen := map[string]bool{}
	for _, c := range versions {
		if !SemVer.MatchString(c) {
			t.Errorf("CHANGELOG.md entry %q is not a semantic version", c)
		}
		if seen[c] {
			t.Errorf("CHANGELOG.md has a duplicate entry for %q", c)
		}
		seen[c] = true
	}
}
