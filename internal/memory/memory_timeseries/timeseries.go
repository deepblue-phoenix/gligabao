// Package memory_timeseries stores timeseries and anchors them to the artifacts, entities and edges of the graph (see
// docs/timeseries.md). Remember calls Store inside its transaction for the series a caller supplied, and Attach serves
// the standalone endpoint. The points are never part of the graph: a series has tables of its own, and is linked to the
// graph by anchors.
package memory_timeseries

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// ErrAnchor means an anchor with on_unmatched "error" (or an ID) matched nothing in the dataset. Nothing is stored.
var ErrAnchor = errors.New("series anchor not found")

// Result is one stored series.
type Result struct {
	ID        int64  `json:"id"`
	Key       string `json:"key"`
	Field     string `json:"field"`
	Name      string `json:"name"`
	Unit      string `json:"unit,omitempty"`
	Frequency string `json:"frequency,omitempty"`
	// Created says the series is new; otherwise points were added to (or replaced in) a stored series.
	Created bool `json:"created"`
	// Points is how many points the request carried; PointCount is how many the series has now.
	Points     int       `json:"points"`
	PointCount int       `json:"point_count"`
	FirstTS    time.Time `json:"first_ts"`
	LastTS     time.Time `json:"last_ts"`
	// Stats summarize all the points of the series.
	Stats   *Stats         `json:"stats"`
	Anchors []AnchorResult `json:"anchors"`
	// Skipped lists the anchors that matched nothing and were ignored (on_unmatched "ignore"), as messages.
	Skipped []string `json:"skipped_anchors,omitempty"`
}

// AnchorResult is one anchor that was stored.
type AnchorResult struct {
	Type   string `json:"type"` // "artifact", "entity" or "edge"
	ID     int64  `json:"id"`
	Role   string `json:"role,omitempty"`
	Window string `json:"window,omitempty"` // "start/end", RFC 3339
	// Created says the entity was created by the anchor (on_unmatched "create"): the text did not mention it.
	Created bool `json:"created,omitempty"`
}

// Store writes the series and their points, refreshes their stats, and anchors them, inside tx (the transaction of
// Remember, or of Attach). dsID is the dataset; artifactID is the artifact the call created (0 if none, as for Attach):
// a series without anchors is anchored to it. specs are validated again with memory_core.ResolveSeries. An anchor that
// cannot be resolved fails with ErrAnchor (so the transaction should be rolled back), unless it says on_unmatched
// "ignore".
func Store(ctx context.Context, tx graph.Tx, dsID, artifactID int64, now time.Time, specs []memory_core.SeriesSpec) ([]Result, error) {
	specs, err := memory_core.ResolveSeries(specs, artifactID != 0)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(specs))
	for _, s := range specs {
		r, err := storeOne(ctx, tx, dsID, artifactID, now, s)
		if err != nil {
			return nil, fmt.Errorf("series %q: %w", s.Key, err)
		}
		out = append(out, *r)
	}
	return out, nil
}

func storeOne(ctx context.Context, tx graph.Tx, dsID, artifactID int64, now time.Time, s memory_core.SeriesSpec) (*Result, error) {
	id, created, err := tx.UpsertSeries(ctx, graph.NewSeries{DatasetID: dsID, Key: s.Key, Field: s.Field, Name: s.Name, Unit: s.Unit,
		Frequency: s.Frequency, Source: graph.SeriesSupplied, CreatedAt: now})
	if err != nil {
		return nil, err
	}
	pts := make([]graph.SeriesPoint, len(s.Points))
	for i, p := range s.Points {
		pts[i] = graph.SeriesPoint{TS: p.TS, Value: p.Value}
	}
	if err := tx.UpsertSeriesPoints(ctx, id, pts); err != nil {
		return nil, err
	}

	// The summary covers every point of the series, not just those of this request.
	all, err := tx.ListSeriesPoints(ctx, id)
	if err != nil {
		return nil, err
	}
	stats := Summarize(all)
	js, err := json.Marshal(stats)
	if err != nil {
		return nil, err
	}
	if err := tx.UpdateSeriesSummary(ctx, id, &stats.FirstTS, &stats.LastTS, stats.Count, string(js), now); err != nil {
		return nil, err
	}
	r := &Result{ID: id, Key: s.Key, Field: s.Field, Name: s.Name, Unit: s.Unit, Frequency: s.Frequency, Created: created,
		Points: len(pts), PointCount: stats.Count, FirstTS: stats.FirstTS, LastTS: stats.LastTS, Stats: stats, Anchors: []AnchorResult{}}

	anchors := s.Anchors
	if len(anchors) == 0 && artifactID != 0 {
		anchors = []memory_core.SeriesAnchor{{Artifact: true}}
	}
	for _, a := range anchors {
		typ, targetID, madeEntity, err := resolveAnchor(ctx, tx, dsID, artifactID, now, a)
		if errors.Is(err, graph.ErrNotFound) {
			if a.OnUnmatched == memory_core.OnUnmatchedIgnore {
				r.Skipped = append(r.Skipped, a.Describe()+" not found")
				continue
			}
			return nil, fmt.Errorf("%w: %s", ErrAnchor, a.Describe())
		}
		if err != nil {
			return nil, err
		}
		if err := tx.AnchorSeries(ctx, graph.NewSeriesAnchor{SeriesID: id, Type: typ, ID: targetID, Role: a.Role,
			WindowStart: a.WindowStart, WindowEnd: a.WindowEnd, CreatedAt: now}); err != nil {
			return nil, err
		}
		ar := AnchorResult{Type: typ, ID: targetID, Role: a.Role, Created: madeEntity}
		if a.WindowStart != nil {
			ar.Window = relational.FormatTime(*a.WindowStart) + "/" + relational.FormatTime(*a.WindowEnd)
		}
		r.Anchors = append(r.Anchors, ar)
	}
	return r, nil
}

// resolveAnchor finds what the anchor points at: its type and ID. It returns graph.ErrNotFound if there is no such
// thing and the anchor cannot create it. created is true if the anchor created the entity.
func resolveAnchor(ctx context.Context, tx graph.Tx, dsID, artifactID int64, now time.Time, a memory_core.SeriesAnchor) (typ string, id int64, created bool, err error) {
	switch {
	case a.Artifact:
		return graph.AnchorArtifact, artifactID, false, nil
	case a.ArtifactID != 0:
		return graph.AnchorArtifact, a.ArtifactID, false, tx.CheckArtifact(ctx, dsID, a.ArtifactID)
	case a.EntityID != 0:
		_, err = tx.GetEntity(ctx, dsID, a.EntityID)
		return graph.AnchorEntity, a.EntityID, false, err
	case a.EdgeID != 0:
		_, err = tx.GetEdge(ctx, dsID, a.EdgeID)
		return graph.AnchorEdge, a.EdgeID, false, err
	case a.Edge != nil:
		id, err = tx.FindEdgeID(ctx, dsID, memory_core.Key(a.Edge.Source), a.Edge.Relation, memory_core.Key(a.Edge.Target))
		return graph.AnchorEdge, id, false, err
	case a.Entity != nil:
		key := memory_core.Key(a.Entity.Name)
		if a.Entity.Type == "" {
			id, err = tx.FindEntityID(ctx, dsID, key)
		} else {
			id, err = tx.FindEntity(ctx, dsID, key, a.Entity.Type)
		}
		if errors.Is(err, graph.ErrNotFound) && a.OnUnmatched == memory_core.OnUnmatchedCreate {
			// The caller says this is the entity's series, so the entity exists even if the text never named it. It is
			// not linked to any artifact: no text mentioned it.
			typ := a.Entity.Type
			if typ == "" {
				typ = "other"
			}
			id, err = tx.UpsertEntity(ctx, dsID, graph.Entity{Name: a.Entity.Name, Type: typ}, key)
			return graph.AnchorEntity, id, err == nil, err
		}
		return graph.AnchorEntity, id, false, err
	}
	return "", 0, false, errors.New("series anchor selects nothing")
}

// Attach stores the series in one transaction in the dataset dsID (which must exist), for the standalone endpoint:
// there is no artifact, so a series is anchored only where its anchors say (a series with none is stored unanchored).
func Attach(ctx context.Context, d *sql.DB, dsID int64, specs []memory_core.SeriesSpec) ([]Result, error) {
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := Store(ctx, tx, dsID, 0, relational.Now(), specs)
	if err != nil {
		return nil, err
	}
	return res, tx.Commit()
}
