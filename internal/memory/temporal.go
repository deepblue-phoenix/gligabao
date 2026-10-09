package memory

// Bi-temporal reconciliation of facts, modelled on Graphiti's entity edges.
//
// Every edge carries two timelines:
//   - event time:  ValidAt / InvalidAt  - when the fact holds in the real world (InvalidAt nil = still true);
//   - system time: CreatedAt / ExpiredAt - when we learned about it / when newer knowledge superseded it.
//
// Pipeline (run by Remember, after Extract):
//  1. loadCandidates + resolveEdges: stored live edges touching the same entities are compared with the new
//     ones; exact repeats are found in Go, the LLM flags other duplicates and contradictions.
//  2. storeEdges: duplicates are merged; for each contradiction the older fact is invalidated
//     (InvalidAt = start of the newer one, ExpiredAt = now) instead of being deleted.

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

const (
	timeLayout    = "2006-01-02T15:04:05Z"
	maxCandidates = 50 // existing edges shown to the LLM for dedup / contradiction checks
)

// EdgePlan is the outcome of comparing new edges against existing ones.
type EdgePlan struct {
	Duplicates     map[int]int64 // index of new edge -> id of the existing edge it repeats
	Contradictions []Contradiction
}

// Contradiction says that the existing edge can't hold together with the new edge.
type Contradiction struct {
	New      int
	Existing int64
}

//---------------------------------------------------------------------------------------------

// resolveEdges compares new edges with existing candidates. Exact repeats (same endpoints and relation as a
// fact that is still true) are detected without the LLM; the LLM is only called for what remains.
func resolveEdges(ctx context.Context, llm LLM, edges, cands []Edge) (*EdgePlan, error) {
	plan := &EdgePlan{Duplicates: map[int]int64{}}

	var rest []int // indices of new edges still undecided
	for i, e := range edges {
		dup := false
		for _, c := range cands {
			if c.InvalidAt == nil && sameTriple(e, c) {
				plan.Duplicates[i] = c.ID
				dup = true
				break
			}
		}
		if !dup {
			rest = append(rest, i)
		}
	}
	if len(rest) == 0 || len(cands) == 0 {
		return plan, nil
	}

	var b strings.Builder
	b.WriteString("<new_facts>\n")
	for _, i := range rest {
		fmt.Fprintf(&b, "[%d] %s %s\n", i, edges[i].Description, span(edges[i].ValidAt, edges[i].InvalidAt))
	}
	b.WriteString("</new_facts>\n\n<existing_facts>\n")
	known := map[int64]bool{}
	for _, c := range cands {
		known[c.ID] = true
		fmt.Fprintf(&b, "[%d] %s %s\n", c.ID, c.Description, span(c.ValidAt, c.InvalidAt))
	}
	b.WriteString("</existing_facts>")

	temp := 0.0
	resp, err := llm.Complete(ctx, ai.Request{
		System:      resolveSystemPrompt,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: b.String()}},
		MaxTokens:   2048,
		Temperature: &temp,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM resolve output truncated")
	}
	js, err := jsonObject(resp.Text)
	if err != nil {
		return nil, err
	}
	type pair struct {
		New      int   `json:"new"`
		Existing int64 `json:"existing"`
	}
	var out struct {
		Duplicates     []pair `json:"duplicates"`
		Contradictions []pair `json:"contradictions"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("memory: decode LLM resolve output: %w", err)
	}

	pending := map[int]bool{}
	for _, i := range rest {
		pending[i] = true
	}
	// Hallucinated numbers/IDs are dropped by checking against what was actually shown.
	for _, p := range out.Duplicates {
		if pending[p.New] && known[p.Existing] {
			plan.Duplicates[p.New] = p.Existing
			delete(pending, p.New)
		}
	}
	seen := map[Contradiction]bool{}
	for _, p := range out.Contradictions {
		c := Contradiction{New: p.New, Existing: p.Existing}
		if pending[p.New] && known[p.Existing] && !seen[c] {
			seen[c] = true
			plan.Contradictions = append(plan.Contradictions, c)
		}
	}
	return plan, nil
}

// sameTriple reports whether two edges have the same endpoints and relation.
func sameTriple(a, b Edge) bool {
	return key(a.Source) == key(b.Source) && key(a.Target) == key(b.Target) && a.Relation == b.Relation
}

func span(valid, invalid *time.Time) string {
	f := func(t *time.Time) string {
		if t == nil {
			return "?"
		}
		return t.UTC().Format(timeLayout)
	}
	return "(" + f(valid) + " -> " + f(invalid) + ")"
}

//---------------------------------------------------------------------------------------------

const edgeColumns = `e.id, s.name, t.name, e.relation, e.description, e.valid_at, e.invalid_at, e.created_at, e.expired_at`
const edgeFrom = ` FROM gb_edges e JOIN gb_entities s ON s.id = e.source_id JOIN gb_entities t ON t.id = e.target_id`

type scanner interface{ Scan(dest ...any) error }

func scanEdge(r scanner) (Edge, error) {
	var e Edge
	var valid, invalid, created, expired sql.NullString
	if err := r.Scan(&e.ID, &e.Source, &e.Target, &e.Relation, &e.Description, &valid, &invalid, &created, &expired); err != nil {
		return e, err
	}
	e.ValidAt, e.InvalidAt, e.CreatedAt, e.ExpiredAt = parseTime(valid.String), parseTime(invalid.String), parseTime(created.String), parseTime(expired.String)
	return e, nil
}

// loadCandidates returns live (not expired) stored edges touching at least one entity named in edges, newest first.
func loadCandidates(ctx context.Context, d *sql.DB, edges []Edge) ([]Edge, error) {
	var keys []any
	seen := map[string]bool{}
	for _, e := range edges {
		for _, n := range []string{e.Source, e.Target} {
			if k := key(n); !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	ph := qmarks(len(keys))
	args := append(append(append([]any{}, keys...), keys...), maxCandidates)
	rows, err := d.QueryContext(ctx, `SELECT `+edgeColumns+edgeFrom+`
		WHERE e.expired_at IS NULL AND (s.name_key IN (`+ph+`) OR t.name_key IN (`+ph+`))
		ORDER BY e.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("load edge candidates: %w", err)
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		e, err := scanEdge(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// loadEdge reads one edge with its endpoint names.
func loadEdge(ctx context.Context, tx *sql.Tx, id int64) (*Edge, error) {
	e, err := scanEdge(tx.QueryRowContext(ctx, `SELECT `+edgeColumns+edgeFrom+` WHERE e.id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("load edge %d: %w", id, err)
	}
	return &e, nil
}

//---------------------------------------------------------------------------------------------

// storeEdges writes edges inside tx according to plan and returns the resulting edges (new ones, or the
// existing ones they duplicate) plus the IDs of edges that were invalidated. entityID maps an entity name to
// its ID, creating the entity if needed.
func storeEdges(ctx context.Context, tx *sql.Tx, memID int64, ref, now time.Time, edges []Edge, plan *EdgePlan,
	entityID func(name string) (int64, error)) ([]Edge, []int64, error) {
	if plan == nil {
		plan = &EdgePlan{}
	}
	nowS := now.UTC().Format(timeLayout)
	ids := make([]int64, len(edges)) // stored ID for each new edge
	batch := map[string]int64{}      // repeats inside this memory
	var order []int64
	inOrder := map[int64]bool{}

	for i, e := range edges {
		if existing, ok := plan.Duplicates[i]; ok {
			// Same fact seen again: keep the stored edge, just record its end if the text says it ended.
			if _, err := tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = COALESCE(invalid_at, ?) WHERE id = ?`,
				fmtTime(e.InvalidAt), existing); err != nil {
				return nil, nil, fmt.Errorf("merge duplicate edge: %w", err)
			}
			ids[i] = existing
			if !inOrder[existing] {
				inOrder[existing] = true
				order = append(order, existing)
			}
			continue
		}
		bk := key(e.Source) + "\x00" + key(e.Target) + "\x00" + e.Relation
		if id, ok := batch[bk]; ok {
			ids[i] = id
			continue
		}
		src, err := entityID(e.Source)
		if err != nil {
			return nil, nil, err
		}
		dst, err := entityID(e.Target)
		if err != nil {
			return nil, nil, err
		}
		validAt := ref.UTC()
		if e.ValidAt != nil {
			validAt = e.ValidAt.UTC()
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gb_edges (source_id, target_id, relation, description, valid_at, invalid_at, created_at, memory_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			src, dst, e.Relation, e.Description, validAt.Format(timeLayout), fmtTime(e.InvalidAt), nowS, memID)
		if err != nil {
			return nil, nil, fmt.Errorf("insert edge: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, nil, err
		}
		ids[i] = id
		batch[bk] = id
		inOrder[id] = true
		order = append(order, id)
	}

	var invalidated []int64
	for _, c := range plan.Contradictions {
		if c.New < 0 || c.New >= len(edges) {
			continue
		}
		inv, err := contradict(ctx, tx, ids[c.New], c.Existing, ref, nowS)
		if err != nil {
			return nil, nil, err
		}
		invalidated = append(invalidated, inv...)
	}

	out := make([]Edge, 0, len(order))
	for _, id := range order {
		e, err := loadEdge(ctx, tx, id)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *e)
	}
	return out, invalidated, nil
}

// contradict applies Graphiti's invalidation rule to a new edge and an existing one that conflicts with it.
// The fact that started earlier is the one that ended: it gets invalid_at = start of the later fact and
// expired_at = now. Facts whose periods don't overlap are left alone. It returns the IDs invalidated.
func contradict(ctx context.Context, tx *sql.Tx, newID, oldID int64, ref time.Time, nowS string) ([]int64, error) {
	if newID == oldID {
		return nil, nil
	}
	nw, err := loadEdge(ctx, tx, newID)
	if err != nil {
		return nil, err
	}
	old, err := loadEdge(ctx, tx, oldID)
	if errors.Is(err, sql.ErrNoRows) { // deleted since the candidates were read
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if old.ExpiredAt != nil {
		return nil, nil
	}

	newStart := ref.UTC()
	if nw.ValidAt != nil {
		newStart = *nw.ValidAt
	}
	// No overlap: the old one ended before the new one began, or the new one ended before the old began.
	if old.InvalidAt != nil && !old.InvalidAt.After(newStart) {
		return nil, nil
	}
	if nw.InvalidAt != nil && old.ValidAt != nil && !nw.InvalidAt.After(*old.ValidAt) {
		return nil, nil
	}

	loser, endsAt := oldID, newStart
	if old.ValidAt != nil && old.ValidAt.After(newStart) {
		// The new information is about an earlier time than what we already know: the new fact is the one superseded.
		loser, endsAt = newID, *old.ValidAt
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_edges SET invalid_at = ?, expired_at = ? WHERE id = ?`,
		endsAt.UTC().Format(timeLayout), nowS, loser); err != nil {
		return nil, fmt.Errorf("invalidate edge: %w", err)
	}
	return []int64{loser}, nil
}

//---------------------------------------------------------------------------------------------

// jsonObject returns the outermost {...} in s, tolerating surrounding prose or code fences.
func jsonObject(s string) (string, error) {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return "", errors.New("memory: no JSON object in LLM output")
	}
	return s[start : end+1], nil
}

// parseTime accepts the date formats an LLM tends to produce and returns UTC, or nil if empty/unparseable.
func parseTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// fmtTime formats t for storage; nil becomes NULL.
func fmtTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

func qmarks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
