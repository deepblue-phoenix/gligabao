package memory_gitexport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// The run logs are JSON files, written in the order of the pipeline's stages (a stage that never started is left out).

type inferenceOut struct {
	ID           int64     `json:"id"`
	Stage        string    `json:"stage,omitempty"`
	Model        string    `json:"model,omitempty"`
	Provider     string    `json:"provider,omitempty"`
	Reasoning    string    `json:"reasoning"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CostUSD      float64   `json:"cost_usd"`
	CreatedAt    time.Time `json:"created_at"`
}

type stageOut struct {
	Stage      string         `json:"stage"`
	Status     string         `json:"status"`
	Error      *string        `json:"error,omitempty"`
	StartedAt  *time.Time     `json:"started_at,omitempty"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Inferences []inferenceOut `json:"inferences,omitempty"`
}

type outputOut struct {
	ID        int64           `json:"id"`
	Stage     string          `json:"stage"`
	Name      string          `json:"name"`
	Label     string          `json:"label,omitempty"`
	Output    json.RawMessage `json:"output"`
	CreatedAt time.Time       `json:"created_at"`
}

type runOut struct {
	ID         int64          `json:"id"`
	Kind       string         `json:"kind,omitempty"`     // remember runs: the artifact kind
	Question   string         `json:"question,omitempty"` // query runs
	SessionID  string         `json:"session_id,omitempty"`
	Status     string         `json:"status"`
	Error      *string        `json:"error,omitempty"`
	Params     map[string]any `json:"params"`
	ArtifactID *int64         `json:"artifact_id,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
	Stages     []stageOut     `json:"stages"`
	Outputs    []outputOut    `json:"outputs,omitempty"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func stagesOut(names []string,
	stages map[string]relational.RememberStage,
	inferences map[int64]relational.Inference) []stageOut {
	out := []stageOut{}
	for _, name := range names {
		st, ok := stages[name]
		if !ok || st.Status == "" || st.Status == relational.RememberNotStarted {
			continue
		}
		so := stageOut{
			Stage:      name,
			Status:     st.Status,
			Error:      st.Error,
			StartedAt:  utcPtr(st.StartedAt),
			FinishedAt: utcPtr(st.FinishedAt),
		}
		for _, id := range st.InferenceIDs {
			inf, ok := inferences[id]
			if !ok {
				so.Inferences = append(so.Inferences, inferenceOut{
					ID:        id,
					Reasoning: "unknown",
				})
				continue
			}
			so.Inferences = append(so.Inferences, inferenceOut{
				ID:           inf.ID,
				Stage:        inf.Stage,
				Model:        inf.Model,
				Provider:     inf.Provider,
				Reasoning:    inf.Reasoning,
				InputTokens:  inf.InputTokens,
				OutputTokens: inf.OutputTokens,
				CostUSD:      inf.CostUSD,
				CreatedAt:    inf.CreatedAt.UTC(),
			})
		}
		out = append(out, so)
	}
	return out
}

func indentJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Sprintf("{\"error\": %s}\n", quote(err.Error()))
	}
	return b.String()
}

func rememberStageNames() []string {
	names := make([]string, len(relational.RememberStages))
	for i, s := range relational.RememberStages {
		names[i] = s.Name
	}
	return names
}

func queryStageNames() []string {
	names := make([]string, len(relational.QueryStages))
	for i, s := range relational.QueryStages {
		names[i] = s.Name
	}
	return names
}

func (r *renderer) rememberRunPath(id int64) string {
	return fmt.Sprintf("runs/remember/%06d.json", id)
}
func (r *renderer) queryRunPath(id int64) string { return fmt.Sprintf("runs/query/%06d.json", id) }

func (r *renderer) rememberRunFile(l RememberRunLog) string {
	run := l.Run
	out := runOut{
		ID:         run.ID,
		Kind:       run.Kind,
		Status:     run.Status,
		Error:      run.Error,
		Params:     run.Params,
		ArtifactID: run.ArtifactID,
		StartedAt:  run.StartedAt.UTC(),
		FinishedAt: utcPtr(run.FinishedAt),
		Stages: stagesOut(rememberStageNames(),
			run.Stages,
			l.Inferences),
	}
	if out.Params == nil {
		out.Params = map[string]any{}
	}
	for _, o := range l.Outputs {
		out.Outputs = append(out.Outputs, outputOut{
			ID:        o.ID,
			Stage:     o.Stage,
			Name:      o.Name,
			Label:     o.Label,
			Output:    o.Output,
			CreatedAt: o.CreatedAt.UTC(),
		})
	}
	return indentJSON(out)
}

func (r *renderer) queryRunFile(l QueryRunLog) string {
	run := l.Run
	out := runOut{
		ID:         run.ID,
		Question:   run.Question,
		SessionID:  run.SessionID,
		Status:     run.Status,
		Error:      run.Error,
		Params:     run.Params,
		StartedAt:  run.StartedAt.UTC(),
		FinishedAt: utcPtr(run.FinishedAt),
		Stages: stagesOut(queryStageNames(),
			run.Stages,
			l.Inferences),
	}
	if out.Params == nil {
		out.Params = map[string]any{}
	}
	return indentJSON(out)
}

// firstLine is the first line of s, cut to n runes.
func firstLine(s string,
	n int) string {
	return subject(s, n)
}

func (r *renderer) rememberRunMessage(run relational.RememberRun) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"remember run #%d %s",
		run.ID,
		run.Status)
	if run.Error != nil {
		b.WriteString(": " + firstLine(*run.Error, 60))
	}
	b.WriteString("\n\nNo artifact of this call is in the dataset (the call failed or was stopped, or its artifact was deleted).\n\n")
	fmt.Fprintf(&b,
		"Remember-Run-Id: %d\nKind: %s\nStatus: %s\n",
		run.ID,
		run.Kind,
		run.Status)
	return b.String()
}

func (r *renderer) queryRunMessage(run relational.QueryRun) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"query #%d: %s\n\nA question asked of the dataset; it does not change it.\n\n",
		run.ID,
		firstLine(run.Question, 60))
	fmt.Fprintf(&b,
		"Query-Run-Id: %d\nStatus: %s\n",
		run.ID,
		run.Status)
	if run.SessionID != "" {
		fmt.Fprintf(&b,
			"Session-Id: %s\n",
			run.SessionID)
	}
	if run.Error != nil {
		fmt.Fprintf(&b,
			"Error: %s\n",
			firstLine(*run.Error, 100))
	}
	return b.String()
}
