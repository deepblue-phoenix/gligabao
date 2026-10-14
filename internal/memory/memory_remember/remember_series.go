package memory_remember

// The timeseries a caller supplies with a text (see docs/timeseries.md). A text that is one chunk stores them inside
// the transaction of its artifact (see store). A chunked text cannot: its chunks are stored one transaction each, and
// an anchor may select an entity that only a later chunk yields, so the series wait until all the chunks are stored and
// are then stored, anchored to the parent artifact, in a transaction of their own.

import (
	"context"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
)

// storeSeries stores the series of the call (j.co.Series), anchored to the artifact artifactID, in a transaction.
func (j *job) storeSeries(ctx context.Context, artifactID int64) ([]memory_timeseries.Result, error) {
	dsID, err := relational.EnsureDataset(ctx, j.d, j.userID, j.dataset)
	if err != nil {
		return nil, err
	}
	tx, err := graph.Begin(ctx, j.d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := memory_timeseries.Store(ctx, tx, dsID, artifactID, j.now, j.co.Series)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit()
}

// traceSeries adds the step of the stored series to the trajectory (nothing if there are none).
func (j *job) traceSeries(artifactID int64, series []memory_timeseries.Result) {
	if len(series) == 0 {
		return
	}
	j.tr.Tool("Stored the timeseries the caller supplied, with their stats, and anchored them (to the artifact when "+
		"they name no anchor; entity and edge selectors are resolved against what was just stored).", "store_timeseries",
		map[string]any{"user_id": j.userID, "dataset": j.dataset, "artifact_id": artifactID, "series": len(series)},
		map[string]any{"series": series})
	j.noteSuppliedSeries(artifactID, series)
}
