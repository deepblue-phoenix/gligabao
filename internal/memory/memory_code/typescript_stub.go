//go:build !tsjs_css

package memory_code

import "errors"

// This file is compiled when the build tag "tsjs_css" is NOT set (the default of a plain `go build` / `go test` / `go get`). It stands in
// for typescript.go, which needs esparser/: the copy of esbuild's parser that is not kept in git and is made by
// `go run ./internal/memory/memory_code/gen_esbuild` and `gen_tsgo` (the copy of typescript-go's parser). See the header of typescript.go for the whole reason.
//
// So a binary built without the tag has no TypeScript/JavaScript/CSS extraction: Detect says the language is unsupported, with the hint to
// build with the tag. Python is not affected.

// parsersBuilt tells whether TypeScript and JavaScript are compiled in (see typescript.go).
const parsersBuilt = false

var errNoEsbuild = errors.New("TypeScript/JavaScript extraction is not built in: generate the parser with " +
	"`go run ./internal/memory/memory_code/gen_esbuild`, `go run ./internal/memory/memory_code/gen_tsgo` and build with `-tags tsjs_css`")

func parseTypeScript(text string) (Result, error) {
	return Result{}, errNoEsbuild
}

func parseJavaScript(text string) (Result, error) {
	return Result{}, errNoEsbuild
}

func parseCSS(text string) (Result, error) {
	return Result{}, errNoEsbuild
}
