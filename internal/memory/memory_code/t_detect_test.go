package memory_code

import "testing"

func TestDetectContent(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"go", "package main\n\nfunc main() {}\n", LangGo},
		{"python", "import os\n\ndef run(x):\n    return x\n", LangPython},
		{"python class", "class A:\n    def f(self):\n        pass\n", LangPython},
		{"js", "const a = (x) => x + 1;\nconsole.log(a(1));\n", LangJavaScript},
		{"ts", "interface P { x: number }\nexport function f(p: P): number { return p.x; }\n", LangTypeScript},
		{"css", ".a {\n  color: red;\n}\n@media (min-width: 1px) {\n  .b { margin: 0; }\n}\n", LangCSS},
	} {
		if got := DetectContent(c.text); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDescribePython(t *testing.T) {
	info, err := Describe("", sample)
	if err != nil {
		t.Fatal(err)
	}
	if info.Language != LangPython || info.Filename != "snippet.py" || info.Detected != "content" {
		t.Fatalf("%+v", info)
	}
	if info.Entities == 0 || info.EntitiesByType[TypeClass] != 2 {
		t.Fatalf("%+v", info)
	}
}
