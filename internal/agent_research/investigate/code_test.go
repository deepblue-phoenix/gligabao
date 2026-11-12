package investigate_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/agent"
	"github.com/deepblue-phoenix/gligabao/internal/agent/agent_defs"
	"github.com/deepblue-phoenix/gligabao/internal/agent/code_sandbox"
	"github.com/deepblue-phoenix/gligabao/internal/agent/code_sandbox/hostfn"
	"github.com/deepblue-phoenix/gligabao/internal/agent_research/investigate"
	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational/dbtest"
)

func TestFormatCodeResult(t *testing.T) {
	text, isErr := investigate.FormatCodeResult(code_sandbox.Result{
		Value:  0.8421,
		Stdout: "mean=0.8421 n=12\n",
	},
		nil,
		0)
	if isErr || text != "ok\nvalue: 0.8421\nstdout:\nmean=0.8421 n=12" {
		t.Errorf("%q %v",
			text,
			isErr)
	}
	text, isErr = investigate.FormatCodeResult(code_sandbox.Result{
		Stdout: strings.Repeat("x", 300),
	},
		nil,
		200)
	if isErr || !strings.Contains(text, "[truncated ") || len([]rune(text)) > 230 {
		t.Errorf("%q", text)
	}
	text, isErr = investigate.FormatCodeResult(code_sandbox.Result{},
		&code_sandbox.Error{
			Kind:    code_sandbox.KindRuntime,
			Message: "ZeroDivisionError",
			Line:    3,
		},
		0)
	if !isErr || text != "error: runtime: ZeroDivisionError (line 3)" {
		t.Errorf("%q", text)
	}
}

func TestCodeToolSpecListsTheRegistry(t *testing.T) {
	desc, schema := investigate.CodeToolSpec(hostfn.Standard())
	if !strings.Contains(desc, "- notes_read() -> str:") || !strings.Contains(desc, "- fetch_page(url: str, max_chars: int = 0) -> str:") {
		t.Errorf("description: %s", desc)
	}
	if schema["required"].([]string)[0] != "code" {
		t.Errorf("schema: %v", schema)
	}
}

// fakeSandbox runs a "program" written as the name of a host function to call, so the loop is tested without Monty.
type fakeSandbox struct{ runs []code_sandbox.Request }

func (f *fakeSandbox) Run(ctx context.Context,
	req code_sandbox.Request,
	host code_sandbox.HostFunc) (code_sandbox.Result, error) {
	f.runs = append(f.runs, req)
	switch strings.TrimSpace(req.Code) {
	case "fetch":
		out, err := host(ctx,
			"fetch_page",
			[]any{"https://a.com/p"},
			nil)
		if err != nil {
			return code_sandbox.Result{}, &code_sandbox.Error{
				Kind:    code_sandbox.KindHostFunction,
				Message: err.Error(),
			}
		}
		return code_sandbox.Result{
			Value:  len(out.(string)),
			Stdout: "fetched\n",
		}, nil
	case "frontier":
		qs, err := host(ctx,
			"frontier_list",
			nil,
			nil)
		return code_sandbox.Result{
			Value: len(qs.([]map[string]any)),
		}, err
	case "forbidden":
		_, err := host(ctx,
			"notes_append",
			nil,
			nil)
		return code_sandbox.Result{}, &code_sandbox.Error{
			Kind:    code_sandbox.KindHostFunction,
			Message: err.Error(),
		}
	}
	return code_sandbox.Result{
		Value: 42,
	}, nil
}

func TestRunCode(t *testing.T) {
	dbtest.Run(t, testRunCode)
}

func testRunCode(t *testing.T,
	d *sql.DB) {
	e := newEnv(t,
		d,
		loopLimits())
	sb := &fakeSandbox{}
	script := []reply{
		/* 1 planning */ {
			calls: []tc{{"add_question", m{"parent_id": "q_001", "question": "What is the weight size?", "priority": 0.8, "validation_criteria": "bytes"}}, {"run_code", m{"code": "1+1"}}},
		},
		/* 2 explore */ {
			calls: []tc{{"run_code", m{"code": "fetch"}}, {"run_code", m{"code": "frontier"}}, {"run_code", m{"code": "forbidden"}}, {"run_code", m{"code": ""}}},
		},
	}
	fm := &fakeModel{
		t:      t,
		script: script,
	}
	count := map[string]int{}
	_, err := investigate.Run(context.Background(), investigate.Config{
		Session:  e.sess,
		System:   "You are a researcher.",
		Task:     "Does X fit?",
		External: fakeTools(),
		Model: func(ctx context.Context,
			p map[string]any) (*ai.RawResponse, error) {
			if len(fm.script) == 0 {
				return nil, errors.New("script over")
			}
			return fm.call(ctx, p)
		},
		Hooks: investigate.Hooks{
			AfterTool: func(_ int,
				name,
				out string,
				isErr bool,
				_ investigate.Totals) {
				count[name]++
				e.outs[name+"#"+string(rune('0'+count[name]))] = out
				if isErr {
					e.outs[name+"#err"+string(rune('0'+count[name]))] = out
				}
			},
		},
		Code: &investigate.CodeConfig{
			Runner: sb,
			Settings: agent_defs.Code{
				Enabled: true,
			},
			Registry: hostfn.Standard(),
		},
	})
	if err == nil || err.Error() != "script over" {
		t.Fatalf("%v", err)
	}
	// Offered in the planning and the exploration turns.
	for i := 0; i < 2; i++ {
		if !slices.Contains(toolNames(fm.payloads[i]), "run_code") {
			t.Errorf("turn %d does not offer run_code: %v",
				i+1,
				toolNames(fm.payloads[i]))
		}
	}
	if !strings.Contains(fm.texts[0], "## Code") {
		t.Error("the prompt block")
	}
	// The description lists the functions the agent can serve: it has web_search and fetch_page but no query tool.
	var desc string
	for _, tl := range fm.payloads[0]["tools"].([]any) {
		if x := tl.(map[string]any); x["name"] == "run_code" {
			desc = x["description"].(string)
		}
	}
	if !strings.Contains(desc, "fetch_page(") || strings.Contains(desc, "kb_query") {
		t.Errorf("description: %s", desc)
	}
	if out := e.outs["run_code#1"]; out != "ok\nvalue: 42" {
		t.Errorf("first run: %q", out)
	}
	if out := e.outs["run_code#2"]; !strings.HasPrefix(out, "ok\nvalue: ") || !strings.Contains(out, "fetched") {
		t.Errorf("fetch run: %q", out)
	}
	if out := e.outs["run_code#err4"]; !strings.Contains(out, "host_function") || !strings.Contains(out, "not a function you may call") {
		t.Errorf("forbidden: %q", out)
	}
	if out := e.outs["run_code#err5"]; !strings.Contains(out, `missing "code"`) {
		t.Errorf("empty code: %q", out)
	}
	// The page read from the code is an attempt of the question being worked on.
	st, err := e.sess.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q, _ := st.Frontier.Get("q_002")
	if len(q.Attempts) != 1 || q.Attempts[0].Tool != "fetch_page" || q.Attempts[0].Query != "https://a.com/p" {
		t.Errorf("attempts: %+v", q.Attempts)
	}
	if len(sb.runs) < 3 || !slices.Contains(sb.runs[0].Functions, "fetch_page") || slices.Contains(sb.runs[0].Functions, "notes_append") {
		t.Errorf("requests: %+v", sb.runs)
	}
}

func TestWorkerCodeTool(t *testing.T) {
	sb := &fakeSandbox{}
	page := agent.Tool{
		Name: "fetch_page",
		Run: func(context.Context,
			json.RawMessage) (string, error) {
			return "hello", nil
		},
	}
	max := 1
	cfg := &investigate.CodeConfig{
		Runner: sb,
		Settings: agent_defs.Code{
			Enabled:  true,
			MaxCalls: &max,
		},
		Registry: hostfn.Standard(),
	}
	tool, ok, err := investigate.WorkerCodeTool(cfg, []agent.Tool{page})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v",
			ok,
			err)
	}
	if strings.Contains(tool.Description, "notes_read") || strings.Contains(tool.Description, "frontier_list") || !strings.Contains(tool.Description, "fetch_page") {
		t.Errorf("description: %s", tool.Description)
	}
	out, err := tool.Run(context.Background(), json.RawMessage(`{"code":"fetch"}`))
	if err != nil || !strings.Contains(out, "value: 5") {
		t.Errorf("%q %v",
			out,
			err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"code":"fetch"}`)); err == nil {
		t.Error("the second call should pass the max_calls limit")
	}
	if _, ok, _ := investigate.WorkerCodeTool(nil, nil); ok {
		t.Error("no config: no tool")
	}
}
