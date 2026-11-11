package agent_defs

import "testing"

// The example definitions that ship in build/agent_defs must load.
func TestShippedExamplesLoad(t *testing.T) {
	reg, err := Load("../../../build/agent_defs")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"news_scanner", "regional_scout"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("%s is missing", name)
		}
	}
	d, _ := reg.Get("news_scanner")
	if len(d.Skills) != 1 || len(d.OnDemandRoles()) != 1 {
		t.Errorf("news_scanner: %+v", d)
	}
}
