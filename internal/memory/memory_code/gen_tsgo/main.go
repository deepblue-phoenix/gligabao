// Command gen_tsgo vendors the TypeScript parser of the official TypeScript compiler port (github.com/microsoft/typescript-go) into
// ../tsparser. Run it from the repo root:
//
//	go run ./internal/memory/memory_code/gen_tsgo
//
// typescript-go keeps its syntax tree and parser in internal/ packages that Go forbids importing from another module (the same situation
// as esbuild, see ../gen_esbuild). So this tool copies, from the pinned commit, internal/parser and the packages it depends on inside
// typescript-go (the non-test files, plus the directories with no Go code that a go:embed needs) into tsparser/<package>, and rewrites
// their import paths to the copy. Their third party imports (golang.org/x/text, x/sync, go-json-experiment, zeebo/xxh3, ...) are normal
// requirements of go.mod. The license and notice of typescript-go (Apache 2.0) are copied too.
//
// The copy is NOT committed (tsparser/ is in .gitignore): it is made at build time, and only the builds with the Go build tag "tsjs_css" (the tag of the TS/JS/CSS parsers) use
// it (see typescript.go in ../). It is quick to run again: it does nothing if tsparser/ was already made for tsgoCommit (the marker
// file tsparser/.tsgo_commit). To upgrade, change tsgoCommit and run it again; the stale copy is replaced.
package main

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const (
	tsgoCommit = "89d5d5b2849a0db0957065889ca58536fa6d2e4a"
	tsgoModule = "github.com/microsoft/typescript-go"
	outRel     = "internal/memory/memory_code/tsparser"
	markerFile = ".tsgo_commit"
	ourModule  = "github.com/deepblue-phoenix/gligabao"
)

// roots are the packages wanted; their dependencies inside typescript-go come with them.
var roots = []string{"parser", "ast", "scanner", "core"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen_tsgo:", err)
		os.Exit(1)
	}
}

func run() error {
	if marker, err := os.ReadFile(filepath.Join(outRel, markerFile)); err == nil && strings.TrimSpace(string(marker)) == tsgoCommit {
		fmt.Println("gen_tsgo: tsparser is up to date (typescript-go " + tsgoCommit[:10] + ")")
		return nil
	}
	dir, err := moduleDir()
	if err != nil {
		return err
	}
	pkgs, err := closure(dir,
		roots,
		nil)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(outRel)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	for _, p := range slices.Sorted(slices.Values(pkgs)) {
		if err := copyPkg(filepath.Join(dir, "internal", p),
			filepath.Join(out, p),
			true); err != nil {
			return err
		}
	}
	for _, f := range []string{"LICENSE", "NOTICE.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, f), b, 0o644); err != nil {
			return err
		}
	}
	// the marker is written last: a copy that was interrupted is made again
	return os.WriteFile(filepath.Join(out, markerFile), []byte(tsgoCommit+"\n"), 0o644)
}

// moduleDir downloads the pinned typescript-go commit (go's module cache) and gives its directory.
func moduleDir() (string, error) {
	b, err := exec.Command("go", "mod", "download", "-json", tsgoModule+"@"+tsgoCommit).Output()
	if err != nil {
		return "", fmt.Errorf("go mod download: %w", err)
	}
	var m struct{ Dir string }
	if err := json.Unmarshal(b, &m); err != nil || m.Dir == "" {
		return "", fmt.Errorf("go mod download: bad answer %q", b)
	}
	return m.Dir, nil
}

// closure is the names (relative to internal/) of the packages todo and of all the typescript-go internal packages they import, read from
// the import clauses of their non-test files (build constraints are ignored: a package that is copied but never imported costs nothing).
func closure(dir string,
	todo []string,
	seen []string) ([]string, error) {
	if len(todo) == 0 {
		return seen, nil
	}
	head, rest := todo[0], todo[1:]
	if slices.Contains(seen, head) {
		return closure(dir,
			rest,
			seen)
	}
	imports, err := importsOf(filepath.Join(dir, "internal", head))
	if err != nil {
		return nil, err
	}
	return closure(dir,
		append(slices.Clone(rest), imports...),
		append(slices.Clone(seen), head))
}

// importsOf are the typescript-go internal packages (relative to internal/) that the non-test .go files of a directory import.
func importsOf(pkgDir string) ([]string, error) {
	files, err := goFiles(pkgDir)
	if err != nil {
		return nil, err
	}
	prefix := tsgoModule + "/internal/"
	out := []string{}
	for _, f := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(),
			f,
			nil,
			parser.ImportsOnly)
		if err != nil {
			return nil, err
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, err
			}
			if rest, ok := strings.CutPrefix(path, prefix); ok {
				out = append(out, rest)
			}
		}
	}
	return out, nil
}

// goFiles are the non-test .go files directly in a directory.
func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			out = append(out, filepath.Join(dir, n))
		}
	}
	return out, nil
}

// copyPkg copies a package directory: its non-test files with the import paths of .go files rewritten, and (top only) the subdirectories
// that have no .go file (data for go:embed, copied whole). Subdirectories with Go code are other packages, copied on their own.
func copyPkg(src,
	dst string,
	top bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	from := `"` + tsgoModule + `/internal/`
	to := `"` + ourModule + "/" + outRel + "/"
	for _, e := range entries {
		n := e.Name()
		switch {
		case e.IsDir():
			if goCode, err := hasGo(filepath.Join(src, n)); err != nil {
				return err
			} else if !goCode && !strings.HasPrefix(n, ".") {
				if err := copyPkg(filepath.Join(src, n),
					filepath.Join(dst, n),
					false); err != nil {
					return err
				}
			}
		case strings.HasSuffix(n, "_test.go"):
		default:
			b, err := os.ReadFile(filepath.Join(src, n))
			if err != nil {
				return err
			}
			if strings.HasSuffix(n, ".go") {
				b = []byte(strings.ReplaceAll(string(b), from, to))
			}
			if err := os.WriteFile(filepath.Join(dst, n), b, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// hasGo tells whether a directory (or one below it) holds a .go file.
func hasGo(dir string) (bool, error) {
	found := false
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		found = found || (err == nil && !e.IsDir() && strings.HasSuffix(p, ".go"))
		return err
	})
	return found, err
}
