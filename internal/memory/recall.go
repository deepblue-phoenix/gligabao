package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ivan/gligabao/internal/ai"
)

const (
	maxQueryTokens  = 12
	maxSeedEntities = 10  // entities matched directly by the query
	maxContextEdges = 100 // edges (1 hop around the seeds) given to the LLM
	maxFallbackSeed = 25  // most recent entities used when nothing matches the query
)

//---------------------------------------------------------------------------------------------

// EvidenceEntity is a graph entity cited by an answer.
type EvidenceEntity struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// EvidenceEdge is a graph edge cited by an answer.
type EvidenceEdge struct {
	ID          int64  `json:"id"`
	SourceID    int64  `json:"source_id"`
	TargetID    int64  `json:"target_id"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Relation    string `json:"relation"`
	Description string `json:"description,omitempty"`
	MemoryID    *int64 `json:"memory_id,omitempty"`
	// Bi-temporal validity of the fact (see temporal.go).
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
}

// Evidence lists the entities and edges the answer is based on.
type Evidence struct {
	Entities []EvidenceEntity `json:"entities"`
	Edges    []EvidenceEdge   `json:"edges"`
}

// RecallResult is the answer to a query together with its supporting evidence.
type RecallResult struct {
	Answer   string   `json:"answer"`
	Evidence Evidence `json:"evidence"`
}

//---------------------------------------------------------------------------------------------

// Recall answers query using the knowledge graph: relevant entities and their edges are retrieved,
// the LLM answers from that context, and the entities/edges it used are returned as evidence. Only the named
// dataset (empty means DefaultDataset) is searched; ErrDatasetNotFound is returned if it does not exist.
func Recall(ctx context.Context, d *sql.DB, llm LLM, dataset, query string) (*RecallResult, error) {
	dataset, err := ResolveDatasetName(dataset)
	if err != nil {
		return nil, err
	}
	dsID, err := datasetID(ctx, d, dataset)
	if err != nil {
		return nil, err
	}
	g, err := retrieve(ctx, d, dsID, query)
	if err != nil {
		return nil, fmt.Errorf("retrieve context: %w", err)
	}
	if len(g.entities) == 0 {
		return &RecallResult{
			Answer:   "I don't have any information in memory to answer that.",
			Evidence: Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}},
		}, nil
	}

	llm = withRecording(llm, d, CallRecall)
	temp := 0.0
	resp, err := llm.Complete(ctx, ai.Request{
		System: recallSystemPrompt,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: "<current_time>\n" + time.Now().UTC().Format(timeLayout) +
			"\n</current_time>\n\n<graph>\n" + g.render() + "</graph>\n\n<question>\n" + query + "\n</question>"}},
		MaxTokens:   2048,
		Temperature: &temp,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM output truncated")
	}
	out, err := parseRecall(resp.Text)
	if err != nil {
		return nil, err
	}
	return g.result(out), nil
}

//---------------------------------------------------------------------------------------------

type recallOutput struct {
	Answer   string  `json:"answer"`
	Entities []int64 `json:"entities"`
	Edges    []int64 `json:"edges"`
}

func parseRecall(s string) (*recallOutput, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return nil, errors.New("memory: no JSON object in LLM output")
	}
	var o recallOutput
	if err := json.Unmarshal([]byte(s[start:end+1]), &o); err != nil {
		return nil, fmt.Errorf("memory: decode LLM output: %w", err)
	}
	o.Answer = strings.TrimSpace(o.Answer)
	if o.Answer == "" {
		return nil, errors.New("memory: LLM returned an empty answer")
	}
	return &o, nil
}

//---------------------------------------------------------------------------------------------

// graphContext is the subgraph handed to the LLM.
type graphContext struct {
	entities map[int64]EvidenceEntity
	edges    map[int64]EvidenceEdge
}

func (g *graphContext) render() string {
	var sb strings.Builder
	for _, id := range sortedKeys(g.entities) {
		e := g.entities[id]
		fmt.Fprintf(&sb, "[entity %d] %s (%s)", e.ID, e.Name, e.Type)
		if e.Description != "" {
			sb.WriteString(": " + e.Description)
		}
		sb.WriteByte('\n')
	}
	for _, id := range sortedKeys(g.edges) {
		e := g.edges[id]
		fmt.Fprintf(&sb, "[edge %d] %s(%d) --%s--> %s(%d)", e.ID, e.Source, e.SourceID, e.Relation, e.Target, e.TargetID)
		if e.Description != "" {
			sb.WriteString(": " + e.Description)
		}
		sb.WriteString(" [valid_at=" + e.ValidAt.UTC().Format(timeLayout))
		if e.InvalidAt != nil {
			sb.WriteString(", invalid_at=" + e.InvalidAt.UTC().Format(timeLayout))
		}
		if e.ExpiredAt != nil {
			sb.WriteString(", superseded")
		}
		sb.WriteString("]\n")
	}
	return sb.String()
}

// result builds the response, keeping only cited IDs that were present in the context. Endpoints of
// cited edges are included as cited entities.
func (g *graphContext) result(o *recallOutput) *RecallResult {
	ev := Evidence{Entities: []EvidenceEntity{}, Edges: []EvidenceEdge{}}
	ents := map[int64]bool{}
	for _, id := range o.Entities {
		if _, ok := g.entities[id]; ok {
			ents[id] = true
		}
	}
	seen := map[int64]bool{}
	for _, id := range o.Edges {
		e, ok := g.edges[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		ev.Edges = append(ev.Edges, e)
		ents[e.SourceID], ents[e.TargetID] = true, true
	}
	sort.Slice(ev.Edges, func(i, j int) bool { return ev.Edges[i].ID < ev.Edges[j].ID })
	for _, id := range sortedKeys(g.entities) {
		if ents[id] {
			ev.Entities = append(ev.Entities, g.entities[id])
		}
	}
	return &RecallResult{Answer: o.Answer, Evidence: ev}
}

func sortedKeys[V any](m map[int64]V) []int64 {
	ks := make([]int64, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

//---------------------------------------------------------------------------------------------

// retrieve finds entities matching the query text, then adds the edges touching them and the entities
// at the other end. If no entity matches, the most recently updated entities are used instead.
func retrieve(ctx context.Context, d *sql.DB, dsID int64, query string) (*graphContext, error) {
	g := &graphContext{entities: map[int64]EvidenceEntity{}, edges: map[int64]EvidenceEdge{}}

	seeds, err := matchEntities(ctx, d, dsID, query)
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, dsID, maxFallbackSeed)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			seeds = append(seeds, id)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(seeds) == 0 {
		return g, nil
	}

	ph, args := placeholders(seeds)
	args = append(args, args...)
	args = append([]any{dsID}, args...)
	args = append(args, maxContextEdges)
	rows, err := d.QueryContext(ctx, `
		SELECT e.id, e.source_id, e.target_id, s.name, t.name, e.relation, e.description, e.memory_id,
			e.valid_at, e.invalid_at, e.created_at, e.expired_at
		FROM gb_edges e
		JOIN gb_entities s ON s.id = e.source_id
		JOIN gb_entities t ON t.id = e.target_id
		WHERE e.dataset_id = ? AND (e.source_id IN (`+ph+`) OR e.target_id IN (`+ph+`))
		ORDER BY e.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for _, id := range seeds {
		ids[id] = true
	}
	for rows.Next() {
		var e EvidenceEdge
		var mem sql.NullInt64
		var valid, invalid, created, expired sql.NullString
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Source, &e.Target, &e.Relation, &e.Description, &mem,
			&valid, &invalid, &created, &expired); err != nil {
			return nil, err
		}
		e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = parseTime(valid.String), parseTime(invalid.String), parseTime(created.String), parseTime(expired.String)
		if mem.Valid {
			e.MemoryID = &mem.Int64
		}
		g.edges[e.ID] = e
		ids[e.SourceID], ids[e.TargetID] = true, true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	all := make([]int64, 0, len(ids))
	for id := range ids {
		all = append(all, id)
	}
	ph, args = placeholders(all)
	erows, err := d.QueryContext(ctx, `SELECT id, name, type, description FROM gb_entities WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer erows.Close()
	for erows.Next() {
		var e EvidenceEntity
		if err := erows.Scan(&e.ID, &e.Name, &e.Type, &e.Description); err != nil {
			return nil, err
		}
		g.entities[e.ID] = e
	}
	return g, erows.Err()
}

// matchEntities returns the IDs of the best entities whose name or description mention query terms.
func matchEntities(ctx context.Context, d *sql.DB, dsID int64, query string) ([]int64, error) {
	toks := tokenize(query)
	qkey := key(query)

	// Score: 5 if the full entity name appears in the query; per token, 3 for a name hit and 1 for a description hit.
	conds := []string{"instr(?, name_key) > 0"}
	whereArgs := []any{qkey}
	score := []string{"(CASE WHEN instr(?, name_key) > 0 THEN 5 ELSE 0 END)"}
	scoreArgs := []any{qkey}
	for _, t := range toks {
		conds = append(conds, "instr(name_key, ?) > 0", "instr(lower(description), ?) > 0")
		whereArgs = append(whereArgs, t, t)
		score = append(score, "(CASE WHEN instr(name_key, ?) > 0 THEN 3 ELSE 0 END)", "(CASE WHEN instr(lower(description), ?) > 0 THEN 1 ELSE 0 END)")
		scoreArgs = append(scoreArgs, t, t)
	}
	// Placeholder order in the SQL text: WHERE, ORDER BY, LIMIT.
	args := append(append(append([]any{dsID}, whereArgs...), scoreArgs...), maxSeedEntities)
	rows, err := d.QueryContext(ctx, `SELECT id FROM gb_entities WHERE dataset_id = ? AND (`+strings.Join(conds, " OR ")+`)
		ORDER BY (`+strings.Join(score, " + ")+`) DESC, updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func placeholders(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be by did do does for from had has have how i in is it its me my of on or
		that the their them there they this to was we were what when where which who whom whose why will with you your about tell
		know can could would should been being into than then these those his her him she he us our`) {
		stopwords[w] = true
	}
}

// tokenize lowercases the query and returns up to maxQueryTokens distinct, non-trivial terms.
func tokenize(q string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 2 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == maxQueryTokens {
			break
		}
	}
	return out
}
