// Package memory_remember is the remember pipeline of internal/memory: it turns free text into a bi-temporal
// knowledge graph (modelled on Graphiti) with an LLM and stores it (see internal/db/graph). See temporal.go for how
// facts are reconciled with what is already stored.
package memory_remember

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai"
	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_chunk"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_pipeline"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_timeseries"
)

// Extraction is the structured result of processing a block of text.
type Extraction struct {
	Entities []Entity `json:"entities"`
	Edges    []Edge   `json:"edges"`
	// Model and Provider name the model that made the extraction (as the LLM response reports them), and ModelID is its
	// ID in gb_models (relational.ModelID(Model)); every entity and edge carries ModelID. Store registers the model
	// if it is not stored yet.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"model_id,omitempty"`
	// InferenceID is the inference (gb_inferences) of the LLM call that made the extraction; 0 if it could not be recorded.
	InferenceID int64 `json:"-"`
	// Raw is the text the LLM answered, kept for the stage outputs (see runLog.Output); "" if it did not answer.
	Raw string `json:"-"`
}

// Result summarizes what was stored by Remember.
type Result struct {
	UserID     string `json:"user_id"`
	Dataset    string `json:"dataset"`
	Kind       string `json:"kind"` // artifact kind: "knowledge" or "episode"
	ArtifactID int64  `json:"artifact_id"`
	// SessionID is the session the artifact was attached to (WithSessionID); omitted if there was none.
	SessionID string    `json:"session_id,omitempty"`
	ValidAt   time.Time `json:"valid_at"`   // event time of the artifact
	CreatedAt time.Time `json:"created_at"` // ingestion time of the artifact

	//--------------------------------------
	Entities         []Entity `json:"entities"`
	Edges            []Edge   `json:"edges"`             // the facts from this text in their stored state
	InvalidatedEdges []int64  `json:"invalidated_edges"` // IDs of edges closed because of this artifact

	//--------------------------------------

	// EmbeddedEntities and EmbeddedEdges are the number of entity and edge embeddings written to the dataset's
	// entity and edge indexes (0 unless the call was made with WithVectors).
	EmbeddedEntities int `json:"embedded_entities,omitempty"`
	EmbeddedEdges    int `json:"embedded_edges,omitempty"`
	// EmbeddedChunks is the number of chunk embeddings written to the dataset's chunk index (only for a chunked text).
	EmbeddedChunks int `json:"embedded_chunks,omitempty"`
	// EmbeddedSummaries is the number of chunk summary embeddings written to the dataset's summary index (only for a
	// chunked text).
	EmbeddedSummaries int `json:"embedded_summaries,omitempty"`

	// Chunks are the chunks of a text that was larger than one chunk, in order; empty for a text that was not
	// chunked. Then ArtifactID is the parent artifact (the whole text), the chunks are its children, and Entities,
	// Edges and InvalidatedEdges are those of all the chunks together.
	Chunks []ChunkResult `json:"chunks,omitempty"`

	// WebSearches are the web searches made for the key entities of the text (only with WithWebSearch): one per
	// entity searched for, in the order of the search, each with the web_search artifact it stored. Their entities and
	// edges are not in Entities and Edges above, which are those of the text itself. See docs/web-search.md.
	WebSearches []WebSearchResult `json:"web_searches,omitempty"`

	// Series are the timeseries the caller supplied (WithSeries), as stored and anchored; omitted when there were none.
	// For a chunked text they are anchored to the parent artifact. See docs/timeseries.md.
	Series []memory_timeseries.Result `json:"series,omitempty"`

	// SeriesFetches are the series fetched from the timeseries providers (only with WithSeriesFetch): one per
	// (entity, provider) the LLM chose, in order, each with the series it stored. See remember_seriesfetch.go.
	SeriesFetches []SeriesFetchResult `json:"series_fetches,omitempty"`

	// SeriesObservations are the series_observation artifacts derived from the series the call stored (only with
	// WithSeriesObservations): one per series, in order, each with the artifact it stored. See remember_observations.go.
	SeriesObservations []SeriesObservationResult `json:"series_observations,omitempty"`

	// Geo are the entities of the text that the LLM judged to be places, with what the geo resolver found for each (only
	// when the call has a geo resolver, WithGeoResolver): one per entity, in the order of extraction. The coordinates are
	// stored in gb_entity_geo. See remember_geo.go.
	Geo []GeoResult `json:"geo,omitempty"`

	// summary is the summary stored with a chunk (see ChunkResult.Summary); nil for anything else.
	summary *SummaryResult

	// RememberRunID is the run log of this call: its row in gb_remember_runs, which holds how each stage of the pipeline went
	// (see runlog.go).
	RememberRunID int64 `json:"remember_run_id,omitempty"`

	// TRAJECTORY
	// TrajectoryID and Trajectory are the ATIF trajectory of this call (also stored in TrajectoryDir); only set when
	// the call was made with WithEval.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

// ChunkResult is one chunk of a chunked text, an artifact of its own (a child of Result.ArtifactID).
type ChunkResult struct {
	ArtifactID int64 `json:"artifact_id"`
	Index      int   `json:"index"`
	Tokens     int   `json:"tokens"`
	// OverlapTokens is how much of the start of the chunk repeats the end of the previous one.
	OverlapTokens int `json:"overlap_tokens"`
	// StartOffset and EndOffset are the byte range of the chunk in the text.
	StartOffset int `json:"start_offset"`
	EndOffset   int `json:"end_offset"`
	// Entities and Edges are how many the chunk mentions and states (before merging with the other chunks).
	Entities int `json:"entities"`
	Edges    int `json:"edges"`
	// Summary is the summary of the chunk: an artifact of its own, a child of the chunk, with the inference (model and
	// reasoning level) that wrote it. Always set for a chunk of a text remembered by Remember.
	Summary *SummaryResult `json:"summary,omitempty"`
}

// job is what one Remember call shares between the steps it runs on each text it extracts (the text itself, or each of
// the chunks of a long one).
type job struct {
	d   *sql.DB
	llm memory_core.LLM // already recording and tracing
	tr  *memory_core.Trace
	// run is the run log of the call (see runlog.go). It is nil in the jobs that store the texts the enrichment stages make (a
	// web search result, a series observation): they are part of those stages, not of the stages of the text itself.
	run    *runLog
	co     memory_core.CallOptions
	opts   []memory_core.Option
	userID string
	// dataset, kind: already resolved.
	dataset, kind string
	ref, now      time.Time

	// web is set on the job that stores the result of a web search (see storeWebSearch); nil for a text the caller sent.
	web *webPart
	// obs is set on the job that stores the observation of a series (see observe); nil for a text the caller sent.
	obs *observationPart
	// observables are the series the call stored that an observation may be made of (see noteObservable).
	observables []observable
	// keyEntities are the entities of the texts stored so far that the web search may pick from (see noteKeyEntities),
	// by normalized name, and keyOrder their names in the order they were extracted.
	keyEntities map[string]*keyEntity
	keyOrder    []string

	// What the stages of the pipeline share (see remember_pipeline.go). text is the text of the call, and chunked says that it is
	// a document; the stages after chunk_text read its chunks (chunks), and keep what each makes of them (exs, sums), indexed
	// like them (a text of one chunk has one item). i is the current item, u the text that the reconcile stages are on.
	text     string
	cc       memory_chunk.Config
	chunked  bool
	chunks   []memory_chunk.Chunk
	exs      []*Extraction
	sums     []*chunkSummary
	i        int
	parentID int64 // the parent artifact of a document
	u        *unit
	merge    *merger // accumulates the Results of the chunks into res
	res      *Result // what Remember returns: the result of the text, or of the document
}

// CallRemember is the type of the LLM calls of Remember, stored in gb_inferences.type.
const CallRemember = "remember"

//---------------------------------------------------------------------------------------------

// Entity is a thing mentioned in the text (person, place, project, concept, ...).
type Entity = graph.Entity

// Edge is a fact relating two entities (referenced by name), with its bi-temporal validity.
// Description is the fact as a sentence, e.g. "Alice works at Acme."
type Edge = graph.Edge

// RunID is the inference of the extraction (0 for none, and for a nil ex).
func (ex *Extraction) RunID() int64 {
	if ex == nil {
		return 0
	}
	return ex.InferenceID
}

// Extract asks the LLM to pull entities and temporal edges out of text. ref is the artifact's time; it anchors
// relative dates and is the ValidAt of every fact whose start the text does not state.
//
// If the LLM answered but its answer is no use (too long, malformed), the error comes with an Extraction that is empty but
// for its InferenceID, so that the run it cost is not lost; it is nil if the call itself failed.
func Extract(ctx context.Context, llm memory_core.LLM, text string, ref time.Time) (*Extraction, error) {

	//-----------------------
	// LLM
	resp, err := llm.Complete(ctx, ai.Request{
		System: entitySystemPrompt,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "<reference_time>\n" + relational.FormatTime(ref) +
			"\n</reference_time>\n\n<text>\n" + text + "\n</text>"}},
		MaxTokens: 4096,
	})

	//-----------------------

	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return &Extraction{InferenceID: resp.InferenceID, Raw: resp.Text}, errors.New("memory: LLM output truncated; text too large")
	}
	ex, err := parseExtraction(resp.Text, ref)
	if err != nil {
		return &Extraction{InferenceID: resp.InferenceID, Raw: resp.Text}, err
	}
	ex.stampModel(resp.Provider, resp.Model)
	ex.InferenceID, ex.Raw = resp.InferenceID, resp.Text
	return ex, nil
}

// stampModel records the model that made the extraction on ex and on each of its entities and edges. The LLM's own
// output never decides it.
func (ex *Extraction) stampModel(provider, name string) {
	modelID := relational.ModelID(name)
	ex.Provider, ex.Model, ex.ModelID = provider, name, modelID
	for i := range ex.Entities {
		ex.Entities[i].ModelID = modelID
	}
	for i := range ex.Edges {
		ex.Edges[i].ModelID = modelID
	}
}

// parseExtraction decodes the LLM's JSON, tolerating surrounding prose or code fences, and normalizes the edges.
func parseExtraction(s string, ref time.Time) (*Extraction, error) {
	js, err := memory_core.JSONObject(s)
	if err != nil {
		return nil, err
	}

	// ENTITY
	var raw struct {
		Entities []Entity `json:"entities"`
		Edges    []struct {
			Source      string `json:"source"`
			Target      string `json:"target"`
			Relation    string `json:"relation"`
			Description string `json:"description"`
			ValidAt     string `json:"valid_at"`
			InvalidAt   string `json:"invalid_at"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	ex := &Extraction{Entities: raw.Entities}
	for _, r := range raw.Edges {

		// EDGE
		e := Edge{
			Source:      strings.TrimSpace(r.Source),
			Target:      strings.TrimSpace(r.Target),
			Relation:    normRelation(r.Relation),
			Description: strings.TrimSpace(r.Description),
			ValidAt:     relational.ParseTime(r.ValidAt),
			InvalidAt:   relational.ParseTime(r.InvalidAt),
		}
		if e.Source == "" || e.Target == "" || e.Relation == "" {
			continue
		}
		if e.Description == "" {
			e.Description = memory_core.SynthStatement(e.Source, e.Relation, e.Target)
		}
		if e.ValidAt == nil { // a fact inherits the time of the artifact that states it
			v := ref.UTC()
			e.ValidAt = &v
		}
		fixSpan(&e)
		ex.Edges = append(ex.Edges, e)
	}
	return ex, nil
}

// fixSpan makes the event-time span of an extracted edge consistent. The text said the fact ended (InvalidAt), which is
// more reliable than a start the model defaulted or guessed, so the end is kept and the start is moved back to it when it
// is later ("Alice left Acme in June 2024" with no start: ValidAt would be the reference time, after the end). The span is
// then empty (ValidAt == InvalidAt): the start is unknown and the fact ended at that instant. ValidAt must be set.
func fixSpan(e *Edge) {
	if e.InvalidAt != nil && e.InvalidAt.Before(*e.ValidAt) {
		v := *e.InvalidAt
		e.ValidAt = &v
	}
}

//---------------------------------------------------------------------------------------------

// ErrInvalidKind means the artifact kind is neither graph.ArtifactKnowledge nor graph.ArtifactEpisode.
var ErrInvalidKind = errors.New("invalid kind")

// DefaultKind is the artifact kind used when the caller gives none: knowledge that is not strictly episodic.
const DefaultKind = graph.ArtifactKnowledge

// ResolveKind trims and lowercases kind and applies DefaultKind to an empty one. It returns ErrInvalidKind for
// anything other than "knowledge" or "episode".
func ResolveKind(kind string) (string, error) {
	switch k := strings.ToLower(strings.TrimSpace(kind)); k {
	case "":
		return DefaultKind, nil
	case graph.ArtifactKnowledge, graph.ArtifactEpisode:
		return k, nil
	}
	return "", fmt.Errorf("%w: kind must be %q or %q", ErrInvalidKind, graph.ArtifactKnowledge, graph.ArtifactEpisode)
}

//---------------------------------------------------------------------------------------------

// Remember extracts entities and temporal edges from text and stores them (with the raw text as an artifact of the
// given kind; empty means DefaultKind, i.e. knowledge) in one transaction, inside userID's named dataset (empty
// means DefaultDataset; it is created if missing). ref is the
// time the text was written/observed; the zero value means the current time. If any LLM step fails nothing is
// stored.
//
// With WithSessionID the artifact is attached to that session, and so is every entity and edge it yields (see Store).
//
// A text of more than the chunk size (WithChunking; 1024 tokens by default) is a document: it is stored as an
// artifact, split into overlapping chunks that are artifacts too (its children), and each chunk is extracted,
// reconciled, stored and embedded on its own, and summarized: the summary is a chunk_summary artifact, a child of the
// chunk, with its own embedding (see rememberPipeline and docs/chunking.md).
//
// With WithWebSearch(true) (and a searcher, WithWebSearcher; ErrWebSearchUnavailable without one) the key entities of the
// text are then searched for on the web, and the results are stored as artifacts of their own (kind web_search) and
// extracted like any text, after the text itself has been stored. See remember_websearch.go and docs/web-search.md.
//
// With WithSeries the timeseries are stored in the same transaction as the text and anchored to its artifact, and to the
// entities and edges their anchors select (resolved once the text is stored). For a chunked text they are stored
// after the chunks, in a transaction of their own. See remember_series.go and docs/timeseries.md.
//
// Every LLM call is recorded as an inference (gb_inferences: the model, the reasoning level it ran with, its tokens and
// cost, and the stage that made it). The LLM stages (extract_graph, summarize_chunk, resolve_edges, resolve_series) run with
// no thinking (reasoning level none) by default; WithReasoning sets a level per stage ("low", "medium" or "high"). Each chunk summary
// artifact references the run that wrote it.
//
// With WithModels each LLM stage (extract_graph, summarize_chunk, resolve_edges, web_search) runs on the model the caller chose; a stage with none uses the
// default model. The extract_graph stage's model is the one recorded on the entities and edges.
//
// With WithSeriesObservations a text is then generated from the stats of each series the call stored (supplied or
// fetched) and stored as an artifact of its own (kind series_observation), extracted like any text. See
// remember_observations.go and docs/timeseries.md.
//
// Only with WithEval is the call recorded as an ATIF trajectory (stored as JSON in TrajectoryDir and returned in
// Result.Trajectory, with its ID in Result.TrajectoryID).
func Remember(ctx context.Context, d *sql.DB, llm memory_core.LLM, userID, dataset, kind, text string, ref time.Time, opts ...memory_core.Option) (res *Result, err error) {
	userID, err = memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	kind, err = ResolveKind(kind)
	if err != nil {
		return nil, err
	}
	now := relational.Now()
	if ref.IsZero() {
		ref = now
	}
	ref = relational.Normalize(ref)
	llm = memory_core.WithRecording(llm, d, CallRemember)

	co := memory_core.NewCallOptions(opts)
	if co.WebSearch && co.WebSearcher == nil {
		return nil, memory_core.ErrWebSearchUnavailable
	}
	// Validated before any LLM call, so a malformed request costs nothing.
	if co.SeriesFetch != nil {
		if co.SeriesFetch, err = memory_core.ResolveSeriesFetch(co.SeriesFetch, co.SeriesProviders); err != nil {
			return nil, err
		}
	}
	series, err := memory_core.ResolveSeries(co.Series, true)
	if err != nil {
		return nil, err
	}
	opts = append(opts[:len(opts):len(opts)], memory_core.WithSeries(series))
	co.Series = series
	tr := memory_core.StartTrace(CallRemember, co)
	defer func() {
		if traj := tr.Finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	llm = tr.Wrap(llm)
	tr.User(text)
	input := map[string]any{"user_id": userID, "dataset": dataset, "kind": kind, "reference_time": relational.FormatTime(ref), "session_id": co.SessionID}
	cc := co.Chunking.WithDefaults()
	if err := cc.Validate(); err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	chunked := cc.NeedsChunking(text)
	if chunked {
		input["chunk_size"], input["chunk_overlap_percent"] = cc.Size, cc.OverlapPercent
	}
	if len(co.Models) > 0 {
		input["models"] = co.Models
	}
	if len(co.Reasoning) > 0 {
		input["reasoning"] = co.Reasoning
	}
	if co.WebSearch {
		input["web_search"] = true
	}
	if len(series) > 0 {
		input["timeseries"] = memory_core.SeriesSummary(series)
	}
	if co.SeriesObservations {
		input["timeseries_observations"] = true
	}
	if f := co.SeriesFetch; f != nil {
		input["timeseries_fetch"] = map[string]any{"providers": f.Providers, "before": f.Before.String(), "after": f.After.String()}
	}
	tr.Input(input)

	run := startRunLog(ctx, d, co.RememberRunID, userID, dataset, kind, input)
	defer func() {
		run.Finish(err)
		if res != nil {
			res.RememberRunID = run.ID()
		}
	}()

	j := &job{d: d, llm: llm, tr: tr, run: run, co: co, opts: opts, userID: userID, dataset: dataset, kind: kind, ref: ref, now: now,
		text: text, cc: cc, chunked: chunked}
	//------------------
	// MEMORY PIPELINE
	if err := memory_pipeline.Run(ctx, rememberPipeline, j, run); err != nil {
		return nil, err
	}
	//------------------
	return j.res, nil
}

// candidateFacts summarizes edges for the trajectory; via, if not nil, says how each (by ID) was found.
func candidateFacts(edges []Edge, via map[int64]string) []map[string]any {
	out := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		out = append(out, memory_core.FactFromEdge(e).Summary(via[e.ID]))
	}
	return out
}

// Store persists an artifact of the given kind (already resolved, see ResolveKind) and its extraction into userID's named dataset (created if missing). Entities are
// deduplicated within the dataset by (normalized name, type); nothing is shared across datasets. Edges are
// written according to plan (see EdgePlan); now is the system time stamped on them. Edges naming an unlisted
// entity create it with type "other".
//
// With WithSessionID the artifact carries the session ID, and the session is recorded against every entity the
// artifact mentions and every edge it states, in gb_entity_sessions and gb_edge_sessions. That includes the entities
// and edges that already existed (duplicates, for which nothing else is written), so the sessions an entity or fact
// was discovered in are all kept. An edge that this artifact merely contradicted is not linked.
//
// With WithVectors, every entity this artifact mentions through the extraction (created or already stored) and
// every edge it states (new, or the stored one it duplicates) is also embedded: entities from their stored name,
// type and description into the dataset's entity index, edges from their description into its edge index (see
// VectorDataset). The embedding is computed before the transaction commits, so an embedder failure stores
// nothing; the vectors are written right after the commit, so a vector store failure returns an error after the
// relational data is saved (the vectors are written again the next time the entity or fact is mentioned).
func Store(ctx context.Context, d *sql.DB, userID, dataset, kind, text string, ref, now time.Time, ex *Extraction, plan *EdgePlan, opts ...memory_core.Option) (*Result, error) {
	return store(ctx, d, userID, dataset, kind, text, ref, now, ex, plan, nil, nil, nil, opts...)
}

// store is Store. part, if not nil, says the text is a chunk of a longer one: it is then stored as a child artifact
// of part.ParentID, and its text is embedded into the chunk index together with the entities and edges. web, if not
// nil, says the text is the result of a web search: it is then stored as an artifact of kind web_search that saves the
// query and is a child of web.ParentID (kind is ignored). obs, if not nil, says the text is the observation of a series:
// it is then stored as an artifact of kind series_observation that records the series and is a child of obs.ParentID
// (kind is ignored). part, web and obs are never more than one set.
func store(ctx context.Context, d *sql.DB, userID, dataset, kind, text string, ref, now time.Time, ex *Extraction, plan *EdgePlan, part *chunkPart, web *webPart, obs *observationPart, opts ...memory_core.Option) (*Result, error) {
	co := memory_core.NewCallOptions(opts)
	userID, err := memory_core.ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = memory_core.ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	sessionID, err := memory_core.ResolveSessionID(co.SessionID)
	if err != nil {
		return nil, err
	}
	// The dataset belongs to the relational store and the graph to the graph store, so they are written separately.
	dsID, err := relational.EnsureDataset(ctx, d, userID, dataset)
	if err != nil {
		return nil, err
	}
	// The entities and edges reference the model that inferred them, so it must be stored first (and not inside the
	// graph transaction, which would hold the database's write lock). A model that gligabao supports is already
	// there; a dated snapshot of one, for instance, is added here.
	if ex.Model != "" {
		id, err := relational.EnsureModel(ctx, d, relational.Model{Name: ex.Model, Provider: ex.Provider, Kind: ai.KindOf(ex.Model)})
		if err != nil {
			return nil, err
		}
		if id != ex.ModelID {
			return nil, fmt.Errorf("memory: extraction model_id %q does not match model %q", ex.ModelID, ex.Model)
		}
	}
	// Registered before the transaction's Rollback, so it runs after the transaction is finished (see prepareVectors).
	var embedUsage *ai.EmbedUsage
	defer func() { recordEmbedUsage(ctx, d, embedUsage) }()
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var artID int64
	switch {
	case web != nil:
		artID, err = tx.InsertWebSearchArtifact(ctx, graph.NewWebSearchArtifact{DatasetID: dsID, ParentID: web.ParentID, Query: web.Query,
			Text: text, SessionID: sessionID, ValidAt: ref, CreatedAt: now})
		kind = graph.ArtifactWebSearch
	case obs != nil:
		artID, err = tx.InsertSeriesObservationArtifact(ctx, graph.NewSeriesObservationArtifact{DatasetID: dsID, ParentID: obs.ParentID,
			SeriesID: obs.SeriesID, Query: obs.Query, Text: text, SessionID: sessionID, ValidAt: ref, CreatedAt: now})
		kind = graph.ArtifactSeriesObservation
	case part == nil:
		artID, err = tx.InsertArtifact(ctx, dsID, kind, text, sessionID, ref, now)
	default:
		artID, err = tx.InsertChunkArtifact(ctx, graph.NewChunkArtifact{DatasetID: dsID, ParentID: part.ParentID, Kind: kind, Text: text,
			SessionID: sessionID, ChunkIndex: part.Chunk.Index, StartOffset: part.Chunk.Start, EndOffset: part.Chunk.End,
			Tokens: part.Chunk.Tokens, ValidAt: ref, CreatedAt: now})
	}
	if err != nil {
		return nil, err
	}

	out := &Result{
		UserID:           userID,
		Dataset:          dataset,
		Kind:             kind,
		ArtifactID:       artID,
		SessionID:        sessionID,
		ValidAt:          ref.UTC(),
		CreatedAt:        now.UTC(),
		Entities:         []Entity{},
		Edges:            []Edge{},
		InvalidatedEdges: []int64{},
	}
	ids := map[string]int64{} // normalized name -> entity id (first listed type wins for edge lookup)
	seenEnt := map[string]bool{}
	var embedIDs []int64 // entities to embed: those upserted by this call, once each
	seenID := map[int64]bool{}

	// Every entity this artifact touches is linked to it, whether it was created now or already existed: entities
	// are shared across artifacts (deduplicated per dataset), so the link, not the entity row, records provenance.
	// The same goes for the artifact's session.
	linkEntity := func(id int64) error {
		if err := tx.LinkEntity(ctx, id, artID); err != nil {
			return err
		}
		if sessionID == "" {
			return nil
		}
		return tx.LinkEntitySession(ctx, id, sessionID)
	}
	upsertEntity := func(e Entity) (int64, error) {
		id, err := tx.UpsertEntity(ctx, dsID, e, memory_core.Key(e.Name))
		if err != nil {
			return 0, err
		}
		if !seenID[id] {
			seenID[id] = true
			embedIDs = append(embedIDs, id)
		}
		return id, linkEntity(id)
	}

	for _, e := range ex.Entities {
		e.Name = strings.TrimSpace(e.Name)
		e.ModelID = ex.ModelID
		e.Type = normType(e.Type)
		e.Description = strings.TrimSpace(e.Description)
		if e.Name == "" {
			continue
		}
		id, err := upsertEntity(e)
		if err != nil {
			return nil, err
		}
		if _, ok := ids[memory_core.Key(e.Name)]; !ok {
			ids[memory_core.Key(e.Name)] = id
		}
		if k := memory_core.Key(e.Name) + "\x00" + e.Type; !seenEnt[k] {
			seenEnt[k] = true
			out.Entities = append(out.Entities, e)
		}
	}

	// Edge endpoints: an entity from this extraction, else an already-stored entity of any type, else a new "other".
	entityID := func(name string) (int64, error) {
		if id, ok := ids[memory_core.Key(name)]; ok {
			return id, nil
		}
		id, err := tx.FindEntityID(ctx, dsID, memory_core.Key(name))
		if errors.Is(err, graph.ErrNotFound) {
			if id, err = upsertEntity(Entity{Name: name, Type: "other", ModelID: ex.ModelID}); err == nil {
				out.Entities = append(out.Entities, Entity{Name: name, Type: "other", ModelID: ex.ModelID})
			}
		} else if err == nil {
			// An edge names an already stored entity that this extraction did not list: it is still mentioned here.
			err = linkEntity(id)
		}
		if err != nil {
			return 0, err
		}
		ids[memory_core.Key(name)] = id
		return id, nil
	}
	out.Edges, out.InvalidatedEdges, err = storeEdges(ctx, tx, dsID, artID, sessionID, ref, now, ex.Edges, plan, entityID)
	if err != nil {
		return nil, err
	}

	// The timeseries the caller supplied are stored in this transaction, so a series that cannot be anchored stores
	// nothing. Only for the artifact the caller sent: not for a chunk (the series go to the parent, see
	// storeDocumentSeries) and not for the result of a web search.
	if part == nil && web == nil && obs == nil && len(co.Series) > 0 {
		out.Series, err = memory_timeseries.Store(ctx, tx, dsID, artID, now, co.Series)
		if err != nil {
			return nil, err
		}
	}

	var chunkVec *chunkVector
	var sumVec *summaryVector
	if part != nil {
		chunkVec = &chunkVector{artifactID: artID, parentID: part.ParentID, index: part.Chunk.Index, text: text}
		if s := part.Summary; s != nil {
			sumID, err := tx.InsertChunkSummaryArtifact(ctx, graph.NewChunkSummaryArtifact{DatasetID: dsID, ParentID: artID, Text: s.Text,
				InferenceID: s.InferenceID, SessionID: sessionID, ValidAt: ref, CreatedAt: now})
			if err != nil {
				return nil, err
			}
			out.summary = &SummaryResult{ArtifactID: sumID, Text: s.Text, Model: s.Model, Reasoning: s.Reasoning, InferenceID: s.InferenceID}
			sumVec = &summaryVector{artifactID: sumID, chunkID: artID, parentID: part.ParentID, index: part.Chunk.Index, text: s.Text}
		}
	}
	pv, embedUsage, err := prepareVectors(ctx, tx, co, dsID, embedIDs, out.Edges, chunkVec, sumVec)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := pv.write(ctx); err != nil {
		return nil, err
	}
	if pv != nil {
		out.EmbeddedEntities, out.EmbeddedEdges, out.EmbeddedChunks, out.EmbeddedSummaries = pv.entities, pv.edges, pv.chunks, pv.summaries
	}
	return out, nil
}

//---------------------------------------------------------------------------------------------

//---------------------------------------------------------------------------------------------

func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" {
		return "other"
	}
	return t
}

func normRelation(r string) string {
	return strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(r), " ")), " ", "_")
}
