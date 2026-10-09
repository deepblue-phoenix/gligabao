// Package memory extracts entities, edges and temporal events from free text with an LLM and stores them in SQLite.
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

// Edge is a directed relation between two entities, referenced by name.
type Edge struct {
	Source      string `json:"source"`
	Target      string `json:"target"`
	Relation    string `json:"relation"`
	Description string `json:"description,omitempty"`
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

const systemPrompt = `You extract a knowledge graph from text.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{
  "entities": [{"name": "...", "type": "...", "description": "..."}],
  "edges": [{"source": "...", "target": "...", "relation": "...", "description": "..."}]
}

Rules:
- Entities are specific, meaningful things: people, organizations, places, projects, products, concepts, events, etc.
- "type" is a single lowercase word such as person, organization, place, project, product, concept, event, other.
- "description" is a short factual summary based only on the text (may be empty).
- Edges are directed: source --relation--> target. "relation" is a short lowercase snake_case verb phrase, e.g. works_at, lives_in, founded, part_of.
- "source" and "target" MUST exactly match the "name" of an entity you listed.
- Use the most complete, canonical name for each entity; do not list the same entity twice.
- Only include facts stated or clearly implied by the text. If there is nothing to extract, return {"entities": [], "edges": []}.`

// Extract asks the LLM to pull entities and edges out of text.
func Extract(ctx context.Context, llm LLM, text string) (*Extraction, error) {
	temp := 0.0
	resp, err := llm.Complete(ctx, ai.Request{
		System:      systemPrompt,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: "<text>\n" + text + "\n</text>"}},
		MaxTokens:   4096,
		Temperature: &temp,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM output truncated; text too large")
	}
	return parseExtraction(resp.Text)
}

// parseExtraction decodes the LLM's JSON, tolerating surrounding prose or code fences.
func parseExtraction(s string) (*Extraction, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("memory: no JSON object in LLM output")
	}
	var ex Extraction
	if err := json.Unmarshal([]byte(s[start:end+1]), &ex); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	return &ex, nil
}

//---------------------------------------------------------------------------------------------

// Result summarizes what was stored by Remember.
type Result struct {
	MemoryID          int64    `json:"memory_id"`
	Entities          []Entity `json:"entities"`
	Edges             []Edge   `json:"edges"`
	Events            []Event  `json:"events"`
	InvalidatedEvents []int64  `json:"invalidated_events"` // IDs of events superseded by this memory
}

// Remember extracts entities, edges and temporal events from text and stores them (with the raw text) in one
// transaction. ref is the time the text was written/observed ("now" for relative dates in it); the zero
// value means the current time. If any LLM step fails nothing is stored.
func Remember(ctx context.Context, d *sql.DB, llm LLM, text string, ref time.Time) (*Result, error) {
	now := time.Now().UTC()
	if ref.IsZero() {
		ref = now
	}
	ref = ref.UTC()
	llm = withRecording(llm, d, CallRemember)

	ex, err := Extract(ctx, llm, text)
	if err != nil {
		return nil, err
	}
	evs, err := ExtractEvents(ctx, llm, text, ref, ex)
	if err != nil {
		return nil, err
	}
	cands, err := loadCandidates(ctx, d, evs)
	if err != nil {
		return nil, err
	}
	plan, err := resolveEvents(ctx, llm, evs, cands)
	if err != nil {
		return nil, err
	}
	return Store(ctx, d, text, ref, now, ex, evs, plan)
}

// Store persists an extraction. Entities are deduplicated by (normalized name, type); edges by
// (source, target, relation). Edges naming an unlisted entity create it with type "other".
// Events are written according to plan (see EventPlan); now is the system time stamped on them.
func Store(ctx context.Context, d *sql.DB, text string, ref, now time.Time, ex *Extraction, evs []Event, plan *EventPlan) (*Result, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `INSERT INTO gb_memories (text, reference_time) VALUES (?, ?)`, text, ref.UTC().Format(timeLayout))
	if err != nil {
		return nil, fmt.Errorf("insert memory: %w", err)
	}
	memID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	out := &Result{MemoryID: memID, Entities: []Entity{}, Edges: []Edge{}, Events: []Event{}, InvalidatedEvents: []int64{}}
	ids := map[string]int64{} // normalized name -> entity id (first listed type wins for edge lookup)
	seenEnt := map[string]bool{}

	upsertEntity := func(e Entity) (int64, error) {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_entities (name, name_key, type, description) VALUES (?, ?, ?, ?)
			ON CONFLICT (name_key, type) DO UPDATE SET
				description = CASE WHEN excluded.description != '' THEN excluded.description ELSE gb_entities.description END,
				updated_at  = CURRENT_TIMESTAMP
			RETURNING id`,
			e.Name, key(e.Name), e.Type, e.Description).Scan(&id)
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

	resolve := func(name string) (int64, error) {
		if id, ok := ids[key(name)]; ok {
			return id, nil
		}
		id, err := upsertEntity(Entity{Name: name, Type: "other"})
		if err != nil {
			return 0, err
		}
		ids[key(name)] = id
		out.Entities = append(out.Entities, Entity{Name: name, Type: "other"})
		return id, nil
	}

	for _, e := range ex.Edges {
		e.Source, e.Target = strings.TrimSpace(e.Source), strings.TrimSpace(e.Target)
		e.Relation = normRelation(e.Relation)
		e.Description = strings.TrimSpace(e.Description)
		if e.Source == "" || e.Target == "" || e.Relation == "" {
			continue
		}
		src, err := resolve(e.Source)
		if err != nil {
			return nil, err
		}
		dst, err := resolve(e.Target)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gb_edges (source_id, target_id, relation, description, memory_id) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (source_id, target_id, relation) DO UPDATE SET
				description = CASE WHEN excluded.description != '' THEN excluded.description ELSE gb_edges.description END,
				memory_id   = excluded.memory_id`,
			src, dst, e.Relation, e.Description, memID); err != nil {
			return nil, fmt.Errorf("upsert edge: %w", err)
		}
		out.Edges = append(out.Edges, e)
	}

	// Event participants: entities from this extraction, else an already-stored entity of any type, else a new "other".
	participant := func(name string) (int64, error) {
		if id, ok := ids[key(name)]; ok {
			return id, nil
		}
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM gb_entities WHERE name_key = ? ORDER BY id LIMIT 1`, key(name)).Scan(&id)
		if err == nil {
			ids[key(name)] = id
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		return resolve(name)
	}
	out.Events, out.InvalidatedEvents, err = storeEvents(ctx, tx, memID, ref, now, evs, plan, participant)
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
