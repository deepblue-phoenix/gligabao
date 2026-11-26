package memory_code

import (
	"regexp"
	"strings"
)

// Content based detection of the language of code that came without a (trusted) file name: every language has a few regular expressions
// of syntax that is typical of it; the language with the highest score wins. See DetectContent.

var (
	goSyntaxRe = regexp.MustCompile(`(?m)^(func\s+(\(\w+\s+\*?\w+\)\s+)?\w+\s*\(|type\s+\w+\s+(struct|interface)\b|import\s+\(|var\s+\w+\s+\w+|:=)`)

	pySyntaxRes = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*def\s+\w+\s*\(.*\)\s*(->\s*[^:]+)?:\s*$`),
		regexp.MustCompile(`(?m)^\s*class\s+\w+\s*(\(.*\))?\s*:\s*$`),
		regexp.MustCompile(`(?m)^(from\s+[\w.]+\s+import\s+|import\s+[\w.]+(\s+as\s+\w+)?\s*$)`),
		regexp.MustCompile(`(?m)^\s*(elif\b.*:|else\s*:|if\b.*:\s*$|for\b.*\bin\b.*:\s*$|while\b.*:\s*$|try\s*:|except\b.*:)`),
		regexp.MustCompile(`\b(self|None|True|False)\b`),
	}

	tsSyntaxRes = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(export\s+)?(declare\s+)?(interface|enum|namespace)\s+\w+`),
		regexp.MustCompile(`(?m)^\s*(export\s+)?type\s+\w+(<.*>)?\s*=`),
		regexp.MustCompile(`\bimport\s+type\b`),
		regexp.MustCompile(`[\w)\]]\s*\??:\s*(string|number|boolean|any|void|unknown|never|object|bigint|symbol)\b`),
		regexp.MustCompile(`\)\s*:\s*[\w<>\[\]|, .]+\s*(\{|=>)`),
		regexp.MustCompile(`\b(public|private|protected|readonly|implements|abstract)\s+\w+`),
		regexp.MustCompile(`\bas\s+(const|string|number|any|unknown|\w+\[\])\b`),
		regexp.MustCompile(`\w+<[A-Z]\w*(,\s*\w+)*>\s*\(`),
	}

	jsSyntaxRes = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(export\s+)?(default\s+)?(async\s+)?function\b\s*\*?\s*\w*\s*\(`),
		regexp.MustCompile(`(?m)^\s*(export\s+)?(const|let|var)\s+\w+\s*=`),
		regexp.MustCompile(`=>`),
		regexp.MustCompile(`\brequire\(\s*['"]`),
		regexp.MustCompile(`(?m)^\s*(import\s+.*\s+from\s+['"]|import\s+['"]|export\s+(default|\{|\*))`),
		regexp.MustCompile(`\bconsole\.\w+\(`),
		regexp.MustCompile(`(?m)^\s*(export\s+)?class\s+\w+(\s+extends\s+[\w.]+)?\s*\{`),
		regexp.MustCompile(`===|!==|;\s*$`),
	}

	cssSyntaxRes = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*[.#@:\w][^{};]*\{\s*$`),
		regexp.MustCompile(`(?m)^\s*[\w-]+\s*:\s*[^;{}]+;\s*$`),
		regexp.MustCompile(`(?m)^\s*@(media|import|keyframes|font-face|charset|supports)\b`),
		regexp.MustCompile(`--[\w-]+\s*:|var\(\s*--`),
	}
)

// score is how many of the expressions match the code.
func score(res []*regexp.Regexp,
	text string) int {
	n := 0
	for _, re := range res {
		if re.MatchString(text) {
			n++
		}
	}
	return n
}

// DetectContent guesses the language of the code from the code alone (a heuristic over typical syntax; never an error: the fallback is
// LangPython, as it always was). It does not check that the extractor of the language is built (see Detect).
func DetectContent(text string) string {
	if goPackageRe.MatchString(text) {
		return LangGo
	}
	py, ts, js, css := score(pySyntaxRes, text), score(tsSyntaxRes, text), score(jsSyntaxRes, text), score(cssSyntaxRes, text)
	// the braces of every C-like language are in the CSS expressions too: CSS needs its own markers and none of the JS/TS/Python ones
	cssOnly := css >= 2 && js == 0 && ts == 0 && py == 0
	if goSyntaxRe.MatchString(text) && py == 0 && ts == 0 && strings.Contains(text, "{") {
		return LangGo
	}
	switch {
	case cssOnly:
		return LangCSS
	case ts >= 1 && ts+js > py && ts*2 >= js/2:
		return LangTypeScript
	case js > py && js >= 1:
		return LangJavaScript
	}
	return LangPython
}

// DefaultFilename is the file name given to code that came without one: "snippet" and the first extension of the language.
func DefaultFilename(lang string) string {
	for _, l := range languages {
		if l.Name == lang && len(l.Extensions) > 0 {
			return "snippet" + l.Extensions[0]
		}
	}
	return "snippet.txt"
}

// Info describes parsed code: the language that was detected, the file name (the one given, or the default for the language) and the
// statistics of its syntax tree.
type Info struct {
	Language string `json:"language"`
	Filename string `json:"filename"`
	// Detected is how the language was found: always "content" (guessed from the code, never from a file name).
	Detected string `json:"detected"`

	Lines int `json:"lines"`
	Bytes int `json:"bytes"`

	Entities        int            `json:"entities"`
	Edges           int            `json:"edges"`
	EntitiesByType  map[string]int `json:"entities_by_type"`
	EdgesByRelation map[string]int `json:"edges_by_relation"`

	// TypeDecls, Annotations and StyleRules are the sizes of the same lists of Result (TypeScript, CSS).
	TypeDecls   int `json:"type_decls"`
	Annotations int `json:"annotations"`
	StyleRules  int `json:"style_rules"`
}

// Resolve is the language of the code and the file name to use for it. The language comes only from the code (DetectContent): a file
// name the caller gave is not looked at, so a wrong or missing extension changes nothing. The name is always the default of the detected
// language. ErrUnsupportedLanguage (wrapped) if the extractor of the language is not built in.
func Resolve(filename,
	text string) (lang,
	name,
	detected string,
	err error) {
	lang = DetectContent(text)
	return lang, DefaultFilename(lang), "content", builtOrError(lang)
}

// Describe parses the code (language and name as in Resolve) and returns its Info.
func Describe(filename,
	text string) (Info, error) {
	lang, name, detected, err := Resolve(filename,
		text)
	if err != nil {
		return Info{}, err
	}
	r, err := Parse(lang,
		text)
	if err != nil {
		return Info{}, err
	}
	return infoOf(lang,
		name,
		detected,
		text,
		r), nil
}

// infoOf is the Info of a parsed Result.
func infoOf(lang,
	name,
	detected,
	text string,
	r Result) Info {
	byType, byRel := map[string]int{}, map[string]int{}
	for _, e := range r.Graph.Entities {
		byType[e.Type]++
	}
	for _, e := range r.Graph.Edges {
		byRel[e.Relation]++
	}
	return Info{
		Language:        lang,
		Filename:        name,
		Detected:        detected,
		Lines:           strings.Count(text, "\n") + 1,
		Bytes:           len(text),
		Entities:        len(r.Graph.Entities),
		Edges:           len(r.Graph.Edges),
		EntitiesByType:  byType,
		EdgesByRelation: byRel,
		TypeDecls:       len(r.Types),
		Annotations:     len(r.Annotations),
		StyleRules:      len(r.Styles),
	}
}
