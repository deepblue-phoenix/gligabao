package memory_temporal

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

const (
	// MaxCandidates is how many existing edges are shown to the LLM for the duplicate / contradiction check.
	MaxCandidates = 50

	ViaName    = "name"
	ViaSimilar = "similar"
	ViaBoth    = ViaName + "+" + ViaSimilar
)

// Candidates are the stored facts a Remember call compares its new facts with, and how they were found.
type Candidates struct {
	// Edges are the merged candidates; ByName and BySimilar are what each lookup found before merging (an edge found by
	// both is in both), and Names are the endpoint names (normalized) that the name lookup searched.
	Edges, ByName, BySimilar []graph.Edge
	Names                    []string
	// Via says how each edge (by ID) was found: ViaName, ViaSimilar or ViaBoth.
	Via map[int64]string
}

// AliasSearch finds, for the dataset dsID, the names of stored entities that are the same as an endpoint of the new edges but
// are not named alike (aliases, found by similarity of embeddings). A search that fails is the caller's to report: it
// returns what it found (maybe nothing), and the candidates are then those of the exact names.
type AliasSearch func(ctx context.Context,
	dsID int64) (names []string)

// LoadCandidates returns the live (not expired) edges of the user's dataset that the new edges may repeat or
// contradict, newest first, at most MaxCandidates. They are the union, merged by edge ID, of two lookups:
//   - by name: edges touching at least one entity named by an endpoint of a new edge (exact, normalized name);
//   - by similarity (only if search is not nil): edges touching a stored entity that search found, whose embedding is
//     close to an endpoint's, which finds aliases the names do not.
//
// A dataset that does not exist yet has none (and search is not called).
func LoadCandidates(ctx context.Context,
	d *sql.DB,
	userID,
	dataset string,
	edges []graph.Edge,
	search AliasSearch) (*Candidates, error) {
	out := &Candidates{
		Via: map[int64]string{},
	}
	dsID, err := relational.DatasetID(ctx,
		d,
		userID,
		dataset)
	if errors.Is(err, relational.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	seen := map[string]bool{}
	for _, e := range edges {
		for _, n := range []string{e.Source, e.Target} {
			if k := memory_core.Key(n); !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	out.Names = keys
	byName, err := graph.LoadCandidateEdges(ctx,
		d,
		dsID,
		keys,
		MaxCandidates)
	if err != nil {
		return nil, err
	}
	out.ByName = byName

	var aliasKeys []string
	if search != nil {
		for _, name := range search(ctx, dsID) {
			if k := memory_core.Key(name); !seen[k] {
				seen[k] = true
				aliasKeys = append(aliasKeys, k)
			}
		}
	}
	bySimilar, err := graph.LoadCandidateEdges(ctx,
		d,
		dsID,
		aliasKeys,
		MaxCandidates)
	if err != nil {
		return nil, err
	}
	out.BySimilar = bySimilar
	out.Edges = MergeCandidates(byName,
		bySimilar,
		MaxCandidates,
		out.Via)
	return out, nil
}

// MergeCandidates merges the edges found by name and by similarity: an edge found by both appears once (edges are
// identified by their ID), newest (highest ID) first, at most limit. via, if not nil, gets how each kept edge was found.
func MergeCandidates(byName,
	bySimilar []graph.Edge,
	limit int,
	via map[int64]string) []graph.Edge {
	how := map[int64]string{}
	var all []graph.Edge
	for _, e := range byName {
		if _, ok := how[e.ID]; !ok {
			how[e.ID] = ViaName
			all = append(all, e)
		}
	}
	for _, e := range bySimilar {
		if how[e.ID] == "" {
			how[e.ID] = ViaSimilar
			all = append(all, e)
		} else if how[e.ID] == ViaName {
			how[e.ID] = ViaBoth
		}
	}
	sort.SliceStable(all, func(i,
		j int) bool {
		return all[i].ID > all[j].ID
	})
	if len(all) > limit {
		all = all[:limit]
	}
	for _, e := range all {
		if via != nil {
			via[e.ID] = how[e.ID]
		}
	}
	return all
}
