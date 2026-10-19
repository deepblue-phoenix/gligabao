package training

import "testing"

// testEvalsDir is the directory of the evals (the tests run in the package directory).
const testEvalsDir = "../../../bin/evals_training"

func TestLoadEvals(t *testing.T) {
	cases, err := LoadEvals(testEvalsDir, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 10 {
		t.Fatalf("got %d cases, want 10", len(cases))
	}
	for _, c := range cases {
		if c.Eval != "world-knowledge-base" || c.Example().Prompt() == "" || c.Output == "" {
			t.Errorf("bad case %+v", c)
		}
	}

	one, err := LoadEvals(testEvalsDir, []string{"maastricht-treaty"}, "world-knowledge-base")
	if err != nil || len(one) != 1 {
		t.Fatalf("one case: %v, %v", one, err)
	}
	if _, err := LoadEvals(testEvalsDir, nil, "nope"); err == nil {
		t.Error("unknown eval accepted")
	}
	if _, err := LoadEvals(testEvalsDir, []string{"nope"}, ""); err == nil {
		t.Error("unknown case accepted")
	}
}
