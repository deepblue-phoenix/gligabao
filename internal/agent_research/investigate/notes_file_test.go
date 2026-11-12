package investigate

import (
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate_core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeUser(t *testing.T) {
	cases := map[string]string{
		"":                            "anon",
		"alice":                       "alice",
		"a.b_c-D9":                    "a.b_c-D9",
		"../../etc/passwd":            ".._.._etc_passwd",
		"a/b\\c":                      "a_b_c",
		"john doe":                    "john_doe",
		"jos\u00e9 \u4e2d\u6587":      "jos____",
		"x@example.com":               "x_example.com",
		strings.Repeat("a", 100):      strings.Repeat("a", 64),
		strings.Repeat("\u4e2d", 100): strings.Repeat("_", 64),
		"..":                          "..",
	}
	for in, want := range cases {
		got := SanitizeUser(in)
		if got != want {
			t.Errorf("SanitizeUser(%q) = %q, want %q",
				in,
				got,
				want)
		}
		if strings.ContainsAny(got, "/\\") {
			t.Errorf("path separator in %q", got)
		}
	}
}

func TestNotesFilePath(t *testing.T) {
	f := NotesFile{
		Dir: "/data/notes",
	}
	id := strings.Repeat("ab", 16)
	if got, want := f.Path("a/b",
		7,
		id), filepath.Join("/data/notes", "notes_a_b_7_"+id+".md"); got != want {
		t.Fatalf("%q != %q",
			got,
			want)
	}
	if f.Path("u",
		7,
		"") != "" {
		t.Fatal("a run without a notes id has no file")
	}
	if filepath.Dir(f.Path("../..",
		1,
		id)) != "/data/notes" {
		t.Fatal("the user cannot leave the directory")
	}
}

func TestNotesFileReadWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(),
		"sub",
		"dir")
	f := NotesFile{
		Dir: dir,
	}
	path := f.Path("u",
		1,
		strings.Repeat("0", 32))
	text, err := f.Read(path)
	if err != nil || text != investigate_core.Render(investigate_core.Notes{}) {
		t.Fatalf("a missing file reads as the empty notes: %q %v",
			text,
			err)
	}
	n := investigate_core.Notes{
		Objective: "Does X fit in 16 GB?",
	}
	if err := f.Write(path, investigate_core.Render(n)); err != nil { // creates the directory
		t.Fatal(err)
	}
	got, err := f.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	back, errs := investigate_core.Parse(got)
	if len(errs) > 0 || investigate_core.Render(back) != investigate_core.Render(n) {
		t.Fatalf("write, read, parse: %v", errs)
	}
	if err := f.Write(path, "second"); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.Read(path); got != "second" {
		t.Fatal(got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("no temp file is left behind: %v", entries)
	}
	if err := f.Write("", "x"); err == nil {
		t.Fatal("empty path")
	}
}
