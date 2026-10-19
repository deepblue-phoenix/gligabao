package training

import "testing"

func TestLoadEvals(t *testing.T) {
	cases, err := LoadEvals(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 10 {
		t.Fatalf("got %d cases, want 10", len(cases))
	}
	for _, c := range cases {
		if c.Eval != "world_knowledge_base" || c.Example().Prompt() == "" || c.Output == "" {
			t.Errorf("bad case %+v", c)
		}
	}

	one, err := LoadEvals([]string{"maastricht-treaty"}, "world_knowledge_base")
	if err != nil || len(one) != 1 {
		t.Fatalf("one case: %v, %v", one, err)
	}
	if _, err := LoadEvals(nil, "nope"); err == nil {
		t.Error("unknown eval accepted")
	}
	if _, err := LoadEvals([]string{"nope"}, ""); err == nil {
		t.Error("unknown case accepted")
	}
}
