package memory

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_gitexport"
)

// LoadGitExport reads everything of userID's live dataset called name (empty means the default dataset) that
// memory_gitexport.Write builds a git repository from. It returns ErrDatasetNotFound if there is no such dataset. It
// reads the databases only and makes no LLM call.
func LoadGitExport(ctx context.Context, d *sql.DB, userID, name string) (*memory_gitexport.Input, error) {
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	name, err = memory_core.ResolveDatasetName(name)
	if err != nil {
		return nil, err
	}
	list, err := relational.ListDatasets(ctx, d, userID, false)
	if err != nil {
		return nil, err
	}
	var ds *relational.Dataset
	for i := range list {
		if list[i].Name == name {
			ds = &list[i]
		}
	}
	if ds == nil {
		return nil, memory_core.ErrDatasetNotFound
	}

	in := &memory_gitexport.Input{Name: ds.Name, Description: ds.Description, Tags: ds.Tags, CreatedAt: ds.CreatedAt, EdgeEvents: map[int64][]graph.EdgeEvent{}}
	if in.Artifacts, err = graph.ListArtifacts(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	if in.Entities, err = graph.ListEntities(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	if in.Edges, err = graph.ListGraphEdges(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	if in.EntityArtifacts, err = graph.ListEntityArtifacts(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	if in.EdgeArtifacts, err = graph.ListEdgeArtifacts(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	for _, a := range in.Artifacts { // the events of a call: those of its root artifact and everything under it
		if a.ParentID != 0 {
			continue
		}
		evs, err := graph.ListEdgeEvents(ctx, d, a.ID)
		if err != nil {
			return nil, err
		}
		if len(evs) > 0 {
			in.EdgeEvents[a.ID] = evs
		}
	}
	if in.Classes, err = graph.ListArtifactClasses(ctx, d, ds.ID); err != nil {
		return nil, err
	}
	if err := loadRunLogs(ctx, d, userID, ds, in); err != nil {
		return nil, err
	}
	models, err := referencedModels(ctx, d, in.Entities, in.Edges)
	if err != nil {
		return nil, err
	}
	in.Models = make(map[string]string, len(models))
	for _, m := range models {
		in.Models[m.ID] = m.Name
	}
	return in, nil
}

// runLogPage is how many runs are read at a time.
const runLogPage = 500

// loadRunLogs reads the remember and query runs of the dataset into in, with their inferences and the stage outputs. Runs are
// recorded by dataset name, so those of an earlier dataset of the user with the same name (deleted before this one was made) are
// left out: only runs that started after that deletion are taken.
func loadRunLogs(ctx context.Context, d *sql.DB, userID string, ds *relational.Dataset, in *memory_gitexport.Input) error {
	all, err := relational.ListDatasets(ctx, d, userID, true)
	if err != nil {
		return err
	}
	var since time.Time
	for _, o := range all {
		if o.Name == ds.Name && o.ID < ds.ID && o.DeletedAt != nil && o.DeletedAt.After(since) {
			since = *o.DeletedAt
		}
	}
	inferences := func(stages map[string]relational.RememberStage, into map[int64]relational.Inference) error {
		for _, st := range stages {
			for _, id := range st.InferenceIDs {
				if _, ok := into[id]; ok {
					continue
				}
				inf, err := relational.GetInference(ctx, d, id)
				if errors.Is(err, relational.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				into[id] = *inf
			}
		}
		return nil
	}

	filter := relational.RememberRunFilter{UserID: userID, Dataset: ds.Name}
	for offset, total := 0, 1; offset < total; offset += runLogPage {
		var runs []relational.RememberRun
		if runs, total, err = relational.ListRememberRuns(ctx, d, filter, runLogPage, offset); err != nil {
			return err
		}
		for _, run := range runs {
			if run.StartedAt.Before(since) {
				continue
			}
			l := memory_gitexport.RememberRunLog{Run: run, Inferences: map[int64]relational.Inference{}}
			if err := inferences(run.Stages, l.Inferences); err != nil {
				return err
			}
			if l.Outputs, err = relational.ListRememberStageOutputs(ctx, d, run.ID, ""); err != nil {
				return err
			}
			in.RememberRuns = append(in.RememberRuns, l)
		}
	}
	qfilter := relational.QueryRunFilter{UserID: userID, Dataset: ds.Name}
	for offset, total := 0, 1; offset < total; offset += runLogPage {
		var runs []relational.QueryRun
		if runs, total, err = relational.ListQueryRuns(ctx, d, qfilter, runLogPage, offset); err != nil {
			return err
		}
		for _, run := range runs {
			if run.StartedAt.Before(since) {
				continue
			}
			l := memory_gitexport.QueryRunLog{Run: run, Inferences: map[int64]relational.Inference{}}
			if err := inferences(run.Stages, l.Inferences); err != nil {
				return err
			}
			in.QueryRuns = append(in.QueryRuns, l)
		}
	}
	return nil
}
