package knowledge

import (
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// The checks of a knowledge case against what was stored in its dataset. None of them depends on the LLM except where it is said.
// Each reports through check: a held check (pass) or a failed one (fail).

// rememberedChunk is a chunk as reported by the remember response.
type rememberedChunk struct {
	ArtifactID  int64 `json:"artifact_id"`
	Index       int   `json:"index"`
	Tokens      int   `json:"tokens"`
	StartOffset int   `json:"start_offset"`
	EndOffset   int   `json:"end_offset"`
}

// checkChunks verifies the chunking of the case's texts, using the chunks the remember calls reported and what is
// stored in the dataset. None of it depends on what the LLM extracts, except the spread of the facts over the chunks:
//   - at least ExpectMinChunks chunks, none larger than the chunk size;
//   - each chunk is a stored child artifact of one of the texts, numbered from 0, and the stored ones are exactly the
//     reported ones (no chunk lost, none stored twice);
//   - the chunks of a text cover it: the first starts at 0, each one starts at or before the end of the previous one
//     (the overlap) and after its start, and the last ends at the end of the text;
//   - the facts were linked to the chunks (never to the parent), and to at least half of them: a corpus that spreads
//     facts evenly must produce edges from the whole text, not just from its start.
func checkChunks(ctx context.Context, database *db.DB, dataset string, minChunks int, parents []int64, reported []rememberedChunk,
	check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, EvalUser, dataset)
	if err != nil {
		return err
	}
	arts, err := graph.ListArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	byID := map[int64]graph.Artifact{}
	children := map[int64][]graph.Artifact{} // parent ID -> its chunks
	for _, a := range arts {
		byID[a.ID] = a
		if a.IsChunk() { // not the web searches that hang off an artifact
			children[a.ParentID] = append(children[a.ParentID], a)
		}
	}

	size := database.Chunking.WithDefaults().Size
	check(len(reported) >= minChunks, fmt.Sprintf("chunking: %d chunks (at least %d)", len(reported), minChunks),
		fmt.Sprintf("chunking: %d chunks, want at least %d", len(reported), minChunks))
	largest := 0
	for _, r := range reported {
		largest = max(largest, r.Tokens)
	}
	check(largest <= size, fmt.Sprintf("chunking: largest chunk is %d tokens (chunk size %d)", largest, size),
		fmt.Sprintf("chunking: a chunk has %d tokens, over the chunk size %d", largest, size))

	stored := 0
	for _, p := range parents {
		stored += len(children[p])
	}
	check(stored == len(reported), fmt.Sprintf("chunking: %d chunk artifacts stored, as reported", stored),
		fmt.Sprintf("chunking: %d chunk artifacts stored, %d reported", stored, len(reported)))

	// Coverage, text by text.
	ok, problem := true, ""
	for _, p := range parents {
		parent, kids := byID[p], children[p]
		for i, k := range kids {
			var prevEnd int
			if i > 0 {
				prevEnd = kids[i-1].EndOffset
			}
			switch {
			case k.ChunkIndex != i:
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d has chunk_index %d", p, i, k.ChunkIndex)
			case i == 0 && k.StartOffset != 0:
				ok, problem = false, fmt.Sprintf("artifact %d: first chunk starts at %d, not 0", p, k.StartOffset)
			case i > 0 && (k.StartOffset > prevEnd || k.StartOffset <= kids[i-1].StartOffset):
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d starts at %d (previous: %d-%d)", p, i, k.StartOffset, kids[i-1].StartOffset, prevEnd)
			case k.EndOffset > len(parent.Text) || parent.Text[k.StartOffset:k.EndOffset] != k.Text:
				ok, problem = false, fmt.Sprintf("artifact %d: chunk %d is not text[%d:%d] of its parent", p, i, k.StartOffset, k.EndOffset)
			}
		}
		if len(kids) > 0 && kids[len(kids)-1].EndOffset != len(parent.Text) {
			ok, problem = false, fmt.Sprintf("artifact %d: the last chunk ends at %d, the text at %d", p, kids[len(kids)-1].EndOffset, len(parent.Text))
		}
	}
	check(ok, "chunking: the chunks are consecutive, overlap, and cover the whole text", "chunking: "+problem)

	// Provenance.
	links, err := graph.ListEdgeArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	isParent := map[int64]bool{}
	for _, p := range parents {
		isParent[p] = true
	}
	linkedChunks := map[int64]bool{}
	toParent := 0
	for _, l := range links {
		if isParent[l.ArtifactID] {
			toParent++
		} else if byID[l.ArtifactID].IsChunk() {
			linkedChunks[l.ArtifactID] = true
		}
	}
	check(toParent == 0, "chunking: no edge is linked to the parent artifact", fmt.Sprintf("chunking: %d edge links point at the parent artifact, not at a chunk", toParent))
	want := (minChunks + 1) / 2
	check(len(linkedChunks) >= want, fmt.Sprintf("chunking: edges come from %d chunks (at least %d)", len(linkedChunks), want),
		fmt.Sprintf("chunking: edges come from only %d chunks, want at least %d", len(linkedChunks), want))
	return nil
}

// checkWebSearches verifies what the web search of remember stored in the dataset. None of it depends on what the web
// says, only on how it was stored:
//   - at least min web_search artifacts;
//   - each saves the query that was issued, has text, and has as its parent an artifact of the dataset that is not itself a
//     web search: the artifact from which the searched entity came (a web search is never a chunk, and has no chunk index);
//   - each has entities of its own linked to it (the entities discovered on the web are stored like any other).
func checkWebSearches(ctx context.Context, database *db.DB, dataset string, min int, check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, EvalUser, dataset)
	if err != nil {
		return err
	}
	arts, err := graph.ListArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	links, err := graph.ListEntityArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	linked := map[int64]int{} // artifact ID -> entities linked to it
	for _, l := range links {
		linked[l.ArtifactID]++
	}
	byID := map[int64]graph.Artifact{}
	for _, a := range arts {
		byID[a.ID] = a
	}
	var webs []graph.Artifact
	for _, a := range arts {
		if a.Kind == graph.ArtifactWebSearch {
			webs = append(webs, a)
		}
	}
	check(len(webs) >= min, fmt.Sprintf("web search: %d web_search artifacts (at least %d)", len(webs), min),
		fmt.Sprintf("web search: %d web_search artifacts, want at least %d", len(webs), min))
	ok, problem := true, ""
	for _, w := range webs {
		parent, hasParent := byID[w.ParentID]
		switch {
		case strings.TrimSpace(w.Query) == "":
			ok, problem = false, fmt.Sprintf("artifact %d: no query saved", w.ID)
		case strings.TrimSpace(w.Text) == "":
			ok, problem = false, fmt.Sprintf("artifact %d: no text", w.ID)
		case !hasParent || parent.Kind == graph.ArtifactWebSearch:
			ok, problem = false, fmt.Sprintf("artifact %d: its parent %d is not an artifact the entity came from", w.ID, w.ParentID)
		case linked[w.ID] == 0:
			ok, problem = false, fmt.Sprintf("artifact %d (%q): no entity is linked to it", w.ID, w.Query)
		}
	}
	check(ok, "web search: every web_search artifact has its query, text, a parent artifact and entities", "web search: "+problem)
	return nil
}

// checkGeo verifies the places stored in the dataset (the rows of gb_entity_geo, found by the geo stage of remember and its resolver)
// against the expected ones: each named entity must be stored as a place, of the kind, in the country and near the coordinates
// that are given. The detection is the LLM's and the coordinates are the resolver's, so this is not independent of either.
func checkGeo(ctx context.Context, database *db.DB, dataset string, want []core.KnowledgeExpectGeo, out io.Writer, check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, EvalUser, dataset)
	if err != nil {
		return err
	}
	stored, err := graph.ListEntityGeo(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	if out != nil {
		fmt.Fprintln(out, "  stored places:")
		for _, g := range stored {
			fmt.Fprintf(out, "    [%d] %s kind=%s (%.4f, %.4f) country=%s %q\n", g.EntityID, g.EntityName, g.Kind, g.Latitude, g.Longitude, g.CountryCode, g.DisplayName)
		}
	}
	for _, w := range want {
		desc := "place " + w.Entity
		var found *graph.EntityGeo
		for i, g := range stored {
			if strings.EqualFold(g.EntityName, w.Entity) {
				found = &stored[i]
				break
			}
		}
		if found == nil {
			check(false, "", desc+": not stored as a place")
			continue
		}
		check(true, desc+": stored as a place", "")
		if w.Kind != "" {
			check(found.Kind == w.Kind, fmt.Sprintf("%s: kind = %s", desc, w.Kind), fmt.Sprintf("%s: kind = %q, want %s", desc, found.Kind, w.Kind))
		}
		if w.CountryCode != "" {
			check(strings.EqualFold(found.CountryCode, w.CountryCode), fmt.Sprintf("%s: country = %s", desc, w.CountryCode),
				fmt.Sprintf("%s: country = %q, want %s", desc, found.CountryCode, w.CountryCode))
		}
		if w.Latitude != nil && w.Longitude != nil {
			radius := w.RadiusKm
			if radius <= 0 {
				radius = core.DefaultGeoRadiusKm
			}
			km := distanceKm(found.Latitude, found.Longitude, *w.Latitude, *w.Longitude)
			check(km <= radius, fmt.Sprintf("%s: %.0f km from (%.2f, %.2f) (within %.0f)", desc, km, *w.Latitude, *w.Longitude, radius),
				fmt.Sprintf("%s: at (%.4f, %.4f), %.0f km from (%.2f, %.2f), want within %.0f km", desc, found.Latitude, found.Longitude, km, *w.Latitude, *w.Longitude, radius))
		}
	}
	return nil
}

// distanceKm is the great-circle distance between two points in degrees (haversine, mean Earth radius).
func distanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthKm = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat, dLon := rad(lat2-lat1), rad(lon2-lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthKm * math.Asin(math.Sqrt(a))
}

// checkSeries verifies the timeseries stored in the dataset against the expected ones: each must exist, have the
// expected number of points and first/last values, and the expected anchors.
func checkSeries(ctx context.Context, database *db.DB, dataset string, want []core.KnowledgeExpectSeries, out io.Writer, check func(ok bool, pass, fail string)) error {
	dsID, err := relational.DatasetID(ctx, database.Relational, EvalUser, dataset)
	if err != nil {
		return err
	}
	stored, err := graph.ListSeries(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	entities, err := graph.ListEntities(ctx, database.Graph, dsID)
	if err != nil {
		return err
	}
	entityName := map[int64]string{}
	for _, e := range entities {
		entityName[e.ID] = e.Name
	}
	if out != nil {
		fmt.Fprintln(out, "  stored series:")
		for _, s := range stored {
			fmt.Fprintf(out, "    [%d] %s/%s points=%d first=%v last=%v\n", s.ID, s.Key, s.Field, s.PointCount, s.FirstTS, s.LastTS)
		}
	}
	for _, w := range want {
		field := w.Field
		if field == "" {
			field = memory_core.DefaultSeriesField
		}
		desc := "series " + w.Key + "/" + field
		var found *graph.Series
		for i, s := range stored {
			if s.Key == w.Key && s.Field == field {
				found = &stored[i]
				break
			}
		}
		if found == nil {
			check(false, "", desc+": not stored")
			continue
		}
		check(true, desc+": stored", "")
		check(found.PointCount == w.Points, fmt.Sprintf("%s: %d points", desc, w.Points),
			fmt.Sprintf("%s: %d points, want %d", desc, found.PointCount, w.Points))
		pts, err := graph.SeriesPoints(ctx, database.Graph, found.ID, nil, nil, 0)
		if err != nil {
			return err
		}
		check(len(pts) == found.PointCount, desc+": point rows match point_count",
			fmt.Sprintf("%s: %d point rows, point_count %d", desc, len(pts), found.PointCount))
		if len(pts) > 0 {
			if w.First != nil {
				check(pts[0].Value == *w.First, fmt.Sprintf("%s: first value = %v", desc, *w.First),
					fmt.Sprintf("%s: first value = %v, want %v", desc, pts[0].Value, *w.First))
			}
			if w.Last != nil {
				check(pts[len(pts)-1].Value == *w.Last, fmt.Sprintf("%s: last value = %v", desc, *w.Last),
					fmt.Sprintf("%s: last value = %v, want %v", desc, pts[len(pts)-1].Value, *w.Last))
			}
		}
		if w.AnchorEntity == "" && !w.AnchorArtifact {
			continue
		}
		anchors, err := graph.ListSeriesAnchors(ctx, database.Graph, found.ID)
		if err != nil {
			return err
		}
		var onEntity, onArtifact bool
		for _, a := range anchors {
			switch a.Type {
			case graph.AnchorEntity:
				onEntity = onEntity || strings.EqualFold(entityName[a.TargetID], w.AnchorEntity)
			case graph.AnchorArtifact:
				onArtifact = true
			}
		}
		if w.AnchorEntity != "" {
			check(onEntity, fmt.Sprintf("%s: anchored to entity %s", desc, w.AnchorEntity),
				fmt.Sprintf("%s: not anchored to entity %s", desc, w.AnchorEntity))
		}
		if w.AnchorArtifact {
			check(onArtifact, desc+": anchored to an artifact", desc+": not anchored to an artifact")
		}
	}
	return nil
}

// sourceArtifactIDs returns the IDs of the artifacts of the dataset that back the given edges (provenance): the
// artifact that created each edge and every later one that restated the fact (gb_edge_artifacts), ascending.
func sourceArtifactIDs(ctx context.Context, database *db.DB, dataset string, edgeArtifact map[int64]*int64) ([]int64, error) {
	dsID, err := relational.DatasetID(ctx, database.Relational, EvalUser, dataset)
	if err != nil {
		return nil, err
	}
	links, err := graph.ListEdgeArtifacts(ctx, database.Graph, dsID)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for edgeID, creator := range edgeArtifact {
		if creator != nil {
			want[*creator] = true
		}
		for _, l := range links {
			if l.EdgeID == edgeID {
				want[l.ArtifactID] = true
			}
		}
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}
