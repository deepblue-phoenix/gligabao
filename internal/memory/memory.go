// Package memory turns free text into a bi-temporal knowledge graph (modelled on Graphiti) with an LLM and stores it in SQLite.
//
// Every input is an episode (a row in gb_memories) with an event time (ValidAt: when the text was written or
// observed) and a system time (CreatedAt: when it was ingested). The facts extracted from it are edges between
// entities; each edge carries its own event time (ValidAt / InvalidAt) and system time (CreatedAt / ExpiredAt).
// See temporal.go for how facts are reconciled with what is already stored.
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/ai"
)

//---------------------------------------------------------------------------------------------

// Entity is a thing mentioned in the text (person, place, project, concept, ...).
type Entity struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// Edge is a fact relating two entities (referenced by name), with its bi-temporal validity.
// Description is the fact as a sentence, e.g. "Alice works at Acme."
type Edge struct {
	ID          int64      `json:"id,omitempty"`
	Source      string     `json:"source"`
	Target      string     `json:"target"`
	Relation    string     `json:"relation"`
	Description string     `json:"description,omitempty"`
	ValidAt     *time.Time `json:"valid_at,omitempty"`   // when the fact became true
	InvalidAt   *time.Time `json:"invalid_at,omitempty"` // when it stopped being true; nil while it is still true
	CreatedAt   *time.Time `json:"created_at,omitempty"` // when we learned about it
	ExpiredAt   *time.Time `json:"expired_at,omitempty"` // when we learned it was superseded
}

// Extraction is the structured result of processing a block of text.
type Extraction struct {
	Entities []Entity `json:"entities"`
	Edges    []Edge   `json:"edges"`
}

// LLM is the subset of ai.Client used here.
type LLM interface {
	Complete(ctx context.Context, req ai.Request) (*ai.Response, error)
}

//---------------------------------------------------------------------------------------------

// Extract asks the LLM to pull entities and temporal edges out of text. ref is the episode's time; it anchors
// relative dates and is the ValidAt of every fact whose start the text does not state.
func Extract(ctx context.Context, llm LLM, text string, ref time.Time) (*Extraction, error) {
	temp := 0.0
	resp, err := llm.Complete(ctx, ai.Request{
		System: entitySystemPrompt,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "<reference_time>\n" + ref.UTC().Format(timeLayout) +
			"\n</reference_time>\n\n<text>\n" + text + "\n</text>"}},
		MaxTokens:   4096,
		Temperature: &temp,
	})
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
		e := Edge{
			Source:      strings.TrimSpace(r.Source),
			Target:      strings.TrimSpace(r.Target),
			Relation:    normRelation(r.Relation),
			Description: strings.TrimSpace(r.Description),
			ValidAt:     parseTime(r.ValidAt),
			InvalidAt:   parseTime(r.InvalidAt),
		}
		if e.Source == "" || e.Target == "" || e.Relation == "" {
			continue
		}
		if e.Description == "" {
			e.Description = e.Source + " " + strings.ReplaceAll(e.Relation, "_", " ") + " " + e.Target + "."
		}
		if e.ValidAt == nil { // a fact inherits the time of the episode that states it
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

// Result summarizes what was stored by Remember.
type Result struct {
	Dataset          string    `json:"dataset"`
	MemoryID         int64     `json:"memory_id"`
	ValidAt          time.Time `json:"valid_at"`   // event time of the episode
	CreatedAt        time.Time `json:"created_at"` // ingestion time of the episode
	Entities         []Entity  `json:"entities"`
	Edges            []Edge    `json:"edges"`             // the facts from this text in their stored state
	InvalidatedEdges []int64   `json:"invalidated_edges"` // IDs of edges closed because of this memory
}

// Remember extracts entities and temporal edges from text and stores them (with the raw text as an episode) in
// one transaction, inside the named dataset (empty means DefaultDataset; it is created if missing). ref is the
// time the text was written/observed; the zero value means the current time. If any LLM step fails nothing is
// stored.
func Remember(ctx context.Context, d *sql.DB, llm LLM, dataset, text string, ref time.Time) (*Result, error) {
	dataset, err := ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	if ref.IsZero() {
		ref = now
	}
	ref = ref.UTC().Truncate(time.Second)
	llm = withRecording(llm, d, CallRemember)

	ex, err := Extract(ctx, llm, text, ref)
	if err != nil {
		return nil, err
	}
	cands, err := loadCandidates(ctx, d, dataset, ex.Edges)
	if err != nil {
		return nil, err
	}
	plan, err := resolveEdges(ctx, llm, ex.Edges, cands)
	if err != nil {
		return nil, err
	}
	return Store(ctx, d, dataset, text, ref, now, ex, plan)
}

// Store persists an episode and its extraction into the named dataset (created if missing). Entities are
// deduplicated within the dataset by (normalized name, type); nothing is shared across datasets. Edges are
// written according to plan (see EdgePlan); now is the system time stamped on them. Edges naming an unlisted
// entity create it with type "other".
func Store(ctx context.Context, d *sql.DB, dataset, text string, ref, now time.Time, ex *Extraction, plan *EdgePlan) (*Result, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	dataset, err = ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	dsID, err := ensureDatasetID(ctx, tx, dataset)
	if err != nil {
		return nil, err
	}

	res, err := tx.ExecContext(ctx, `INSERT INTO gb_memories (dataset_id, text, valid_at, created_at) VALUES (?, ?, ?, ?)`,
		dsID, text, ref.UTC().Format(timeLayout), now.UTC().Format(timeLayout))
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}
	memID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	out := &Result{Dataset: dataset, MemoryID: memID, ValidAt: ref.UTC(), CreatedAt: now.UTC(),
		Entities: []Entity{}, Edges: []Edge{}, InvalidatedEdges: []int64{}}
	ids := map[string]int64{} // normalized name -> entity id (first listed type wins for edge lookup)
	seenEnt := map[string]bool{}

	upsertEntity := func(e Entity) (int64, error) {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_entities (dataset_id, name, name_key, type, description) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (dataset_id, name_key, type) DO UPDATE SET
				description = CASE WHEN excluded.description != '' THEN excluded.description ELSE gb_entities.description END,
				updated_at  = CURRENT_TIMESTAMP
			RETURNING id`,
			dsID, e.Name, key(e.Name), e.Type, e.Description).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("upsert entity %q: %w", e.Name, err)
		}
		return id, nil
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
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? AND name_key = ? ORDER BY id LIMIT 1`, dsID, key(name)).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if id, err = upsertEntity(Entity{Name: name, Type: "other"}); err == nil {
				out.Entities = append(out.Entities, Entity{Name: name, Type: "other"})
			}
		}
		if err != nil {
			return 0, err
		}
		ids[key(name)] = id
		return id, nil
	}
	out.Edges, out.InvalidatedEdges, err = storeEdges(ctx, tx, dsID, memID, ref, now, ex.Edges, plan, entityID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

//---------------------------------------------------------------------------------------------

func key(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }

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
