package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeTeam struct{ calls []string }

func (f *fakeTeam) Spawn(_ context.Context, name, reason, task string, maxSteps int) (string, error) {
	f.calls = append(f.calls, fmt.Sprintf("spawn %s|%s|%s|%d", name, reason, task, maxSteps))
	return "ok", nil
}
func (f *fakeTeam) Wait(_ context.Context, names []string) (string, error) {
	f.calls = append(f.calls, "wait "+strings.Join(names, ","))
	return "ok", nil
}
func (f *fakeTeam) Send(_ context.Context, to, text string) (string, error) {
	f.calls = append(f.calls, "send "+to+"|"+text)
	return "ok", nil
}
func (f *fakeTeam) Siblings(_ context.Context) (string, error) {
	f.calls = append(f.calls, "siblings")
	return "ok", nil
}
func (f *fakeTeam) Receive(_ context.Context, wait time.Duration) (string, error) {
	f.calls = append(f.calls, "receive "+wait.String())
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
		{TeamTools{Messaging: true}, ",list_siblings,send_message,read_messages"},
		{TeamTools{Spawn: true, Messaging: true}, ",spawn_subagent,wait_subagents,list_siblings,send_message,read_messages"},
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
	AddTeamTools(a, ft, TeamTools{Spawn: true, Messaging: true})
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
		ToolSendMessage:   {`{"to":"a"}`, `{"text":"x"}`, `{"to":"a","text":"` + strings.Repeat("x", maxMessageChars+1) + `"}`},
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
		{ToolListSiblings, `{}`},
		{ToolWaitSubagents, `{"names":["a","b"]}`},
		{ToolSendMessage, `{"to":"b","text":"hi"}`},
		{ToolReadMessages, `{"wait_seconds":600}`},
	} {
		if err := run(c[0], c[1]); err != nil {
			t.Errorf("%s: %v", c[0], err)
		}
	}
	if err := run(ToolWaitSubagents, `{}`); err != nil {
		t.Fatal(err)
	}
	want := "spawn a-1|covers Asia|do it|7;siblings;wait a,b;send b|hi;receive 1m0s;wait "
	if got := strings.Join(ft.calls, ";"); got != want {
		t.Errorf("calls: %s", got)
	}
}
