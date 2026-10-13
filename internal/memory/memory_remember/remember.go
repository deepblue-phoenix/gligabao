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
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
)

// CallRemember is the type of the LLM calls of Remember, stored in gb_llm_calls.type.
const CallRemember = "remember"

//---------------------------------------------------------------------------------------------

// Entity is a thing mentioned in the text (person, place, project, concept, ...).
type Entity = graph.Entity

// Edge is a fact relating two entities (referenced by name), with its bi-temporal validity.
// Description is the fact as a sentence, e.g. "Alice works at Acme."
type Edge = graph.Edge

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
}

// Extract asks the LLM to pull entities and temporal edges out of text. ref is the artifact's time; it anchors
// relative dates and is the ValidAt of every fact whose start the text does not state.
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
		return nil, errors.New("memory: LLM output truncated; text too large")
	}
	ex, err := parseExtraction(resp.Text, ref)
	if err != nil {
		return nil, err
	}
	ex.stampModel(resp.Provider, resp.Model)
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
		if e.InvalidAt != nil && !e.InvalidAt.After(*e.ValidAt) { // inconsistent stop time
			e.InvalidAt = nil
		}
		ex.Edges = append(ex.Edges, e)
	}
	return ex, nil
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

	// Chunks are the chunks of a text that was larger than one chunk, in order; empty for a text that was not
	// chunked. Then ArtifactID is the parent artifact (the whole text), the chunks are its children, and Entities,
	// Edges and InvalidatedEdges are those of all the chunks together.
	Chunks []ChunkResult `json:"chunks,omitempty"`

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
}

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
// reconciled, stored and embedded on its own (see rememberDocument and docs/chunking.md).
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
	tr.Input(input)

	j := &job{d: d, llm: llm, tr: tr, co: co, opts: opts, userID: userID, dataset: dataset, kind: kind, ref: ref, now: now}
	if chunked {
		return j.rememberDocument(ctx, text, cc)
	}

	//--------------------------------------
	// EXTRACT
	ex, err := Extract(ctx, llm, text, ref)
	if err != nil {
		return nil, err
	}

	tr.Tool("The LLM extracted these entities and edges (new facts) from the text, before any stored fact is considered.",
		"extract_graph", map[string]any{"text_chars": len(text)},
		map[string]any{"entities": ex.Entities, "edges": candidateFacts(ex.Edges, nil)})

	return j.resolveAndStore(ctx, text, ex, nil)
}

// job is what one Remember call shares between the steps it runs on each text it extracts (the text itself, or each of
// the chunks of a long one).
type job struct {
	d      *sql.DB
	llm    memory_core.LLM // already recording and tracing
	tr     *memory_core.Trace
	co     memory_core.CallOptions
	opts   []memory_core.Option
	userID string
	// dataset, kind: already resolved.
	dataset, kind string
	ref, now      time.Time
}

// resolveAndStore reconciles the new facts of ex (extracted from text) with the stored ones and stores the text as an
// artifact, with its entities, edges and (with vectors) embeddings. part says the text is a chunk of a longer one; nil
// for an artifact that is not a chunk.
func (j *job) resolveAndStore(ctx context.Context, text string, ex *Extraction, part *chunkPart) (*Result, error) {
	d, tr, co, userID, dataset := j.d, j.tr, j.co, j.userID, j.dataset
	label := part.label()

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
	cs, err := loadCandidates(ctx, d, co, userID, dataset, ex)
	if err != nil {
		return nil, err
	}
	cands := cs.Edges

	// Three steps in the trajectory: each lookup with what it found alone, then the merge.
	tr.Tool("Looked up the live stored facts touching an entity whose normalized name is an endpoint of a new fact "+
		"(newest first, at most 50): the candidates found by exact name."+label, "find_candidates_by_name",
		map[string]any{"user_id": userID, "dataset": dataset, "names": cs.Names},
		map[string]any{"candidates": len(cs.ByName), "facts": candidateFacts(cs.ByName, nil)})

	//--------------------------------------

	traceSearchEntities(tr, cs.SearchTexts, cs.Similar, candidateFacts(cs.BySimilar, nil), cs.SearchUsage, cs.SearchErr)
	tr.Tool("Merged the two lookups by edge ID (newest first, at most 50); the new facts are checked against these "+
		"candidates for duplicates and contradictions."+label, "load_candidates",
		map[string]any{"user_id": userID, "dataset": dataset, "new_edges": len(ex.Edges), "by_name": len(cs.ByName), "by_similarity": len(cs.BySimilar)},
		map[string]any{"candidates": len(cands), "facts": candidateFacts(cands, cs.Via)})
	plan, err := resolveEdges(ctx, j.llm, ex.Edges, cands)
	if err != nil {
		return nil, err
	}
	res, err := store(ctx, d, userID, dataset, j.kind, text, j.ref, j.now, ex, plan, part, append(j.opts[:len(j.opts):len(j.opts)], memory_core.WithTrace(tr))...)
	if err != nil {
		return nil, err
	}
	args := map[string]any{"user_id": userID, "dataset": dataset, "kind": j.kind}
	if part != nil {
		args["parent_id"], args["chunk_index"] = part.ParentID, part.Chunk.Index
	}
	tr.Tool("Stored the artifact, entities and edges."+label, "store_artifact", args,
		map[string]any{"artifact_id": res.ArtifactID, "entities": res.Entities, "edges": res.Edges, "invalidated_edges": res.InvalidatedEdges,
			"embedded_entities": res.EmbeddedEntities, "embedded_edges": res.EmbeddedEdges, "embedded_chunks": res.EmbeddedChunks})
	return res, nil
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
	return store(ctx, d, userID, dataset, kind, text, ref, now, ex, plan, nil, opts...)
}

// store is Store; part, if not nil, says the text is a chunk of a longer one: it is then stored as a child artifact
// of part.ParentID, and its text is embedded into the chunk index together with the entities and edges.
func store(ctx context.Context, d *sql.DB, userID, dataset, kind, text string, ref, now time.Time, ex *Extraction, plan *EdgePlan, part *chunkPart, opts ...memory_core.Option) (*Result, error) {
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
	if part == nil {
		artID, err = tx.InsertArtifact(ctx, dsID, kind, text, sessionID, ref, now)
	} else {
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

	var chunkVec *chunkVector
	if part != nil {
		chunkVec = &chunkVector{artifactID: artID, parentID: part.ParentID, index: part.Chunk.Index, text: text}
	}
	pv, embedUsage, err := prepareVectors(ctx, tx, co, dsID, embedIDs, out.Edges, chunkVec)
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
		out.EmbeddedEntities, out.EmbeddedEdges, out.EmbeddedChunks = pv.entities, pv.edges, pv.chunks
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
