package memory

// Temporal event pipeline, modelled on Graphiti's bi-temporal edges.
//
// Every event carries two timelines:
//   - event time:  ValidAt / InvalidAt  - when the event holds in the real world (nil = unknown / still true);
//   - system time: CreatedAt / ExpiredAt - when we learned about it / when newer knowledge superseded it.
//
// Pipeline (run by Remember):
//  1. ExtractEvents: the LLM pulls events out of the text, resolving relative dates ("last March")
//     against the memory's reference time.
//  2. loadCandidates + resolveEvents: existing live events sharing a participant are compared with the new
//     ones; the LLM flags duplicates and contradictions.
//  3. Store: duplicates are merged; for each contradiction the older event is invalidated
//     (InvalidAt = start of the newer one, ExpiredAt = now), as Graphiti does for edges.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ivan/gligabao/internal/ai"
)

const (
	timeLayout    = "2006-01-02T15:04:05Z"
	maxCandidates = 50 // existing events shown to the LLM for dedup / contradiction checks
)

//---------------------------------------------------------------------------------------------

// Event is something that happened or was true over a period of time, involving some entities.
type Event struct {
	ID           int64      `json:"id,omitempty"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Participants []string   `json:"participants"`
	ValidAt      *time.Time `json:"valid_at,omitempty"`   // when it started being true
	InvalidAt    *time.Time `json:"invalid_at,omitempty"` // when it stopped being true
	CreatedAt    *time.Time `json:"created_at,omitempty"` // when we learned about it
	ExpiredAt    *time.Time `json:"expired_at,omitempty"` // when we learned it was superseded
}

// EventPlan is the outcome of comparing new events against existing ones.
type EventPlan struct {
	Duplicates     map[int]int64 // index of new event -> id of the existing event it repeats
	Contradictions []Contradiction
}

// Contradiction says that the existing event can't hold together with the new event.
type Contradiction struct {
	New      int
	Existing int64
}

type rawEvent struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Participants []string `json:"participants"`
	ValidAt      string   `json:"valid_at"`
	InvalidAt    string   `json:"invalid_at"`
}

//---------------------------------------------------------------------------------------------

const eventSystemPrompt = `You extract temporal events from text.

An event is something that happened, started, ended, changed or was planned, and that has (or implies) a point or period in time: "Alice joined Acme in March 2023", "Bob moved from Rome to Paris last summer", "The launch is scheduled for 5 May 2025".

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"events": [{"name": "...", "description": "...", "participants": ["..."], "valid_at": "...", "invalid_at": "..."}]}

Rules:
- "name" is a short lowercase snake_case label, e.g. joined_company, moved_city.
- "description" is a self-contained sentence stating the fact, using full names instead of pronouns.
- "participants" are the names of the entities involved. Use the exact names from <entities> when they match; otherwise use the most complete name.
- "valid_at" is the ISO 8601 UTC datetime (e.g. 2023-03-01T00:00:00Z) at which the event started or became true. "invalid_at" is the datetime at which it stopped being true, if the text says so. Use null when the text gives no information; never guess.
- Resolve relative expressions ("yesterday", "last year", "two weeks ago") against <reference_time>. For a date with partial precision use its earliest instant (a month -> the 1st at 00:00:00Z, a year -> 1 January).
- If the text says something stopped ("Alice left Acme in June 2024"), the event describes the thing that stopped ("Alice worked at Acme") and "invalid_at" is the stop time.
- Do not invent events. Facts with no temporal meaning at all (static attributes) are not events. If there is nothing to extract, return {"events": []}.`

// ExtractEvents asks the LLM to pull temporal events out of text. ref anchors relative dates; ex (may be nil)
// supplies the entities found in the same text so participants use consistent names.
func ExtractEvents(ctx context.Context, llm LLM, text string, ref time.Time, ex *Extraction) ([]Event, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "<reference_time>\n%s\n</reference_time>\n\n<entities>\n", ref.UTC().Format(timeLayout))
	if ex != nil {
		for _, e := range ex.Entities {
			fmt.Fprintf(&b, "- %s (%s)\n", e.Name, normType(e.Type))
		}
	}
	fmt.Fprintf(&b, "</entities>\n\n<text>\n%s\n</text>", text)

	temp := 0.0
	resp, err := llm.Complete(ctx, ai.Request{
		System:      eventSystemPrompt,
		Messages:    []ai.Message{{Role: ai.RoleUser, Content: b.String()}},
		MaxTokens:   4096,
		Temperature: &temp,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == "max_tokens" {
		return nil, errors.New("memory: LLM event output truncated; text too large")
	}
	js, err := jsonObject(resp.Text)
	if err != nil {
		return nil, err
	}
	var out struct {
		Events []rawEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("memory: decode LLM event output: %w", err)
	}
	return normalizeEvents(out.Events), nil
}

// normalizeEvents cleans raw LLM events: trims text, drops empty ones, parses dates and de-duplicates
// participants. A stop time that is not after the start time is discarded as inconsistent.
func normalizeEvents(raw []rawEvent) []Event {
	evs := []Event{}
	for _, r := range raw {
		ev := Event{
			Name:         normRelation(r.Name),
			Description:  strings.TrimSpace(r.Description),
			Participants: []string{},
			ValidAt:      parseTime(r.ValidAt),
			InvalidAt:    parseTime(r.InvalidAt),
		}
		if ev.Description == "" {
			continue
		}
		if ev.Name == "" {
			ev.Name = "event"
		}
		seen := map[string]bool{}
		for _, p := range r.Participants {
			if p = strings.TrimSpace(p); p != "" && !seen[key(p)] {
				seen[key(p)] = true
				ev.Participants = append(ev.Participants, p)
			}
		}
		if ev.ValidAt != nil && ev.InvalidAt != nil && !ev.InvalidAt.After(*ev.ValidAt) {
			ev.InvalidAt = nil
		}
		evs = append(evs, ev)
	}
	return evs
}

//---------------------------------------------------------------------------------------------

const resolveSystemPrompt = `You compare NEW events with EXISTING events in a temporal knowledge base.

New events are listed as "[n] description (valid_at -> invalid_at)" in <new_events>; existing ones as "[id] description (valid_at -> invalid_at)" in <existing_events>. "?" means unknown.

Return ONLY a JSON object (no prose, no markdown fences) of this shape:
{"duplicates": [{"new": 0, "existing": 12}], "contradictions": [{"new": 1, "existing": 7}]}

Rules:
- "duplicates": a new event states exactly the same fact as an existing event (wording may differ).
- "contradictions": an existing event can no longer be true at the same time as a new event, because the new one replaces, ends or conflicts with it (e.g. "Alice works at Acme" vs "Alice joined Globex"; "Bob lives in Rome" vs "Bob moved to Paris"). Events that merely both involve the same entities, or can both be true together, are NOT contradictions.
- A pair is never both a duplicate and a contradiction.
- "new" is the number from <new_events>; "existing" is the ID from <existing_events>. Use only numbers and IDs that appear there.
- If there are no duplicates or contradictions, return empty arrays.`

// resolveEvents compares new events with existing candidates. Exact repeats (same description and
// participants) are detected without the LLM; the LLM is only called for what remains.
func resolveEvents(ctx context.Context, llm LLM, evs, cands []Event) (*EventPlan, error) {
	plan := &EventPlan{Duplicates: map[int]int64{}}

	var rest []int // indices of new events still undecided
	for i, ev := range evs {
		dup := false
		for _, c := range cands {
			if sameEvent(ev, c) {
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
	b.WriteString("<new_events>\n")
	for _, i := range rest {
		fmt.Fprintf(&b, "[%d] %s %s\n", i, evs[i].Description, span(evs[i].ValidAt, evs[i].InvalidAt))
	}
	b.WriteString("</new_events>\n\n<existing_events>\n")
	known := map[int64]bool{}
	for _, c := range cands {
		known[c.ID] = true
		fmt.Fprintf(&b, "[%d] %s %s\n", c.ID, c.Description, span(c.ValidAt, c.InvalidAt))
	}
	b.WriteString("</existing_events>")

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

// sameEvent reports whether two events state the same fact among the same participants.
func sameEvent(a, b Event) bool {
	return key(a.Description) == key(b.Description) && participantKey(a.Participants) == participantKey(b.Participants)
}

func participantKey(names []string) string {
	ks := make([]string, 0, len(names))
	for _, n := range names {
		ks = append(ks, key(n))
	}
	sort.Strings(ks)
	return strings.Join(ks, "\x00")
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

// loadCandidates returns live (not expired) stored events sharing at least one participant with evs,
// newest first.
func loadCandidates(ctx context.Context, d *sql.DB, evs []Event) ([]Event, error) {
	var keys []any
	seen := map[string]bool{}
	for _, ev := range evs {
		for _, p := range ev.Participants {
			if k := key(p); !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT id FROM gb_events
		WHERE expired_at IS NULL AND id IN (
			SELECT ep.event_id FROM gb_event_participants ep
			JOIN gb_entities e ON e.id = ep.entity_id
			WHERE e.name_key IN (`+qmarks(len(keys))+`))
		ORDER BY id DESC LIMIT ?`, append(keys, maxCandidates)...)
	if err != nil {
		return nil, fmt.Errorf("load event candidates: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	out := make([]Event, 0, len(ids))
	for _, id := range ids {
		ev, err := loadEvent(ctx, d, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *ev)
	}
	return out, nil
}

// querier is implemented by *sql.DB and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// loadEvent reads one event with its participants.
func loadEvent(ctx context.Context, q querier, id int64) (*Event, error) {
	ev := Event{ID: id, Participants: []string{}}
	var valid, invalid, created, expired sql.NullString
	err := q.QueryRowContext(ctx, `SELECT name, description, valid_at, invalid_at, created_at, expired_at FROM gb_events WHERE id = ?`, id).
		Scan(&ev.Name, &ev.Description, &valid, &invalid, &created, &expired)
	if err != nil {
		return nil, fmt.Errorf("load event %d: %w", id, err)
	}
	ev.ValidAt, ev.InvalidAt, ev.CreatedAt, ev.ExpiredAt = parseTime(valid.String), parseTime(invalid.String), parseTime(created.String), parseTime(expired.String)

	rows, err := q.QueryContext(ctx, `
		SELECT e.name FROM gb_event_participants ep JOIN gb_entities e ON e.id = ep.entity_id
		WHERE ep.event_id = ? ORDER BY e.id`, id)
	if err != nil {
		return nil, fmt.Errorf("load event participants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		ev.Participants = append(ev.Participants, n)
	}
	return &ev, rows.Err()
}

//---------------------------------------------------------------------------------------------

// storeEvents writes evs inside tx according to plan and returns the resulting events (new ones, or the
// existing ones they duplicate) plus the IDs of events that were invalidated. participant maps an
// entity name to its ID, creating the entity if needed.
func storeEvents(ctx context.Context, tx *sql.Tx, memID int64, ref, now time.Time, evs []Event, plan *EventPlan,
	participant func(name string) (int64, error)) ([]Event, []int64, error) {
	if plan == nil {
		plan = &EventPlan{}
	}
	nowS := now.UTC().Format(timeLayout)
	ids := make([]int64, len(evs)) // stored ID for each new event
	batch := map[string]int64{}    // repeats inside this memory
	var order []int64
	inOrder := map[int64]bool{}

	for i, ev := range evs {
		if existing, ok := plan.Duplicates[i]; ok {
			// Same fact seen again: keep the stored event, just fill dates it was missing.
			if _, err := tx.ExecContext(ctx, `
				UPDATE gb_events SET valid_at = COALESCE(valid_at, ?), invalid_at = COALESCE(invalid_at, ?) WHERE id = ?`,
				fmtTime(ev.ValidAt), fmtTime(ev.InvalidAt), existing); err != nil {
				return nil, nil, fmt.Errorf("merge duplicate event: %w", err)
			}
			ids[i] = existing
			if !inOrder[existing] {
				inOrder[existing] = true
				order = append(order, existing)
			}
			continue
		}
		bk := key(ev.Description) + "\x01" + participantKey(ev.Participants)
		if id, ok := batch[bk]; ok {
			ids[i] = id
			continue
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO gb_events (name, description, description_key, valid_at, invalid_at, created_at, memory_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ev.Name, ev.Description, key(ev.Description), fmtTime(ev.ValidAt), fmtTime(ev.InvalidAt), nowS, memID)
		if err != nil {
			return nil, nil, fmt.Errorf("insert event: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, nil, err
		}
		for _, p := range ev.Participants {
			eid, err := participant(p)
			if err != nil {
				return nil, nil, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO gb_event_participants (event_id, entity_id) VALUES (?, ?)`, id, eid); err != nil {
				return nil, nil, fmt.Errorf("insert event participant: %w", err)
			}
		}
		ids[i] = id
		batch[bk] = id
		inOrder[id] = true
		order = append(order, id)
	}

	var invalidated []int64
	for _, c := range plan.Contradictions {
		if c.New < 0 || c.New >= len(evs) {
			continue
		}
		inv, err := contradict(ctx, tx, ids[c.New], c.Existing, ref, nowS)
		if err != nil {
			return nil, nil, err
		}
		invalidated = append(invalidated, inv...)
	}

	out := make([]Event, 0, len(order))
	for _, id := range order {
		ev, err := loadEvent(ctx, tx, id)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *ev)
	}
	return out, invalidated, nil
}

// contradict applies Graphiti's invalidation rule to a new event and an existing one that conflicts with it.
// The event that started earlier is the one that ended: it gets invalid_at = start of the later event and
// expired_at = now. Events whose periods don't overlap are left alone. It returns the IDs invalidated.
func contradict(ctx context.Context, tx *sql.Tx, newID, oldID int64, ref time.Time, nowS string) ([]int64, error) {
	if newID == oldID {
		return nil, nil
	}
	nw, err := loadEvent(ctx, tx, newID)
	if err != nil {
		return nil, err
	}
	old, err := loadEvent(ctx, tx, oldID)
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
		// The new information is about an earlier time than what we already know: the new event is the one superseded.
		loser, endsAt = newID, *old.ValidAt
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gb_events SET invalid_at = ?, expired_at = ? WHERE id = ?`,
		endsAt.UTC().Format(timeLayout), nowS, loser); err != nil {
		return nil, fmt.Errorf("invalidate event: %w", err)
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
