package memory_remember

// The pipeline of Remember: the stages a call goes through, in order, as a value (rememberPipeline) that reads top to bottom,
// and the stages themselves, each a method of job that does its work and nothing else. Remember sets the call up and runs it
// with memory_pipeline.Run, which tells the run log (see runlog.go) of every stage: when it starts and ends, with what error,
// whether it was skipped, and its inferences. The stages share what they make through the job (job.exs, job.u, job.res, ...).
//
// A text of one chunk goes through the stages once; a document (a text of more than one chunk, see docs/chunking.md) goes
// through extract_graph and summarize_chunk for every chunk first, since nothing is written until every chunk is extracted,
// then its parent artifact is stored (the store stage), and then every chunk goes through load_candidates, resolve_edges and
// store, one chunk to the end before the next, so that a chunk is also compared with the facts of the chunks before it.
// The stages after those (store_series for a document, geo, web search, series fetch and observations) run once, for the text as
// a whole; none of them fails the call.

import (
	"context"
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_pipeline"
)

// The names of the stages are those of the run log (relational.RememberStages, in this order; TestPipelineStagesAreTheRunLogs
// checks it).
var rememberPipeline = []memory_pipeline.Step[job]{
	// Jev (system 1) judges how complex the text is. Advisory: for now the result is only logged.
	memory_pipeline.Stage[job]{Name: StageClassifyComplexity, Policy: memory_pipeline.Advisory, Enabled: (*job).hasJev, Run: (*job).classifyComplexity},

	// A document is split into chunks...
	memory_pipeline.Stage[job]{Name: StageChunkText, Enabled: (*job).isDocument, Run: (*job).chunkText},
	// ...and the graph is extracted from the text, or from every chunk, which is also summarized.
	memory_pipeline.Each[job]{Count: (*job).itemCount, Set: (*job).setItem, Steps: []memory_pipeline.Step[job]{
		memory_pipeline.Stage[job]{Name: memory_core.StageExtractGraph, Run: (*job).extract},
		memory_pipeline.Stage[job]{Name: memory_core.StageSummarizeChunk, Enabled: (*job).isDocument, Run: (*job).summarize},
	}},

	// The whole text of a document is stored as the parent artifact of its chunks.
	memory_pipeline.Stage[job]{Name: StageStore, Enabled: (*job).isDocument, Run: (*job).storeParent},
	// The new facts of the text, or of each chunk, are reconciled with the stored ones and stored.
	memory_pipeline.Each[job]{Count: (*job).itemCount, Set: (*job).setUnit, Steps: reconcileSteps},
	// The series of a document are stored once the chunks, and so their entities, are (those of a text of one chunk are stored
	// by its store stage, in the same transaction).
	memory_pipeline.Stage[job]{Name: StageStoreSeries, Enabled: (*job).storesDocumentSeries, Run: (*job).storeDocumentSeries},

	// The enrichment stages run once the text is stored, on its key entities: none of them fails the call.
	memory_pipeline.Stage[job]{Name: StageResolveGeo, Policy: memory_pipeline.Advisory, Enabled: (*job).hasGeo, Run: (*job).geoStage},
	memory_pipeline.Stage[job]{Name: memory_core.StageWebSearch, Policy: memory_pipeline.Advisory, Enabled: (*job).hasWebSearch, Run: (*job).webSearchStage},
	memory_pipeline.Stage[job]{Name: StageFetchSeries, Policy: memory_pipeline.Advisory, Enabled: (*job).hasSeriesFetch, Run: (*job).fetchSeriesStage},
	// An artifact for each series the call stored, supplied or fetched.
	memory_pipeline.Stage[job]{Name: StageObserveSeries, Policy: memory_pipeline.Advisory, Enabled: (*job).hasObservables, Run: (*job).observeSeriesStage},
}

// reconcileSteps are the stages that reconcile the facts extracted from a text with the stored ones and store the text (job.u):
// those of the text itself or of a chunk (the Each of rememberPipeline), and of the texts the enrichment stages make (see
// job.reconcile).
var reconcileSteps = []memory_pipeline.Step[job]{
	memory_pipeline.Stage[job]{Name: StageLoadCandidates, Run: (*job).loadCandidatesStage},
	memory_pipeline.Stage[job]{Name: memory_core.StageResolveEdges, Run: (*job).resolveEdgesStage},
	memory_pipeline.Stage[job]{Name: StageStore, Also: (*job).alsoStoresSeries, Run: (*job).storeStage},
}

// unit is a text that goes through the reconcile stages, with what they make of it: the text of the call, a chunk of it, or a
// text of an enrichment stage.
type unit struct {
	text string
	ex   *Extraction
	// part says the text is a chunk of a longer one; nil if it is not.
	part *chunkPart

	cs   *candidates // load_candidates
	plan *EdgePlan   // resolve_edges
	res  *Result     // store
}

// reconcile runs the reconcile stages on a text that an enrichment stage made (a web search result, a series observation), from
// which ex was extracted, and returns what was stored. The job is the one of the enrichment stage, with its own run log view
// (see runLog.Within).
func (j *job) reconcile(ctx context.Context, text string, ex *Extraction) (*Result, error) {
	j.u = &unit{text: text, ex: ex}
	if err := memory_pipeline.Run(ctx, reconcileSteps, j, j.run); err != nil {
		return nil, err
	}
	return j.u.res, nil
}

//---------------------------------------------------------------------------------------------
// The items of the call: the text, or each chunk of it

func (j *job) isDocument() bool { return j.chunked }

// itemCount is how many texts go through the stages that run for each: one, or the chunks of a document.
func (j *job) itemCount() int {
	if j.chunked {
		return len(j.chunks)
	}
	return 1
}

// setItem makes item i the current one.
func (j *job) setItem(i int) { j.i = i }

// setUnit makes item i the current one, and makes the unit of it that the reconcile stages work on.
func (j *job) setUnit(i int) {
	j.i = i
	if !j.chunked {
		j.u = &unit{text: j.text, ex: j.exs[0]}
		return
	}
	c := j.chunks[i]
	j.u = &unit{text: c.Text, ex: j.exs[i], part: &chunkPart{ParentID: j.parentID, Chunk: c, Count: len(j.chunks), Summary: j.sums[i]}}
}

// chunkError says which chunk a failure of the reconcile stages was of. Those stages cannot be one transaction (the candidates of
// a chunk are read from the database), so the chunks before it are stored, each complete, and the parent stays.
func (j *job) chunkError(err error) error {
	if err == nil || j.u.part == nil {
		return err
	}
	return fmt.Errorf("memory: chunk %d of %d (parent artifact %d; the %d chunks before it are stored): %w",
		j.i+1, j.u.part.Count, j.u.part.ParentID, j.i, err)
}

//---------------------------------------------------------------------------------------------
// chunk_text, extract_graph, summarize_chunk

// chunkText is the chunk_text stage: the split of a document into chunks.
func (j *job) chunkText(_ context.Context) error {
	chunks, err := memory_chunk.Split(j.text, j.cc)
	if err != nil {
		return fmt.Errorf("memory: chunk text: %w", err)
	}
	j.chunks = chunks
	summary := make([]map[string]any, 0, len(chunks))
	for _, c := range chunks {
		summary = append(summary, map[string]any{"index": c.Index, "tokens": c.Tokens, "overlap_tokens": c.OverlapTokens, "start": c.Start, "end": c.End})
	}
	j.tr.Tool("The text is larger than one chunk, so it was split into overlapping chunks at paragraph boundaries; each chunk is "+
		"extracted, stored and embedded on its own.", "chunk_text",
		map[string]any{"text_chars": len(j.text), "chunk_size": j.cc.Size, "overlap_percent": j.cc.OverlapPercent},
		map[string]any{"chunks": len(chunks), "details": summary})
	j.run.Output(StageChunkText, "chunk_text", "", map[string]any{"chunks": len(chunks), "details": summary})
	return nil
}

// extract is the extract_graph stage, for the current item: the LLM extracts the entities and edges (the new facts) from the
// text, or from the chunk, before any stored fact is considered.
func (j *job) extract(ctx context.Context) error {
	if j.exs == nil {
		j.exs = make([]*Extraction, j.itemCount())
	}
	if !j.chunked {
		ex, err := Extract(ctx, j.co.ModelLLM(j.llm, memory_core.StageExtractGraph), j.text, j.ref)
		j.outputExtraction("", ex)
		if err != nil {
			return err
		}
		j.exs[0] = ex
		j.tr.Tool("The LLM extracted these entities and edges (new facts) from the text, before any stored fact is considered.",
			"extract_graph", map[string]any{"text_chars": len(j.text)},
			map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})
		return nil
	}
	c := j.chunks[j.i]
	part := &chunkPart{Chunk: c, Count: len(j.chunks)}
	ex, err := Extract(ctx, j.co.ModelLLM(j.llm, memory_core.StageExtractGraph), c.Text, j.ref)
	j.outputExtraction(j.stepLabel(part), ex)
	if err != nil {
		return fmt.Errorf("memory: extract chunk %d of %d: %w", j.i+1, len(j.chunks), err)
	}
	j.exs[j.i] = ex
	j.tr.Tool("The LLM extracted these entities and edges (new facts) from the chunk, before any stored fact is considered."+part.label(),
		"extract_graph", map[string]any{"chunk_index": j.i, "chunk_tokens": c.Tokens, "text_chars": len(c.Text)},
		map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})
	return nil
}

// summarize is the summarize_chunk stage, for the current chunk: the LLM summarizes it.
func (j *job) summarize(ctx context.Context) error {
	if j.sums == nil {
		j.sums = make([]*chunkSummary, len(j.chunks))
	}
	c := j.chunks[j.i]
	part := &chunkPart{Chunk: c, Count: len(j.chunks)}
	sum, err := summarizeChunk(ctx, j.co.ModelLLM(j.llm, memory_core.StageSummarizeChunk), c.Text)
	if sum != nil {
		j.run.Output(memory_core.StageSummarizeChunk, "summarize_chunk", j.stepLabel(part),
			map[string]any{"summary": sum.Text, "model": sum.Model, "reasoning": sum.Reasoning, "inference_id": sum.InferenceID, "raw": sum.Raw})
	}
	if err != nil {
		return fmt.Errorf("memory: summarize chunk %d of %d: %w", j.i+1, len(j.chunks), err)
	}
	j.sums[j.i] = sum
	j.tr.Tool("The LLM summarized the chunk; the summary is stored as an artifact of its own, a child of the chunk, and embedded."+part.label(),
		"summarize_chunk", map[string]any{"chunk_index": j.i, "chunk_tokens": c.Tokens, "text_chars": len(c.Text), "reasoning": sum.Reasoning},
		map[string]any{"summary": sum.Text, "model": sum.Model, "reasoning": sum.Reasoning, "inference_id": sum.InferenceID})
	return nil
}

//---------------------------------------------------------------------------------------------
// store (the parent of a document), load_candidates, resolve_edges, store

// storeParent is the store stage of a document, before its chunks: the text as a whole is stored as the parent artifact. Nothing
// is written before it, so a failure of the extraction of a chunk stores nothing. It makes the Result of the document, which
// the chunks are merged into.
func (j *job) storeParent(ctx context.Context) error {
	sessionID, err := memory_core.ResolveSessionID(j.co.SessionID)
	if err != nil {
		return err
	}
	parentID, err := storeParent(ctx, j.d, j.userID, j.dataset, j.kind, j.text, sessionID, j.ref, j.now)
	if err != nil {
		return err
	}
	j.run.Artifact(parentID)
	j.parentID = parentID
	j.tr.Tool("Stored the whole text as the parent artifact of the chunks.", "store_parent_artifact",
		map[string]any{"user_id": j.userID, "dataset": j.dataset, "kind": j.kind}, map[string]any{"artifact_id": parentID, "chunks": len(j.chunks)})
	j.run.Output(StageStore, "store_parent_artifact", "", map[string]any{"artifact_id": parentID, "chunks": len(j.chunks)})

	j.res = &Result{
		UserID: j.userID, Dataset: j.dataset, Kind: j.kind, ArtifactID: parentID, SessionID: sessionID,
		ValidAt: j.ref.UTC(), CreatedAt: j.now.UTC(),
		Entities: []Entity{}, Edges: []Edge{}, InvalidatedEdges: []int64{}, Chunks: make([]ChunkResult, 0, len(j.chunks)),
	}
	j.merge = newMerger(j.res)
	return nil
}

// labels are the label of the steps of the trajectory (the messages) and that of the outputs in the run log, for the current text.
func (j *job) labels() (trace, step string) {
	part := j.u.part
	return part.label() + j.web.label() + j.obs.label(), j.stepLabel(part)
}

// loadCandidatesStage is the load_candidates stage, for the current text: it looks up the stored facts that the new ones might
// repeat or contradict.
func (j *job) loadCandidatesStage(ctx context.Context) error {
	u := j.u
	label, step := j.labels()

	//--------------------------------------
	// LOAD_CANDIDATES

	// Candidates are facts (edges) ALREADY STORED in this user's dataset that the new facts in ex.Edges might repeat or
	// contradict. A candidate is any stored edge that
	//   - belongs to the same user and dataset (nothing is shared across datasets),
	//   - is still live (expired_at IS NULL: not already superseded by a newer fact),
	//   - touches at least one entity (as source or target) that is an endpoint of a new edge, found in two ways that
	//     are merged by edge ID: by exact normalized name, and (with WithVectors) by similarity of the entity's
	//     embedding, which finds aliases ("Alicia" for a stored "Alice Smith"); see findSimilarEntities,
	// newest first, capped at maxCandidates (50). E.g. the new fact "Alice lives in Berlin" has Alice and Berlin as
	// endpoints, so every live fact about Alice or Berlin is a candidate, such as "Alice lives in Paris".
	// A dataset that does not exist yet (first Remember) has none.
	//
	// They are the only stored facts resolveEdges looks at: exact repeats (same endpoints and relation) are merged in
	// Go, and the rest are shown to the LLM, which flags semantic duplicates and contradictions; the contradicted
	// ones are invalidated (not deleted) by Store. Without candidates there is no LLM resolution call.
	// For the chunks of a long text this runs after the previous chunk was stored, so a chunk is also compared with
	// the facts of the chunks before it.
	cs, err := loadCandidates(ctx, j.d, j.co, j.userID, j.dataset, u.ex)
	if err != nil {
		return j.chunkError(err)
	}
	u.cs = cs
	cands := cs.Edges

	// Three steps in the trajectory: each lookup with what it found alone, then the merge.
	j.tr.Tool("Looked up the live stored facts touching an entity whose normalized name is an endpoint of a new fact "+
		"(newest first, at most 50): the candidates found by exact name."+label, "find_candidates_by_name",
		map[string]any{"user_id": j.userID, "dataset": j.dataset, "names": cs.Names},
		map[string]any{"candidates": len(cs.ByName), "facts": candidateFacts(cs.ByName, nil)})

	//--------------------------------------

	traceSearchEntities(j.tr, cs.SearchTexts, cs.Similar, candidateFacts(cs.BySimilar, nil), cs.SearchUsage, cs.SearchErr)
	j.tr.Tool("Merged the two lookups by edge ID (newest first, at most 50); the new facts are checked against these "+
		"candidates for duplicates and contradictions."+label, "load_candidates",
		map[string]any{"user_id": j.userID, "dataset": j.dataset, "new_edges": len(u.ex.Edges), "by_name": len(cs.ByName), "by_similarity": len(cs.BySimilar)},
		map[string]any{"candidates": len(cands), "facts": candidateFacts(cands, cs.Via)})
	j.outputCandidates(step, u.ex, cs)
	return nil
}

// resolveEdgesStage is the resolve_edges stage, for the current text: the new facts are compared with the candidates (an LLM call
// if there are any that are not exact repeats), which says which are duplicates and which contradict stored facts.
func (j *job) resolveEdgesStage(ctx context.Context) error {
	u := j.u
	_, step := j.labels()
	plan, err := resolveEdges(ctx, j.co.ModelLLM(j.llm, memory_core.StageResolveEdges), u.ex.Edges, u.cs.Edges)
	j.outputResolveEdges(step, u.ex, plan)
	if err != nil {
		return j.chunkError(err)
	}
	u.plan = plan
	return nil
}

// alsoStoresSeries says that the store stage of the text also stores the series of the call: it does when the text is a single
// chunk (not a chunk of a document, and not the text of an enrichment stage), in the same transaction (see storeStage).
func (j *job) alsoStoresSeries() []string {
	if j.u.part == nil && j.web == nil && j.obs == nil && len(j.co.Series) > 0 {
		return []string{StageStoreSeries}
	}
	return nil
}

// storeStage is the store stage, for the current text: the text is stored as an artifact, with its entities and edges, the
// contradicted facts are invalidated, and (with vectors) everything is embedded.
func (j *job) storeStage(ctx context.Context) error {
	u := j.u
	label, step := j.labels()
	res, err := store(ctx, j.d, j.userID, j.dataset, j.kind, u.text, j.ref, j.now, u.ex, u.plan, u.part, j.web, j.obs,
		append(j.opts[:len(j.opts):len(j.opts)], memory_core.WithTrace(j.tr))...)
	if err != nil {
		return j.chunkError(err)
	}
	u.res = res
	j.noteKeyEntities(u.ex, res.ArtifactID)
	args := map[string]any{"user_id": j.userID, "dataset": j.dataset, "kind": j.kind}
	if u.part != nil {
		args["parent_id"], args["chunk_index"] = u.part.ParentID, u.part.Chunk.Index
	}
	if j.web != nil {
		args["parent_id"], args["query"] = j.web.ParentID, j.web.Query
	}
	if j.obs != nil {
		args["parent_id"], args["query"], args["series_id"] = j.obs.ParentID, j.obs.Query, j.obs.SeriesID
	}
	stored := map[string]any{"artifact_id": res.ArtifactID, "entities": res.Entities, "edges": res.Edges, "invalidated_edges": res.InvalidatedEdges,
		"embedded_entities": res.EmbeddedEntities, "embedded_edges": res.EmbeddedEdges, "embedded_chunks": res.EmbeddedChunks, "embedded_summaries": res.EmbeddedSummaries}
	j.tr.Tool("Stored the artifact, entities and edges."+label, "store_artifact", args, stored)
	j.run.Output(StageStore, "store", step, stored)
	j.traceSeries(res.ArtifactID, res.Series)

	switch {
	case u.part != nil: // a chunk of a document: merged into its Result
		j.merge.add(res, u.part.Chunk, len(u.ex.Entities), len(u.ex.Edges))
	case j.web == nil && j.obs == nil: // the text of the call
		j.run.Artifact(res.ArtifactID)
		j.res = res
	}
	return nil
}

//---------------------------------------------------------------------------------------------
// store_series, and the enrichment stages

// storesDocumentSeries says whether the store_series stage is used: for a document that has series (the series of a text of one
// chunk are stored by its store stage).
func (j *job) storesDocumentSeries() bool { return j.chunked && len(j.co.Series) > 0 }

// storeDocumentSeries is the store_series stage of a document: the series are anchored to the parent artifact.
func (j *job) storeDocumentSeries(ctx context.Context) (err error) {
	j.res.Series, err = j.storeSeries(ctx, j.parentID)
	if err != nil {
		return fmt.Errorf("memory: timeseries (the text, its %d chunks and their facts are stored): %w", len(j.chunks), err)
	}
	j.traceSeries(j.parentID, j.res.Series)
	return nil
}

func (j *job) hasJev() bool         { return j.co.Jev != nil }
func (j *job) hasGeo() bool         { return j.co.GeoResolver != nil }
func (j *job) hasWebSearch() bool   { return j.co.WebSearch }
func (j *job) hasSeriesFetch() bool { return j.co.SeriesFetch != nil }
func (j *job) hasObservables() bool { return j.co.SeriesObservations && len(j.observables) > 0 }

// geoStage is the resolve_geo stage: the places among the entities of the text get their coordinates. The stage failed if any
// result of it did.
func (j *job) geoStage(ctx context.Context) error {
	j.res.Geo = j.resolveGeo(ctx)
	return resultsError(j.res.Geo)
}

// webSearchStage is the web_search stage: the key entities of the text are searched for on the web, and the results stored.
func (j *job) webSearchStage(ctx context.Context) error {
	j.res.WebSearches = j.webSearch(ctx)
	return resultsError(j.res.WebSearches)
}

// fetchSeriesStage is the fetch_series stage: series of the key entities are fetched from the providers and stored.
func (j *job) fetchSeriesStage(ctx context.Context) error {
	j.res.SeriesFetches = j.fetchSeries(ctx)
	return resultsError(j.res.SeriesFetches)
}

// observeSeriesStage is the observe_series stage: an observation text is made of each series the call stored, and stored.
func (j *job) observeSeriesStage(ctx context.Context) error {
	j.res.SeriesObservations = j.observeSeries(ctx)
	return resultsError(j.res.SeriesObservations)
}
