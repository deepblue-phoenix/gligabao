package memory_remember

import (
	"maps"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// runParams is what the run log stores as the params of a call (gb_remember_runs.params): the input of the trajectory
// (what was asked for) plus EVERY flag the pipeline supports, set or not (false, empty or null when not set), so that a run
// says what it was made with and, as important, what it was made without. A key that is absent means a run from before
// the flags were recorded (0.219.0), not "off".
// The user_id stays in the stored params (it is also a column of the run); the REST endpoints of the runs of a user
// (GET /v1/gb/remember/runs) remove it from what they return, see api/remember/runs.go.
// It is pure: the trajectory input is not changed (its keys and shapes are those of the evals), the flags are added to a copy.
func runParams(input map[string]any,
	co *memory_core.CallOptions,
	ground *groundCall,
	cc memory_chunk.Config,
	chunked bool,
	seriesCount int) map[string]any {
	params := maps.Clone(input)
	var ontologies any = []any{}
	mode := ""
	if ground != nil {
		mode = ground.mode
		if g, ok := input["ontology_grounding"].(map[string]any); ok {
			ontologies = g["ontologies"] // the ontologies the call resolved (id, name, version)
		}
	}
	graphClasses := co.GraphClasses
	if graphClasses == nil {
		graphClasses = []string{}
	}
	models := map[string]string{}
	maps.Copy(models, co.Models)
	reasoning := map[string]string{}
	maps.Copy(reasoning, co.Reasoning)
	maps.Copy(params,
		map[string]any{
			"web_search":              co.WebSearch,
			"classify_graph":          co.ClassifyGraph,
			"graph_classes":           graphClasses,
			"ontology_grounding":      ground != nil,
			"ontology_grounding_mode": mode,
			"ontologies":              ontologies,
			"geo":                     co.GeoResolver != nil || co.GeoDeferred,
			"geo_sync":                co.GeoSync,
			"graph_index":             co.GraphIndex,
			"timeseries":              seriesCount > 0,
			"timeseries_fetch":        co.SeriesFetch != nil,
			"timeseries_observations": co.SeriesObservations,
			"chunked":                 chunked,
			"chunk_size":              cc.Size,
			"chunk_overlap_percent":   cc.OverlapPercent,
			"models":                  models,
			"reasoning":               reasoning,
		})
	return params
}
