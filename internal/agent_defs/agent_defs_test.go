package agent_defs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodTOML = `
schema = 1
name = "news_scanner"
version = "1.2.0"
description = "Scans recent news."
system_prompt_file = "prompts/news.md"
model = "my-model"
max_steps = 25
max_tokens = 4096
tools = ["web_search", "fetch_url"]
skills = ["citation_style", "missing_one"]
required_skills = ["dates"]

[search]
blocked_domains = ["Pinterest.com"]
preferred_domains = ["reuters.com"]
recency = "week"
language = "en"
location = { country = "de", city = "Munich" }
max_uses_per_call = 3
max_calls = 10
[search.fetch]
allowed_domains = ["example.com"]
max_page_chars = 5000

[knowledge]
access = "read_only"
dataset = "news"
query_first = true

[limits]
max_cost_usd = 1.5
max_tree_cost_usd = 5

[spawn]
enabled = true
max_per_agent = 3
[[spawn.roles]]
name = "regional"
agent = "regional_scout"
version = ">=1.0, <2"
when = "the topic names several regions"
does = "covers one region"
max_instances = 2
max_steps = 8
[[spawn.roles]]
name = "headlines"
agent = "regional_scout"
trigger = "on_start"
does = "reads the headlines"
task_template = "Headlines about {{.Topic}}"
`

const scoutTOML = `
name = "regional_scout"
version = "1.0.3"
description = "Scouts a region."
system_prompt = "You scout a region."
max_steps = 12
`

// dir makes an agent_defs directory with the files (path -> content).
func dir(t *testing.T, files map[string]string) string {
	t.Helper()
	d := t.TempDir()
	for name, content := range files {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func goodFiles() map[string]string {
	return map[string]string{
		"news_scanner.toml":        goodTOML,
		"regional_scout.toml":      scoutTOML,
		"prompts/news.md":          "  You are a news scanner.\n",
		"skills/citation_style.md": "Cite like this.",
		"skills/dates.md":          "Give dates.",
	}
}

func TestLoadReadsTomlAndResolvesEverything(t *testing.T) {
	reg, err := Load(dir(t, goodFiles()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range reg.List() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "default,news_scanner,regional_scout" {
		t.Fatalf("names: %v", names)
	}
	d, err := reg.Resolve("news_scanner", "^1.1")
	if err != nil {
		t.Fatal(err)
	}
	if d.SystemPrompt != "You are a news scanner." || d.Model != "my-model" || d.MaxSteps != 25 || d.MaxTokens != 4096 {
		t.Errorf("basics: %+v", d)
	}
	// The prompt file is resolved; the skills come required first, and the optional one that is missing is skipped.
	if len(d.Skills) != 2 || d.Skills[0] != (Skill{"dates", "Give dates."}) || d.Skills[1].Name != "citation_style" {
		t.Errorf("skills: %+v", d.Skills)
	}
	// Domains are lowercased, the country upper-cased.
	if d.Search.BlockedDomains[0] != "pinterest.com" || d.Search.Location.Country != "DE" || d.Search.Fetch.MaxPageChars != 5000 || d.Search.MaxUsesPerCall != 3 {
		t.Errorf("search: %+v", d.Search)
	}
	if d.Knowledge.Access != AccessReadOnly || d.Knowledge.Dataset != "news" || !d.Knowledge.QueryFirst || d.Limits.MaxCostUSD != 1.5 || d.Limits.MaxTreeCostUSD != 5 {
		t.Errorf("knowledge and limits: %+v %+v", d.Knowledge, d.Limits)
	}
	if !d.Spawn.Enabled || d.MaxPerAgent() != 3 || d.MaxDepth() != MaxSubagentDepth || d.MaxPerTree() != MaxSubagentsPerTree {
		t.Errorf("spawn: %+v", d.Spawn)
	}
	if on := d.OnDemandRoles(); len(on) != 1 || on[0].Name != "regional" {
		t.Errorf("on demand: %+v", on)
	}
	start := d.OnStartRoles()
	if len(start) != 1 || start[0].Name != "headlines" {
		t.Fatalf("on start: %+v", start)
	}
	if task, err := start[0].Task("batteries"); err != nil || task != "Headlines about batteries" {
		t.Errorf("task: %q %v", task, err)
	}
	child, _ := reg.Get("regional_scout")
	if def, limit := on0(d).Steps(child); def != 8 || limit != 12 {
		t.Errorf("steps of the role: %d %d", def, limit)
	}
	if len(d.Hash) != 64 || d.Source == "" {
		t.Errorf("hash and source: %q %q", d.Hash, d.Source)
	}
}

func on0(d *Def) Role { return d.OnDemandRoles()[0] }

func TestLoadReadsJSON(t *testing.T) {
	reg, err := Load(dir(t, map[string]string{"a.json": `{"name":"json_agent","version":"0.1.0","description":"d","system_prompt":"p","tools":["web_search"],"knowledge":{"access":"none"}}`}))
	if err != nil {
		t.Fatal(err)
	}
	d, _ := reg.Get("json_agent")
	if d == nil || d.MaxSteps != DefaultMaxSteps || len(d.Tools) != 1 || d.Knowledge.Access != AccessNone {
		t.Errorf("def: %+v", d)
	}
}

func TestMissingDirectoryIsOnlyTheBuiltin(t *testing.T) {
	reg, err := Load(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatal(err)
	}
	d := reg.Default()
	if len(reg.List()) != 1 || d.Source != builtinSource || !d.Spawn.Enabled || d.MaxSteps != 20 || d.MaxTokens != 8192 || d.Limits != (Limits{}) {
		t.Errorf("builtin: %+v", d)
	}
	if _, err := Load(filepath.Join(dir(t, map[string]string{"f": "x"}), "f")); err == nil {
		t.Error("a file is not a directory")
	}
}

func TestAFileCalledDefaultReplacesTheBuiltin(t *testing.T) {
	reg, err := Load(dir(t, map[string]string{"default.toml": `name = "default"
version = "2.0.0"
description = "Mine."
system_prompt = "MINE"
`}))
	if err != nil {
		t.Fatal(err)
	}
	if d := reg.Default(); d.Version != "2.0.0" || d.SystemPrompt != "MINE" {
		t.Errorf("default: %+v", d)
	}
}

func TestInvalidDefinitionsAreAllReported(t *testing.T) {
	const base = "name = \"x\"\nversion = \"1.0.0\"\ndescription = \"d\"\n"
	cases := map[string]struct {
		files map[string]string
		want  []string
	}{
		"unknown key":                   {map[string]string{"x.toml": base + "system_prompt = \"p\"\nmodle = \"m\"\n"}, []string{"unknown key(s): modle"}},
		"unknown json key":              {map[string]string{"x.json": `{"name":"x","bogus":1}`}, []string{"invalid JSON"}},
		"bad toml":                      {map[string]string{"x.toml": "name = "}, []string{"invalid TOML"}},
		"no prompt":                     {map[string]string{"x.toml": base}, []string{"system_prompt or system_prompt_file: one is required"}},
		"both prompts":                  {map[string]string{"x.toml": base + "system_prompt = \"p\"\nsystem_prompt_file = \"p.md\"\n", "p.md": "p"}, []string{"give one of them"}},
		"missing prompt":                {map[string]string{"x.toml": base + "system_prompt_file = \"p.md\"\n"}, []string{"system_prompt_file", "p.md"}},
		"prompt escapes":                {map[string]string{"sub/x.toml": base + "system_prompt_file = \"../../x\"\n"}, []string{}}, // not loaded (subdirectory)
		"prompt outside":                {map[string]string{"x.toml": base + "system_prompt_file = \"../outside.md\"\n"}, []string{"outside the definitions directory"}},
		"absolute prompt":               {map[string]string{"x.toml": base + "system_prompt_file = \"/etc/passwd\"\n"}, []string{"relative"}},
		"bad name":                      {map[string]string{"x.toml": "name = \"Bad Name\"\nversion = \"1.0.0\"\ndescription = \"d\"\nsystem_prompt = \"p\"\n"}, []string{"name:"}},
		"bad version":                   {map[string]string{"x.toml": "name = \"x\"\nversion = \"1.0\"\ndescription = \"d\"\nsystem_prompt = \"p\"\n"}, []string{"version:"}},
		"no description":                {map[string]string{"x.toml": "name = \"x\"\nversion = \"1.0.0\"\nsystem_prompt = \"p\"\n"}, []string{"description: required"}},
		"unknown tool":                  {map[string]string{"x.toml": base + "system_prompt = \"p\"\ntools = [\"shell\"]\n"}, []string{"unknown tool \"shell\""}},
		"knowledge tool":                {map[string]string{"x.toml": base + "system_prompt = \"p\"\ntools = [\"query\"]\n"}, []string{"[knowledge] access"}},
		"too many steps":                {map[string]string{"x.toml": base + "system_prompt = \"p\"\nmax_steps = 41\n"}, []string{"max_steps"}},
		"allowed+blocked":               {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[search]\nallowed_domains = [\"a.com\"]\nblocked_domains = [\"b.com\"]\n"}, []string{"cannot be used together"}},
		"bad domain":                    {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[search]\nblocked_domains = [\"https://a.com/x\"]\n"}, []string{"not a host name"}},
		"bad recency":                   {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[search]\nrecency = \"decade\"\n"}, []string{"search.recency"}},
		"domains without the tool":      {map[string]string{"x.toml": base + "system_prompt = \"p\"\ntools = [\"fetch_url\"]\n[search]\nblocked_domains = [\"b.com\"]\n"}, []string{"web_search is not in tools"}},
		"bad access":                    {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[knowledge]\naccess = \"write\"\n"}, []string{"knowledge.access"}},
		"lock without dataset":          {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[knowledge]\ndataset_locked = true\n"}, []string{"needs a dataset"}},
		"negative cap":                  {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[limits]\nmax_cost_usd = -1\n"}, []string{"limits.max_cost_usd"}},
		"spawn settings while disabled": {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nmax_per_agent = 2\n"}, []string{"enabled is false"}},
		"spawn over ceiling":            {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\nmax_per_agent = 6\n"}, []string{"spawn.max_per_agent"}},
		"role needs when":               {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\n"}, []string{"when: required"}},
		"on_demand task template":       {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\nwhen = \"w\"\ntask_template = \"x\"\n"}, []string{"only an on_start role"}},
		"bad template":                  {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\ntrigger = \"on_start\"\ntask_template = \"{{.Nope}}\"\n"}, []string{"task_template"}},
		"bad trigger":                   {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\nwhen = \"w\"\ntrigger = \"always\"\n"}, []string{"trigger"}},
		"duplicate role":                {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\nwhen = \"w\"\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\nwhen = \"w\"\n"}, []string{"defined twice"}},
		"unknown role agent":            {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"ghost\"\ndoes = \"d\"\nwhen = \"w\"\n"}, []string{"no agent called \"ghost\""}},
		"role version not met":          {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\nversion = \">=2\"\ndoes = \"d\"\nwhen = \"w\"\n"}, []string{"does not meet"}},
		"role steps do not fit":         {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"default\"\ndoes = \"d\"\nwhen = \"w\"\nmax_steps = 30\n"}, []string{"do not fit"}},
		"self role":                     {map[string]string{"x.toml": base + "system_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"x\"\ndoes = \"d\"\nwhen = \"w\"\n"}, []string{"x -> x starts itself"}},
		"required skill missing":        {map[string]string{"x.toml": base + "system_prompt = \"p\"\nrequired_skills = [\"nope\"]\n"}, []string{"required_skills", "nope"}},
		"bad skill name":                {map[string]string{"x.toml": base + "system_prompt = \"p\"\nskills = [\"../x\"]\n"}, []string{"skills:"}},
		"duplicate name":                {map[string]string{"x.toml": base + "system_prompt = \"p\"\n", "y.json": `{"name":"x","version":"1.0.0","description":"d","system_prompt":"p"}`}, []string{"is also the name of"}},
	}
	for name, c := range cases {
		files := c.files
		d := dir(t, files)
		_, err := Load(d)
		if len(c.want) == 0 {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: loaded without error", name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: the error lacks %q:\n%v", name, w, err)
			}
		}
	}
}

func TestMutualRolesAreACycle(t *testing.T) {
	mk := func(name, other string) string {
		return "name = \"" + name + "\"\nversion = \"1.0.0\"\ndescription = \"d\"\nsystem_prompt = \"p\"\n[spawn]\nenabled = true\n[[spawn.roles]]\nname = \"r\"\nagent = \"" + other + "\"\ndoes = \"d\"\nwhen = \"w\"\n"
	}
	_, err := Load(dir(t, map[string]string{"a.toml": mk("a", "b"), "b.toml": mk("b", "a")}))
	if err == nil || !strings.Contains(err.Error(), "starts itself through its roles") {
		t.Fatalf("err: %v", err)
	}
}

func TestSkillAndPromptSymlinksCannotLeaveTheDirectory(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dir(t, map[string]string{"x.toml": "name = \"x\"\nversion = \"1.0.0\"\ndescription = \"d\"\nsystem_prompt = \"p\"\nrequired_skills = [\"leak\"]\n"})
	if err := os.MkdirAll(filepath.Join(d, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d, "skills", "leak.md")); err != nil {
		t.Skip("no symlinks")
	}
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "outside the definitions directory") {
		t.Fatalf("err: %v", err)
	}
}

func TestBundleRoundTripAndTamper(t *testing.T) {
	reg, err := Load(dir(t, goodFiles()))
	if err != nil {
		t.Fatal(err)
	}
	b, err := reg.Bundle("news_scanner")
	if err != nil {
		t.Fatal(err)
	}
	if b.Agent != "news_scanner" || len(b.Defs) != 2 { // the agent and the one its roles start: not the default, not reachable
		t.Fatalf("bundle: %s %d", b.Agent, len(b.Defs))
	}
	data, err := b.JSON()
	if err != nil {
		t.Fatal(err)
	}
	reg2, name, err := ParseBundle(data)
	if err != nil || name != "news_scanner" {
		t.Fatalf("parse: %q %v", name, err)
	}
	for _, n := range []string{"news_scanner", "regional_scout"} {
		a, _ := reg.Get(n)
		c, _ := reg2.Get(n)
		if c == nil || a.Hash != c.Hash || a.SystemPrompt != c.SystemPrompt || len(a.Skills) != len(c.Skills) {
			t.Errorf("%s did not survive the bundle: %+v vs %+v", n, a, c)
		}
	}
	// A changed prompt no longer matches its hash.
	tampered := strings.Replace(string(data), "You are a news scanner.", "You are evil.", 1)
	if _, _, err := ParseBundle([]byte(tampered)); err == nil || !strings.Contains(err.Error(), "hash does not match") {
		t.Errorf("tampered bundle: %v", err)
	}
	if _, _, err := ParseBundle([]byte(`{"agent":"ghost","defs":[]}`)); err == nil {
		t.Error("a bundle for an agent it does not have")
	}
	if _, _, err := ParseBundle([]byte(`{"agent":"default","defs":[],"extra":1}`)); err == nil {
		t.Error("unknown field in a bundle")
	}
	var generic map[string]any
	if json.Unmarshal(data, &generic) != nil {
		t.Error("the bundle is not JSON")
	}
	// A bundle of the built-in agent alone is what a plain research sends to a container.
	bb, err := BuiltinRegistry().Bundle(DefaultName)
	if err != nil || len(bb.Defs) != 1 {
		t.Fatalf("builtin bundle: %+v %v", bb, err)
	}
	if _, name, err := ParseBundle(mustJSON(t, bb)); err != nil || name != DefaultName {
		t.Errorf("builtin bundle parse: %q %v", name, err)
	}
}

func mustJSON(t *testing.T, b *Bundle) []byte {
	t.Helper()
	data, err := b.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBundleSizeIsCapped(t *testing.T) {
	d := Builtin()
	d.SystemPrompt = strings.Repeat("x", MaxBundleBytes)
	if _, err := (&Bundle{Agent: d.Name, Defs: []*Def{d}}).JSON(); err == nil {
		t.Error("an oversized bundle was made")
	}
}

func TestHashChangesWithTheContentNotTheSource(t *testing.T) {
	a, b := Builtin(), Builtin()
	if a.Hash != b.Hash {
		t.Fatal("the same content has two hashes")
	}
	b.Source = "elsewhere"
	if hashOf(b) != a.Hash {
		t.Error("the source is part of the hash")
	}
	b.SystemPrompt += " more"
	if hashOf(b) == a.Hash {
		t.Error("the prompt is not part of the hash")
	}
}

func TestResolve(t *testing.T) {
	reg, err := Load(dir(t, goodFiles()))
	if err != nil {
		t.Fatal(err)
	}
	if d, err := reg.Resolve("", ""); err != nil || d.Name != DefaultName {
		t.Errorf("empty name: %v %v", d, err)
	}
	for _, bad := range [][2]string{{"ghost", ""}, {"regional_scout", ">=2.0"}, {"regional_scout", "1.0.4"}, {"regional_scout", "x"}} {
		if _, err := reg.Resolve(bad[0], bad[1]); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
	if _, err := reg.Resolve("ghost", ""); err == nil || !strings.Contains(err.Error(), "available: default, news_scanner, regional_scout") {
		t.Errorf("the error lists the agents: %v", err)
	}
}

func TestSatisfies(t *testing.T) {
	for _, c := range []struct {
		version, constraint string
		want                bool
	}{
		{"1.2.3", "", true}, {"1.2.3", "1.2.3", true}, {"1.2.3", "1.2", true}, {"1.2.3", "1", true}, {"1.2.3", "1.3", false}, {"1.2.3", "=1.2.3", true},
		{"1.2.3", ">=1.2.3", true}, {"1.2.3", ">1.2.3", false}, {"1.2.3", "<=1.2.3", true}, {"1.2.3", "<1.2.3", false}, {"1.2.3", ">=1.0, <2", true},
		{"2.0.0", ">=1.0, <2", false}, {"1.9.9", "^1.2", true}, {"2.0.0", "^1.2", false}, {"1.1.0", "^1.2", false}, {"0.10.0", ">0.9.5", true},
	} {
		got, err := satisfies(c.version, c.constraint)
		if err != nil || got != c.want {
			t.Errorf("%s %q: %v %v", c.version, c.constraint, got, err)
		}
	}
	for _, bad := range []string{"x", ">=", "1..2", "1.2.3.4", ">=1,", "01.2"} {
		if _, err := satisfies("1.0.0", bad); err == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestRoleStepsDefaults(t *testing.T) {
	child := Builtin()
	child.MaxSteps = 15
	for _, c := range []struct {
		role             Role
		wantDef, wantMax int
	}{
		{Role{}, 15, 15}, {Role{MaxSteps: 5}, 5, 15}, {Role{MaxStepsLimit: 8}, 8, 8}, {Role{MaxSteps: 3, MaxStepsLimit: 9}, 3, 9},
	} {
		if def, limit := c.role.Steps(child); def != c.wantDef || limit != c.wantMax {
			t.Errorf("%+v: %d %d", c.role, def, limit)
		}
	}
}
