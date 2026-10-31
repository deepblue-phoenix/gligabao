package memory_temporal

import (
	"context"
	"errors"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// StoreEdges writes edges inside tx according to plan and returns the resulting edges (new ones, or the
// existing ones they duplicate) plus the IDs of edges that were invalidated. entityID maps an entity name to
// its ID, creating the entity if needed. Every edge this artifact states, new or a duplicate of a stored one, is
// linked to the artifact and, if sessionID is not empty, to its session. Each fact it creates, confirms, ends, invalidates
// or supersedes gets an edge event (see graph.EdgeEvent) in the same transaction.
func StoreEdges(ctx context.Context, tx graph.Tx, dsID, artID int64, sessionID string, ref, now time.Time, edges []graph.Edge, plan *EdgePlan,
	entityID func(name string) (int64, error)) ([]graph.Edge, []int64, error) {
	if plan == nil {
		plan = &EdgePlan{}
	}
	link := func(edgeID int64) error {
		if err := tx.LinkEdge(ctx, edgeID, artID); err != nil {
			return err
		}
		if sessionID == "" {
			return nil
		}
		return tx.LinkEdgeSession(ctx, edgeID, sessionID)
	}
	ids := make([]int64, len(edges)) // stored ID for each new edge
	batch := map[string]int64{}      // repeats inside this artifact
	var order []int64
	inOrder := map[int64]bool{}

	for i, e := range edges {
		if existing, ok := plan.Duplicates[i]; ok {
			// Same fact seen again: keep the stored edge, just record its end if the text says it ended.
			stored, err := tx.GetEdge(ctx, dsID, existing)
			if err != nil {
				return nil, nil, err
			}
			if err := tx.MergeDuplicateEdge(ctx, dsID, existing, e.InvalidAt); err != nil {
				return nil, nil, err
			}
			ev := graph.EdgeEvent{DatasetID: dsID, EdgeID: existing, ArtifactID: artID, Event: graph.EdgeConfirmed,
				InvalidAtBefore: stored.InvalidAt, InvalidAtAfter: stored.InvalidAt, CreatedAt: now}
			if stored.InvalidAt == nil && e.InvalidAt != nil {
				ev.Event, ev.InvalidAtAfter = graph.EdgeEnded, e.InvalidAt
			}
			if err := tx.InsertEdgeEvent(ctx, ev); err != nil {
				return nil, nil, err
			}
			// The stored edge keeps its own artifact_id (its creator); this artifact is recorded as another source.
			if err := link(existing); err != nil {
				return nil, nil, err
			}
			ids[i] = existing
			if !inOrder[existing] {
				inOrder[existing] = true
				order = append(order, existing)
			}
			continue
		}
		bk := memory_core.Key(e.Source) + "\x00" + memory_core.Key(e.Target) + "\x00" + e.Relation
		if id, ok := batch[bk]; ok {
			ids[i] = id
			continue
		}
		src, err := entityID(e.Source)
		if err != nil {
			return nil, nil, err
		}
		dst, err := entityID(e.Target)
		if err != nil {
			return nil, nil, err
		}
		validAt := ref.UTC()
		if e.ValidAt != nil {
			validAt = e.ValidAt.UTC()
		}
		id, err := tx.InsertEdge(ctx, graph.NewEdge{DatasetID: dsID, SourceID: src, TargetID: dst, Relation: e.Relation,
			Description: e.Description, ValidAt: validAt, InvalidAt: e.InvalidAt, CreatedAt: now, ArtifactID: artID, ModelID: e.ModelID})
		if err != nil {
			return nil, nil, err
		}
		if err := link(id); err != nil {
			return nil, nil, err
		}
		if err := tx.InsertEdgeEvent(ctx, graph.EdgeEvent{DatasetID: dsID, EdgeID: id, ArtifactID: artID, Event: graph.EdgeCreated,
			InvalidAtAfter: e.InvalidAt, CreatedAt: now}); err != nil {
			return nil, nil, err
		}
		ids[i] = id
		batch[bk] = id
		inOrder[id] = true
		order = append(order, id)
	}

	var invalidated []int64
	for _, c := range plan.Contradictions {
		if c.New < 0 || c.New >= len(edges) {
			continue
		}
		inv, err := contradict(ctx, tx, dsID, artID, ids[c.New], c.Existing, ref, now)
		if err != nil {
			return nil, nil, err
		}
		invalidated = append(invalidated, inv...)
	}

	out := make([]graph.Edge, 0, len(order))
	for _, id := range order {
		e, err := tx.GetEdge(ctx, dsID, id)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *e)
	}
	return out, invalidated, nil
}

// contradict applies Graphiti's invalidation rule to a new edge and an existing one that conflicts with it.
// The fact that started earlier is the one that ended: it gets invalid_at = start of the later fact and
// expired_at = now. Facts whose periods don't overlap are left alone. It returns the IDs invalidated, and logs an edge
// event (invalidated, or superseded when the new fact is the one closed) for the artifact artID.
func contradict(ctx context.Context, tx graph.Tx, dsID, artID, newID, oldID int64, ref, now time.Time) ([]int64, error) {
	if newID == oldID {
		return nil, nil
	}
	nw, err := tx.GetEdge(ctx, dsID, newID)
	if err != nil {
		return nil, err
	}
	old, err := tx.GetEdge(ctx, dsID, oldID)
	if errors.Is(err, graph.ErrNotFound) { // deleted (or from another dataset) since the candidates were read
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if old.ExpiredAt != nil {
		return nil, nil
	}

	newStart := ref.UTC()
	if nw.ValidAt != nil {
		newStart = *nw.ValidAt
	}
	// No overlap: the old one ended before the new one began, or the new one ended before the old began.
	if old.InvalidAt != nil && !old.InvalidAt.After(newStart) {
		return nil, nil
	}
	if nw.InvalidAt != nil && old.ValidAt != nil && !nw.InvalidAt.After(*old.ValidAt) {
		return nil, nil
	}

	loser, endsAt := old, newStart
	ev := graph.EdgeEvent{DatasetID: dsID, ArtifactID: artID, Event: graph.EdgeInvalidated, CausedByEdgeID: newID, CreatedAt: now}
	if old.ValidAt != nil && old.ValidAt.After(newStart) {
		// The new information is about an earlier time than what we already know: the new fact is the one superseded.
		loser, endsAt = nw, *old.ValidAt
		ev.Event, ev.CausedByEdgeID = graph.EdgeSuperseded, oldID
	}
	if err := tx.InvalidateEdge(ctx, loser.ID, endsAt, now); err != nil {
		return nil, err
	}
	ev.EdgeID, ev.InvalidAtBefore, ev.InvalidAtAfter = loser.ID, loser.InvalidAt, &endsAt
	if err := tx.InsertEdgeEvent(ctx, ev); err != nil {
		return nil, err
	}
	return []int64{loser.ID}, nil
}
