package memory

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
)

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
}

// Extract asks the LLM to pull entities and temporal edges out of text. ref is the artifact's time; it anchors
// relative dates and is the ValidAt of every fact whose start the text does not state.
func Extract(ctx context.Context, llm LLM, text string, ref time.Time) (*Extraction, error) {

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
	return parseExtraction(resp.Text, ref)
}

// parseExtraction decodes the LLM's JSON, tolerating surrounding prose or code fences, and normalizes the edges.
func parseExtraction(s string, ref time.Time) (*Extraction, error) {
	js, err := jsonObject(s)
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
			e.Description = e.Source + " " + strings.ReplaceAll(e.Relation, "_", " ") + " " + e.Target + "."
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
	UserID           string    `json:"user_id"`
	Dataset          string    `json:"dataset"`
	Kind             string    `json:"kind"` // artifact kind: "knowledge" or "episode"
	ArtifactID       int64     `json:"artifact_id"`
	ValidAt          time.Time `json:"valid_at"`   // event time of the artifact
	CreatedAt        time.Time `json:"created_at"` // ingestion time of the artifact
	Entities         []Entity  `json:"entities"`
	Edges            []Edge    `json:"edges"`             // the facts from this text in their stored state
	InvalidatedEdges []int64   `json:"invalidated_edges"` // IDs of edges closed because of this artifact
	// TrajectoryID and Trajectory are the ATIF trajectory of this call (also stored in TrajectoryDir); only set when
	// the call was made with WithEval.
	TrajectoryID string           `json:"trajectory_id,omitempty"`
	Trajectory   *atif.Trajectory `json:"trajectory,omitempty"`
}

// Remember extracts entities and temporal edges from text and stores them (with the raw text as an artifact of the
// given kind; empty means DefaultKind, i.e. knowledge) in one transaction, inside userID's named dataset (empty
// means DefaultDataset; it is created if missing). ref is the
// time the text was written/observed; the zero value means the current time. If any LLM step fails nothing is
// stored.
//
// Only with WithEval is the call recorded as an ATIF trajectory (stored as JSON in TrajectoryDir and returned in
// Result.Trajectory, with its ID in Result.TrajectoryID).
func Remember(ctx context.Context, d *sql.DB, llm LLM, userID, dataset, kind, text string, ref time.Time, opts ...Option) (res *Result, err error) {
	userID, err = ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = ResolveDatasetName(dataset)
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
	llm = withRecording(llm, d, CallRemember)

	tr := startTrace(CallRemember, newCallOptions(opts))
	defer func() {
		if traj := tr.finish(err); res != nil && traj != nil {
			res.TrajectoryID, res.Trajectory = traj.TrajectoryID, traj
		}
	}()
	llm = tr.wrap(llm)
	tr.user(text)
	tr.input(map[string]any{"user_id": userID, "dataset": dataset, "kind": kind, "reference_time": relational.FormatTime(ref)})

	ex, err := Extract(ctx, llm, text, ref)
	if err != nil {
		return nil, err
	}
	cands, err := loadCandidates(ctx, d, userID, dataset, ex.Edges)
	if err != nil {
		return nil, err
	}
	tr.tool("Loaded stored facts that new facts may duplicate or contradict.", "load_candidates",
		map[string]any{"user_id": userID, "dataset": dataset, "new_edges": len(ex.Edges)},
		map[string]any{"candidates": len(cands)})
	plan, err := resolveEdges(ctx, llm, ex.Edges, cands)
	if err != nil {
		return nil, err
	}
	res, err = Store(ctx, d, userID, dataset, kind, text, ref, now, ex, plan)
	if err != nil {
		return nil, err
	}
	tr.tool("Stored the artifact, entities and edges.", "store_artifact",
		map[string]any{"user_id": userID, "dataset": dataset, "kind": kind},
		map[string]any{"artifact_id": res.ArtifactID, "entities": res.Entities, "edges": res.Edges, "invalidated_edges": res.InvalidatedEdges})
	return res, nil
}

// Store persists an artifact of the given kind (already resolved, see ResolveKind) and its extraction into userID's named dataset (created if missing). Entities are
// deduplicated within the dataset by (normalized name, type); nothing is shared across datasets. Edges are
// written according to plan (see EdgePlan); now is the system time stamped on them. Edges naming an unlisted
// entity create it with type "other".
func Store(ctx context.Context, d *sql.DB, userID, dataset, kind, text string, ref, now time.Time, ex *Extraction, plan *EdgePlan) (*Result, error) {
	userID, err := ResolveUserID(userID)
	if err != nil {
		return nil, err
	}
	dataset, err = ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	// The dataset belongs to the relational store and the graph to the graph store, so they are written separately.
	dsID, err := relational.EnsureDataset(ctx, d, userID, dataset)
	if err != nil {
		return nil, err
	}
	tx, err := graph.Begin(ctx, d)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	artID, err := tx.InsertArtifact(ctx, dsID, kind, text, ref, now)
	if err != nil {
		return nil, err
	}

	out := &Result{
		UserID:           userID,
		Dataset:          dataset,
		Kind:             kind,
		ArtifactID:       artID,
		ValidAt:          ref.UTC(),
		CreatedAt:        now.UTC(),
		Entities:         []Entity{},
		Edges:            []Edge{},
		InvalidatedEdges: []int64{},
	}
	ids := map[string]int64{} // normalized name -> entity id (first listed type wins for edge lookup)
	seenEnt := map[string]bool{}

	// Every entity this artifact touches is linked to it, whether it was created now or already existed: entities
	// are shared across artifacts (deduplicated per dataset), so the link, not the entity row, records provenance.
	upsertEntity := func(e Entity) (int64, error) {
		id, err := tx.UpsertEntity(ctx, dsID, e, key(e.Name))
		if err != nil {
			return 0, err
		}
		return id, tx.LinkEntity(ctx, id, artID)
	}

	for _, e := range ex.Entities {
		e.Name = strings.TrimSpace(e.Name)
		e.Type = normType(e.Type)
		e.Description = strings.TrimSpace(e.Description)
		if e.Name == "" {
			continue
		}
		id, err := upsertEntity(e)
		if err != nil {
			return nil, err
		}
		if _, ok := ids[key(e.Name)]; !ok {
			ids[key(e.Name)] = id
		}
		if k := key(e.Name) + "\x00" + e.Type; !seenEnt[k] {
			seenEnt[k] = true
			out.Entities = append(out.Entities, e)
		}
	}

	// Edge endpoints: an entity from this extraction, else an already-stored entity of any type, else a new "other".
	entityID := func(name string) (int64, error) {
		if id, ok := ids[key(name)]; ok {
			return id, nil
		}
		id, err := tx.FindEntityID(ctx, dsID, key(name))
		if errors.Is(err, graph.ErrNotFound) {
			if id, err = upsertEntity(Entity{Name: name, Type: "other"}); err == nil {
				out.Entities = append(out.Entities, Entity{Name: name, Type: "other"})
			}
		} else if err == nil {
			// An edge names an already stored entity that this extraction did not list: it is still mentioned here.
			err = tx.LinkEntity(ctx, id, artID)
		}
		if err != nil {
			return 0, err
		}
		ids[key(name)] = id
		return id, nil
	}
	out.Edges, out.InvalidatedEdges, err = storeEdges(ctx, tx, dsID, artID, ref, now, ex.Edges, plan, entityID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
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
