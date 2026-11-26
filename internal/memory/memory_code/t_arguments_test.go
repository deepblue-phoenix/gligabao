package memory_code

import "testing"

func TestArguments(t *testing.T) {
	for name, tc := range map[string]struct {
		file, src string
		fn        string
		want      []string
	}{
		"go":     {"a.go", "package a\n\nfunc add(x int, y int, _ int) int { return x }\n", "add", []string{"add.x", "add.y"}},
		"python": {"a.py", "class A:\n    def run(self, a, b=1, *args, **kw):\n        pass\n", "A.run", []string{"A.run.a", "A.run.b", "A.run.args", "A.run.kw"}},
	} {
		t.Run(name, func(t *testing.T) {
			g, err := ExtractAuto(tc.file, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, e := range g.Edges {
				if e.Source == tc.fn && e.Relation == RelHasArgument {
					got = append(got, e.Target)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("args of %s: %v, want %v", tc.fn, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("args of %s: %v, want %v", tc.fn, got, tc.want)
				}
			}
			for _, e := range g.Entities {
				if e.Name == tc.want[0] && !IsArgument(e.Type) {
					t.Errorf("%s has type %q", e.Name, e.Type)
				}
			}
			plain := WithoutArguments(g)
			for _, e := range plain.Entities {
				if IsArgument(e.Type) {
					t.Errorf("argument %s was not dropped", e.Name)
				}
			}
			for _, e := range plain.Edges {
				if e.Relation == RelHasArgument {
					t.Errorf("edge %v was not dropped", e)
				}
			}
		})
	}
}
