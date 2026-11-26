// Command gen generates the pure-Go (no cgo) Python parser of memory_code with ANTLR4, into ../pyparser. Run it from the repo root:
//
//	go run ./internal/memory/memory_code/gen
//
// It downloads, once, the pinned grammar files of antlr/grammars-v4 (python/python3: the lexer and parser grammars and the Go base classes)
// and the ANTLR tool into a cache dir (os.UserCacheDir()/gligabao/antlr; a cached file is not downloaded again, and the jar is verified
// against its SHA-256), patches the grammar the way the grammar's own transformGrammar.py does, and runs the tool (needs a Java runtime,
// only here: the generated files are committed, so building and running gligabao needs neither Java nor the network).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// file is a file to download: its URL, its name in the cache and, if not empty, the SHA-256 it must have.
type file struct {
	url    string
	name   string
	sha256 string
}

const (
	grammarsCommit = "1a298589a3168f87fe476f6347e0ded46344fbdf"
	grammarsBase   = "https://raw.githubusercontent.com/antlr/grammars-v4/" + grammarsCommit + "/python/python3/"
	antlrJar       = "antlr-4.13.2-complete.jar"
	outPackage     = "pyparser"
)

var files = []file{
	{url: grammarsBase + "Python3Lexer.g4", name: "Python3Lexer.g4"},
	{url: grammarsBase + "Python3Parser.g4", name: "Python3Parser.g4"},
	{url: grammarsBase + "Go/python3_lexer_base.go", name: "python3_lexer_base.go"},
	{url: grammarsBase + "Go/python3_parser_base.go", name: "python3_parser_base.go"},
	{
		url:    "https://www.antlr.org/download/" + antlrJar,
		name:   antlrJar,
		sha256: "eae2dfa119a64327444672aff63e9ec35a20180dc5b8090b7a6ab85125df4d76",
	},
}

var predicateRe = regexp.MustCompile(`\{[^{}]*\}\?`)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run() error {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	cache := filepath.Join(cacheRoot, "gligabao", "antlr", grammarsCommit)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		if err := ensure(cache, f); err != nil {
			return err
		}
	}
	out := filepath.Join("internal", "memory", "memory_code", outPackage)
	work, err := os.MkdirTemp("", "gligabao-antlr-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	for _, name := range []string{"Python3Lexer.g4", "Python3Parser.g4"} {
		if err := patchGrammar(filepath.Join(cache, name),
			filepath.Join(work, name),
			name == "Python3Lexer.g4"); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	cmd := exec.Command("java", "-jar", filepath.Join(cache, antlrJar),
		"-Dlanguage=Go", "-visitor", "-no-listener", "-package", outPackage, "-o", mustAbs(out),
		"Python3Lexer.g4", "Python3Parser.g4")
	cmd.Dir = work
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("antlr (is java installed?): %w", err)
	}
	for _, junk := range []string{"*.interp", "*.tokens"} {
		matches, _ := filepath.Glob(filepath.Join(out, junk))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				return err
			}
		}
	}
	for _, b := range []string{"python3_lexer_base.go", "python3_parser_base.go"} {
		data, err := os.ReadFile(filepath.Join(cache, b))
		if err != nil {
			return err
		}
		text := strings.Replace(string(data), "package parser", "package "+outPackage, 1)
		if b == "python3_lexer_base.go" {
			if !strings.Contains(text, lexerBaseTail) {
				return fmt.Errorf("the upstream %s changed: update lexerBaseTail", b)
			}
			text = strings.Replace(text, lexerBaseTail, lexerBaseFix, 1)
		}
		if err := os.WriteFile(filepath.Join(out, b), []byte(text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

// ensure downloads f into the cache unless it is there already (and, with a SHA-256, unchanged).
func ensure(cache string,
	f file) error {
	path := filepath.Join(cache, f.name)
	if data, err := os.ReadFile(path); err == nil && (f.sha256 == "" || sum(data) == f.sha256) {
		return nil
	}
	resp, err := http.Get(f.url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", f.url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if f.sha256 != "" && sum(data) != f.sha256 {
		return fmt.Errorf("download %s: SHA-256 mismatch", f.url)
	}
	return os.WriteFile(path, data, 0o644)
}

func sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// patchGrammar is transformGrammar.py: the Java-ish "this." of the actions and predicates becomes the receiver of the Go base class (in the
// lexer: "p." in a predicate, "l." in an action).
func patchGrammar(from,
	to string,
	lexer bool) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	text := string(data)
	if lexer { // a predicate ({this.x()}?) runs on the parser-side receiver "p", an action on the lexer "l"; one line can have both
		text = predicateRe.ReplaceAllStringFunc(text, func(m string) string {
			return strings.ReplaceAll(m, "this.", "p.")
		})
		text = strings.ReplaceAll(text, "this.", "l.")
	} else {
		text = strings.ReplaceAll(text, "this.", "p.")
	}
	return os.WriteFile(to, []byte(text), 0o644)
}

// lexerBaseTail is the end of NextToken of the upstream Go lexer base class, which drops the token that follows an INDENT/DEDENT/NEWLINE it
// made; lexerBaseFix replaces it.
const (
	lexerBaseTail = "\tif len(l.tokens) == 0 {\n\t\treturn next\n\t} else {\n\t\tx := l.tokens[0]\n\t\tl.tokens = l.tokens[1:]\n\t\treturn x\n\t}\n"
	lexerBaseFix  = "\t// the Java original gets every token through its overridden emit(); in Go BaseLexer.NextToken calls its own Emit, so the token\n" +
		"\t// it made (next) is queued here, unless the actions of the NEWLINE rule queued it already\n" +
		"\tif len(l.tokens) == 0 || l.tokens[len(l.tokens)-1] != next {\n\t\tl.tokens = append(l.tokens, next)\n\t}\n" +
		"\tx := l.tokens[0]\n\tl.tokens = l.tokens[1:]\n\treturn x\n"
)
