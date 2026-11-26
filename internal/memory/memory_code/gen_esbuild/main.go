// Command gen_esbuild vendors the TypeScript/JavaScript parser of esbuild (github.com/evanw/esbuild) into ../esparser. Run it from the repo root:
//
//	go run ./internal/memory/memory_code/gen_esbuild
//
// esbuild exposes no syntax tree in its public API (pkg/api): its parser lives in internal/ packages that Go forbids importing from another
// module. So this tool copies, from the pinned release, the packages that internal/js_parser depends on (the non-test .go files only) into
// esparser/<package>, rewrites their import paths to the copy, and adds esbuild's MIT license.
//
// The copy is NOT committed (esparser/ is in .gitignore): about 2 MB of third party code does not belong in the history of this repo.
// Instead it is made at build time, and only the builds with the Go build tag "esbuild" use it (see typescript.go in ../ for the full
// explanation). ops/build.py, the Dockerfile, run_tests.sh and gligabao_int/run_build.sh run this command before they build with the tag.
// It is quick to run again: it does nothing if esparser/ was already made for esbuildVersion (the marker file esparser/.esbuild_version).
// To upgrade esbuild, change esbuildVersion and run it again; the stale copy is replaced.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	esbuildVersion = "v0.28.2"
	esbuildModule  = "github.com/evanw/esbuild"
	outRel         = "internal/memory/memory_code/esparser"
	markerFile     = ".esbuild_version"
	ourModule      = "github.com/deepblue-phoenix/gligabao"
)

// roots are the packages wanted; their dependencies inside esbuild come with them.
var roots = []string{"js_parser", "js_ast", "logger", "config", "js_lexer", "fs", "helpers"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen_esbuild:", err)
		os.Exit(1)
	}
}

func run() error {
	if marker, err := os.ReadFile(filepath.Join(outRel, markerFile)); err == nil && strings.TrimSpace(string(marker)) == esbuildVersion {
		fmt.Println("gen_esbuild: esparser is up to date (esbuild " + esbuildVersion + ")")
		return nil
	}
	dir, err := moduleDir()
	if err != nil {
		return err
	}
	pkgs, err := closure(dir)
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
	for _, p := range pkgs {
		if err := copyPkg(dir, out, p); err != nil {
			return err
		}
	}
	lic, err := os.ReadFile(filepath.Join(dir, "LICENSE.md"))
	if err != nil {
		return err
	}
	note := fmt.Sprintf("Vendored from %s %s (internal/ packages, import paths rewritten) by gen_esbuild. Do not edit.\n\n", esbuildModule, esbuildVersion)
	if err := os.WriteFile(filepath.Join(out, "LICENSE.md"), append([]byte(note), lic...), 0o644); err != nil {
		return err
	}
	// the marker is written last: a copy that was interrupted is made again
	return os.WriteFile(filepath.Join(out, markerFile), []byte(esbuildVersion+"\n"), 0o644)
}

// moduleDir downloads the pinned esbuild module (go's module cache) and gives its directory.
func moduleDir() (string, error) {
	b, err := exec.Command("go", "mod", "download", "-json", esbuildModule+"@"+esbuildVersion).Output()
	if err != nil {
		return "", fmt.Errorf("go mod download: %w", err)
	}
	var m struct{ Dir string }
	if err := json.Unmarshal(b, &m); err != nil || m.Dir == "" {
		return "", fmt.Errorf("go mod download: bad answer %q", b)
	}
	return m.Dir, nil
}

// closure is the sorted names (relative to internal/) of the roots and all the esbuild internal packages they import.
func closure(dir string) ([]string, error) {
	args := []string{"list", "-deps", "-f", "{{.ImportPath}}"}
	for _, r := range roots {
		args = append(args, "./internal/"+r)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}
	prefix := esbuildModule + "/internal/"
	var pkgs []string
	for _, l := range strings.Fields(string(b)) {
		if rest, ok := strings.CutPrefix(l, prefix); ok {
			pkgs = append(pkgs, rest)
		}
	}
	return pkgs, nil
}

// copyPkg copies the non-test .go files of internal/<pkg> to out/<pkg>, with the import paths rewritten.
func copyPkg(dir, out, pkg string) error {
	src := filepath.Join(dir, "internal", pkg)
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	dst := filepath.Join(out, pkg)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	from := `"` + esbuildModule + `/internal/`
	to := `"` + ourModule + "/" + outRel + "/"
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(src, n))
		if err != nil {
			return err
		}
		s := strings.ReplaceAll(string(b), from, to)
		if err := os.WriteFile(filepath.Join(dst, n), []byte(s), 0o644); err != nil {
			return err
		}
	}
	return nil
}
