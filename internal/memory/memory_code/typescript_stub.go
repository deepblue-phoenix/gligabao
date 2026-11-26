//go:build !esbuild

package memory_code

import "errors"

// This file is compiled when the build tag "esbuild" is NOT set (the default of a plain `go build` / `go test` / `go get`). It stands in
// for typescript.go, which needs esparser/: the copy of esbuild's parser that is not kept in git and is made by
// `go run ./internal/memory/memory_code/gen_esbuild`. See the header of typescript.go for the whole reason.
//
// So a binary built without the tag has no TypeScript/JavaScript extraction: Detect says the language is unsupported, with the hint to
// build with the tag. Python is not affected.

// esbuildBuilt tells whether TypeScript and JavaScript are compiled in (see typescript.go).
const esbuildBuilt = false

var errNoEsbuild = errors.New("TypeScript/JavaScript extraction is not built in: generate the parser with " +
	"`go run ./internal/memory/memory_code/gen_esbuild` and build with `-tags esbuild`")

func extractTypeScript(text string) (Graph, error) {
	return Graph{}, errNoEsbuild
}

func extractJavaScript(text string) (Graph, error) {
	return Graph{}, errNoEsbuild
}
