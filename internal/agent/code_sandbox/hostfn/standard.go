package hostfn

import (
	"context"
	"errors"
	"fmt"
)

// Names of the tools of the agent that the fetch class functions run.
const (
	ToolWebSearch = "web_search"
	ToolFetchPage = "fetch_page"
	ToolQuery     = "query"
)

// ToolOf is the tool of the agent a fetch class function needs ("" for the others).
func ToolOf(name string) string {
	switch name {
	case "web_search":
		return ToolWebSearch
	case "fetch_page":
		return ToolFetchPage
	case "kb_query":
		return ToolQuery
	}
	return ""
}

func str(name string,
	required bool) Param {
	p := Param{
		Name:     name,
		Type:     "str",
		Required: required,
	}
	if !required {
		p.Default = ""
	}
	return p
}

func fetch(tool,
	argName string) Handler {
	return func(ctx context.Context,
		env Env,
		a Args) (any, error) {
		if env.Fetch == nil {
			return nil, errors.New("not available in this run")
		}
		out, err := env.Fetch(ctx,
			tool,
			map[string]any{
				argName: a.Str(argName),
			})
		if err != nil {
			return nil, err
		}
		if n := a.Int("max_chars"); n > 0 {
			if r := []rune(out); len(r) > n {
				out = string(r[:n])
			}
		}
		return out, nil
	}
}

func filterStatus(items []map[string]any,
	status string) []map[string]any {
	if status == "" {
		return items
	}
	out := []map[string]any{}
	for _, it := range items {
		if s, _ := it["status"].(string); s == status {
			out = append(out, it)
		}
	}
	return out
}

// Standard is the first set of functions (docs/agents/monty-code-execution.md): they read the state of the run or fetch like the model's
// own tools do; none of them changes the frontier, the notes or the claims.
func Standard() Registry {
	r, err := New(
		Spec{
			Name:   "notes_read",
			Doc:    "the current notes.md text",
			Result: "str",
			Class:  ClassRead,
			Handler: func(_ context.Context,
				env Env,
				_ Args) (any, error) {
				return env.Notes, nil
			},
		},
		Spec{
			Name:   "frontier_get",
			Doc:    "one question with its status, answer, criteria and attempt log",
			Result: "dict",
			Class:  ClassRead,
			Params: []Param{str("id", true)},
			Handler: func(_ context.Context,
				env Env,
				a Args) (any, error) {
				for _, q := range env.Questions {
					if q["id"] == a.Str("id") {
						return q, nil
					}
				}
				return nil, fmt.Errorf("no question %q", a.Str("id"))
			},
		},
		Spec{
			Name:   "frontier_list",
			Doc:    "the questions, optionally of one status",
			Result: "list[dict]",
			Class:  ClassRead,
			Params: []Param{str("status", false)},
			Handler: func(_ context.Context,
				env Env,
				a Args) (any, error) {
				return filterStatus(env.Questions, a.Str("status")), nil
			},
		},
		Spec{
			Name:   "claims_list",
			Doc:    "the claim ledger (id, text, status, sources, value and unit of a datapoint), optionally of one status",
			Result: "list[dict]",
			Class:  ClassRead,
			Params: []Param{str("status", false)},
			Handler: func(_ context.Context,
				env Env,
				a Args) (any, error) {
				return filterStatus(env.Claims, a.Str("status")), nil
			},
		},
		Spec{
			Name:   "fetch_page",
			Doc:    "the text of a page, like the tool of the same name (its limits and cost count); max_chars cuts the text",
			Result: "str",
			Class:  ClassFetch,
			Params: []Param{str("url", true), {
				Name:    "max_chars",
				Type:    "int",
				Default: 0,
			}},
			Handler: fetch(ToolFetchPage, "url"),
		},
		Spec{
			Name:   "web_search",
			Doc:    "search results, like the tool of the same name (its limits and cost count)",
			Result: "str",
			Class:  ClassFetch,
			Params: []Param{str("query", true), {
				Name:    "max_chars",
				Type:    "int",
				Default: 0,
			}},
			Handler: fetch(ToolWebSearch, "query"),
		},
		Spec{
			Name:   "kb_query",
			Doc:    "query the knowledge base, like the query tool",
			Result: "str",
			Class:  ClassFetch,
			Params: []Param{str("question", true), {
				Name:    "max_chars",
				Type:    "int",
				Default: 0,
			}},
			Handler: fetch(ToolQuery, "question"),
		},
		Spec{
			Name:   "log",
			Doc:    "add a line to the trace of this call (not shown to you)",
			Result: "None",
			Class:  ClassRead,
			Params: []Param{str("message", true)},
			Handler: func(_ context.Context,
				env Env,
				a Args) (any, error) {
				if env.Log != nil {
					env.Log(a.Str("message"))
				}
				return nil, nil
			},
		},
	)
	if err != nil {
		panic("hostfn: the standard registry is invalid: " + err.Error()) // a programming error, caught by the tests
	}
	return r
}
