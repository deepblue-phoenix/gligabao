package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type fakeTeam struct {
	calls []string
	roles []string
}

func (f *fakeTeam) Spawn(_ context.Context, role, name, reason, task string, maxSteps int) (string, error) {
	f.roles = append(f.roles, role)
	f.calls = append(f.calls, fmt.Sprintf("spawn %s|%s|%s|%d", name, reason, task, maxSteps))
	return "ok", nil
}
func (f *fakeTeam) Wait(_ context.Context, names []string) (string, error) {
	f.calls = append(f.calls, "wait "+strings.Join(names, ","))
	return "ok", nil
}
func toolNames(a *Agent) string {
	var names []string
	for _, t := range a.Tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ",")
}

func TestAddTeamTools(t *testing.T) {
	plain := ResearchAgent(nil, nil, nil)
	base := toolNames(plain)
	for _, c := range []struct {
		tools TeamTools
		extra string
	}{
		{TeamTools{}, ""},
		{TeamTools{Spawn: true}, ",spawn_subagent,wait_subagents"},
	} {
		a := ResearchAgent(nil, nil, nil)
		AddTeamTools(a, &fakeTeam{}, c.tools)
		if got := toolNames(a); got != base+c.extra {
			t.Errorf("%+v: tools %s", c.tools, got)
		}
		if (c.extra == "") != (a.System == plain.System) {
			t.Errorf("%+v: the prompt should change with the tools", c.tools)
		}
	}
}

func TestTeamToolsCheckTheirInput(t *testing.T) {
	ft := &fakeTeam{}
	a := ResearchAgent(nil, nil, nil)
	AddTeamTools(a, ft, TeamTools{Spawn: true})
	tools := map[string]Tool{}
	for _, tl := range a.Tools {
		tools[tl.Name] = tl
	}
	run := func(name, input string) error {
		_, err := tools[name].Run(context.Background(), json.RawMessage(input))
		return err
	}
	for name, bad := range map[string][]string{
		ToolSpawnSubagent: {`{}`, `{"name":"a b","reason":"r","task":"x"}`, `{"name":"a","reason":"r","task":" "}`, `{"name":"a","task":"x"}`, `{"name":"a","reason":" ","task":"x"}`,
			`{"name":"a","reason":"` + strings.Repeat("r", subagentReasonChars+1) + `","task":"x"}`, `{"name":"a","reason":"r","task":"x","max_steps":99}`, `nope`},
	} {
		for _, in := range bad {
			if err := run(name, in); err == nil {
				t.Errorf("%s %s should fail", name, in)
			}
		}
	}
	if len(ft.calls) != 0 {
		t.Fatalf("bad input reached the team: %v", ft.calls)
	}
	for _, c := range [][2]string{
		{ToolSpawnSubagent, `{"name":"a-1","reason":"covers Asia","task":"do it","max_steps":7}`},
		{ToolWaitSubagents, `{"names":["a","b"]}`},
	} {
		if err := run(c[0], c[1]); err != nil {
			t.Errorf("%s: %v", c[0], err)
		}
	}
	if err := run(ToolWaitSubagents, `{}`); err != nil {
		t.Fatal(err)
	}
	want := "spawn a-1|covers Asia|do it|7;wait a,b;wait "
	if got := strings.Join(ft.calls, ";"); got != want {
		t.Errorf("calls: %s", got)
	}
}

func TestSpawnToolWithRoles(t *testing.T) {
	ft := &fakeTeam{}
	a := ResearchAgent(nil, nil, nil)
	AddTeamTools(a, ft, TeamTools{
		Spawn: true, Roles: []Role{{Name: "regional", Does: "researches one region", When: "many regions"}, {Name: "fact_checker", Does: "checks claims"}},
		Prompt: "ROLES PROMPT",
	})
	if !strings.HasSuffix(a.System, "ROLES PROMPT") || strings.Contains(a.System, "really warranted") {
		t.Errorf("the roles prompt replaces the free-form one: %s", a.System)
	}
	var spawn Tool
	for _, tl := range a.Tools {
		if tl.Name == ToolSpawnSubagent {
			spawn = tl
		}
	}
	props := spawn.Schema["properties"].(map[string]any)
	role, ok := props["role"].(map[string]any)
	if !ok || fmt.Sprint(role["enum"]) != "[regional fact_checker]" {
		t.Fatalf("role property: %v", props["role"])
	}
	if req := spawn.Schema["required"].([]string); req[0] != "role" {
		t.Errorf("required: %v", req)
	}
	run := func(in string) error {
		_, err := spawn.Run(context.Background(), json.RawMessage(in))
		return err
	}
	for _, bad := range []string{`{"name":"a","reason":"r","task":"x"}`, `{"role":"nope","name":"a","reason":"r","task":"x"}`} {
		if err := run(bad); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	if err := run(`{"role":"regional","name":"asia","reason":"r","task":"x"}`); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ft.roles) != "[regional]" {
		t.Errorf("roles passed to the team: %v", ft.roles)
	}

	// Without roles, a role is refused.
	free := ResearchAgent(nil, nil, nil)
	AddTeamTools(free, ft, TeamTools{Spawn: true})
	for _, tl := range free.Tools {
		if tl.Name == ToolSpawnSubagent {
			if _, err := tl.Run(context.Background(), json.RawMessage(`{"role":"x","name":"a","reason":"r","task":"x"}`)); err == nil {
				t.Error("a role without roles should fail")
			}
		}
	}
}

func TestWaitAloneForSubagentsThatWereStarted(t *testing.T) {
	a := ResearchAgent(nil, nil, nil)
	base := toolNames(a)
	AddTeamTools(a, &fakeTeam{}, TeamTools{Wait: true, Prompt: "STARTED FOR YOU"})
	if got := toolNames(a); got != base+",wait_subagents" || !strings.HasSuffix(a.System, "STARTED FOR YOU") {
		t.Errorf("tools %s, prompt %s", got, a.System)
	}
}
