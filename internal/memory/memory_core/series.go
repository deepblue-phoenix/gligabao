package memory_core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// The timeseries a caller supplies, to be anchored to what a remember call stores (see docs/timeseries.md). The types
// are the JSON of the request; ResolveSeries validates and normalizes them.

// ErrInvalidSeries means a supplied timeseries is malformed.
var ErrInvalidSeries = errors.New("invalid timeseries")

// The limits of a supplied timeseries.
const (
	MaxSeriesPerCall     = 20      // series in one request
	MaxSeriesPoints      = 100_000 // points in one series
	MaxSeriesAnchors     = 20      // anchors of one series
	MaxSeriesKeyLen      = 256     // bytes of key, name and the names in a selector
	MaxSeriesLabelLen    = 64      // bytes of field, unit, frequency and role
	DefaultSeriesField   = "value"
	OnUnmatchedCreate    = "create"
	OnUnmatchedIgnore    = "ignore"
	OnUnmatchedError     = "error"
	defaultOnUnmatchedID = OnUnmatchedError
)

// SeriesPoint is one value at a time. In JSON it is a pair: ["2025-01-29T00:00:00Z", 235.1].
type SeriesPoint struct {
	TS    time.Time
	Value float64
}

func (p SeriesPoint) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{relational.FormatTime(p.TS), p.Value})
}

func (p *SeriesPoint) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil || len(pair) != 2 {
		return errors.New(`a point must be a pair [time, value]`)
	}
	var ts string
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return errors.New("the time of a point must be an RFC 3339 string")
	}
	t, err := relational.ParseTimeStrict(ts)
	if err != nil {
		return fmt.Errorf("the time %q of a point must be RFC 3339, e.g. 2025-01-31T09:00:00Z", ts)
	}
	var v float64
	if err := json.Unmarshal(pair[1], &v); err != nil {
		return errors.New("the value of a point must be a number")
	}
	p.TS, p.Value = t, v
	return nil
}

// SeriesEntity selects an entity by name and, optionally, type (any type if empty).
type SeriesEntity struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// SeriesEdge selects a fact by the names of its endpoints and its relation.
type SeriesEdge struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Target   string `json:"target"`
}

// SeriesAnchor says what a series is anchored to. Exactly one of Artifact, ArtifactID, Entity, EntityID, Edge and
// EdgeID is set. Entity and Edge are selectors, resolved only after the text was extracted and stored (the entities
// do not exist when the request is made); the IDs name things that are already stored.
type SeriesAnchor struct {
	// Artifact anchors to the artifact being created (the default when a series has no anchors). Not for the
	// standalone endpoint, which creates none.
	Artifact   bool          `json:"artifact,omitempty"`
	ArtifactID int64         `json:"artifact_id,omitempty"`
	Entity     *SeriesEntity `json:"entity,omitempty"`
	EntityID   int64         `json:"entity_id,omitempty"`
	Edge       *SeriesEdge   `json:"edge,omitempty"`
	EdgeID     int64         `json:"edge_id,omitempty"`

	// Role (optional) says what the series is to the anchor, e.g. "price".
	Role string `json:"role,omitempty"`
	// Window (optional) is [start, end], RFC 3339: the slice of the series that matters to the anchor.
	Window []string `json:"window,omitempty"`
	// OnUnmatched says what to do when the selector (or ID) matches nothing: "create" (an entity selector only;
	// the default for it: the entity is created), "ignore" (skip the anchor; the default for an edge selector) or
	// "error" (fail; the default for an ID).
	OnUnmatched string `json:"on_unmatched,omitempty"`

	// WindowStart and WindowEnd are Window, parsed (set by ResolveSeries).
	WindowStart *time.Time `json:"-"`
	WindowEnd   *time.Time `json:"-"`
}

// Describe names the anchor for messages.
func (a SeriesAnchor) Describe() string {
	switch {
	case a.Artifact:
		return "the artifact"
	case a.ArtifactID != 0:
		return fmt.Sprintf("artifact %d", a.ArtifactID)
	case a.Entity != nil:
		return fmt.Sprintf("entity %q", a.Entity.Name)
	case a.EntityID != 0:
		return fmt.Sprintf("entity %d", a.EntityID)
	case a.Edge != nil:
		return fmt.Sprintf("fact %s -%s-> %s", a.Edge.Source, a.Edge.Relation, a.Edge.Target)
	case a.EdgeID != 0:
		return fmt.Sprintf("edge %d", a.EdgeID)
	}
	return "nothing"
}

// SeriesSpec is a timeseries the caller supplies.
type SeriesSpec struct {
	// Key identifies the series in the dataset (with Field): sending the same key again adds to the series, and a
	// point at a time the series has replaces its value. Required.
	Key string `json:"key"`
	// Field says which value of the key the points are (default "value"); several fields of one key, such as the
	// open, high, low and close of a price, are several series.
	Field     string `json:"field,omitempty"`
	Name      string `json:"name,omitempty"` // default: the key
	Unit      string `json:"unit,omitempty"`
	Frequency string `json:"frequency,omitempty"` // a label such as "1d", not a constraint
	// Points are [time, value] pairs, at least one, in any order (ResolveSeries sorts them).
	Points []SeriesPoint `json:"points"`
	// Anchors (optional) say what the series is anchored to. Without any, a series sent with remember is anchored to the
	// artifact it creates.
	Anchors []SeriesAnchor `json:"anchors,omitempty"`
}

// ResolveSeries validates the series a caller supplied and returns a normalized copy: strings trimmed, defaults
// applied (field "value", name = key, on_unmatched), points sorted by time with one value per time (the last one
// given wins) and normalized to the precision of the database, windows parsed. artifact says whether the call creates
// an artifact the series can be anchored to (remember does; the standalone endpoint does not). It returns
// ErrInvalidSeries for anything malformed. Resolving a resolved series changes nothing.
func ResolveSeries(in []SeriesSpec, artifact bool) ([]SeriesSpec, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxSeriesPerCall {
		return nil, fmt.Errorf("%w: at most %d series per request", ErrInvalidSeries, MaxSeriesPerCall)
	}
	out := make([]SeriesSpec, 0, len(in))
	seen := map[string]bool{}
	for i, s := range in {
		r, err := resolveSeries(s, artifact)
		if err != nil {
			return nil, fmt.Errorf("%w: series[%d]: %v", ErrInvalidSeries, i, err)
		}
		if id := r.Key + "\x00" + r.Field; seen[id] {
			return nil, fmt.Errorf("%w: series[%d]: key %q with field %q is given twice", ErrInvalidSeries, i, r.Key, r.Field)
		} else {
			seen[id] = true
		}
		out = append(out, r)
	}
	return out, nil
}

func resolveSeries(s SeriesSpec, artifact bool) (SeriesSpec, error) {
	var err error
	r := SeriesSpec{}
	if r.Key, err = label(s.Key, "key", MaxSeriesKeyLen, true); err != nil {
		return r, err
	}
	if r.Field, err = label(s.Field, "field", MaxSeriesLabelLen, false); err != nil {
		return r, err
	}
	if r.Field == "" {
		r.Field = DefaultSeriesField
	}
	if r.Name, err = label(s.Name, "name", MaxSeriesKeyLen, false); err != nil {
		return r, err
	}
	if r.Name == "" {
		r.Name = r.Key
	}
	if r.Unit, err = label(s.Unit, "unit", MaxSeriesLabelLen, false); err != nil {
		return r, err
	}
	if r.Frequency, err = label(s.Frequency, "frequency", MaxSeriesLabelLen, false); err != nil {
		return r, err
	}

	//--------------------------------------
	// POINTS
	if len(s.Points) == 0 {
		return r, errors.New("points are required (at least one)")
	}
	if len(s.Points) > MaxSeriesPoints {
		return r, fmt.Errorf("at most %d points per series (use several requests for more)", MaxSeriesPoints)
	}
	byTime := make(map[int64]int, len(s.Points)) // unix nano -> index in r.Points
	r.Points = make([]SeriesPoint, 0, len(s.Points))
	for _, p := range s.Points {
		if p.TS.IsZero() {
			return r, errors.New("a point has no time")
		}
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			return r, errors.New("a point value must be a finite number")
		}
		p.TS = relational.Normalize(p.TS)
		if i, ok := byTime[p.TS.UnixNano()]; ok {
			r.Points[i] = p // the last value given for a time wins
			continue
		}
		byTime[p.TS.UnixNano()] = len(r.Points)
		r.Points = append(r.Points, p)
	}
	sort.Slice(r.Points, func(i, j int) bool { return r.Points[i].TS.Before(r.Points[j].TS) })

	//--------------------------------------
	// ANCHORS
	if len(s.Anchors) > MaxSeriesAnchors {
		return r, fmt.Errorf("at most %d anchors per series", MaxSeriesAnchors)
	}
	for i, a := range s.Anchors {
		ra, err := resolveAnchor(a, artifact)
		if err != nil {
			return r, fmt.Errorf("anchors[%d]: %w", i, err)
		}
		r.Anchors = append(r.Anchors, ra)
	}
	return r, nil
}

func resolveAnchor(a SeriesAnchor, artifact bool) (SeriesAnchor, error) {
	var err error
	r := SeriesAnchor{Artifact: a.Artifact, ArtifactID: a.ArtifactID, EntityID: a.EntityID, EdgeID: a.EdgeID}
	set := 0
	for _, b := range []bool{a.Artifact, a.ArtifactID != 0, a.Entity != nil, a.EntityID != 0, a.Edge != nil, a.EdgeID != 0} {
		if b {
			set++
		}
	}
	if set != 1 {
		return r, errors.New("exactly one of artifact, artifact_id, entity, entity_id, edge and edge_id is required")
	}
	if a.ArtifactID < 0 || a.EntityID < 0 || a.EdgeID < 0 {
		return r, errors.New("an ID must be positive")
	}
	if a.Artifact && !artifact {
		return r, errors.New("artifact: this request creates no artifact to anchor to")
	}

	switch {
	case a.Entity != nil:
		e := SeriesEntity{}
		if e.Name, err = label(a.Entity.Name, "entity.name", MaxSeriesKeyLen, true); err != nil {
			return r, err
		}
		if e.Type, err = label(a.Entity.Type, "entity.type", MaxSeriesLabelLen, false); err != nil {
			return r, err
		}
		e.Type = strings.ToLower(e.Type)
		r.Entity = &e
	case a.Edge != nil:
		e := SeriesEdge{}
		if e.Source, err = label(a.Edge.Source, "edge.source", MaxSeriesKeyLen, true); err != nil {
			return r, err
		}
		if e.Target, err = label(a.Edge.Target, "edge.target", MaxSeriesKeyLen, true); err != nil {
			return r, err
		}
		rel, err := label(a.Edge.Relation, "edge.relation", MaxSeriesKeyLen, true)
		if err != nil {
			return r, err
		}
		e.Relation = strings.ReplaceAll(strings.ToLower(strings.Join(strings.Fields(rel), " ")), " ", "_") // as the relations of the edges are
		r.Edge = &e
	}
	if r.Role, err = label(a.Role, "role", MaxSeriesLabelLen, false); err != nil {
		return r, err
	}

	switch u := strings.ToLower(strings.TrimSpace(a.OnUnmatched)); u {
	case "":
		switch {
		case r.Entity != nil:
			r.OnUnmatched = OnUnmatchedCreate
		case r.Edge != nil:
			r.OnUnmatched = OnUnmatchedIgnore
		default:
			r.OnUnmatched = defaultOnUnmatchedID
		}
	case OnUnmatchedCreate:
		if r.Entity == nil {
			return r, errors.New(`on_unmatched "create" only applies to an entity selector`)
		}
		r.OnUnmatched = u
	case OnUnmatchedIgnore, OnUnmatchedError:
		r.OnUnmatched = u
	default:
		return r, fmt.Errorf(`on_unmatched must be "create", "ignore" or "error"`)
	}

	switch len(a.Window) {
	case 0:
	case 2:
		var w [2]time.Time
		for i, s := range a.Window {
			t, err := relational.ParseTimeStrict(strings.TrimSpace(s))
			if err != nil {
				return r, fmt.Errorf("window must be two RFC 3339 times, e.g. 2025-01-01T00:00:00Z")
			}
			w[i] = relational.Normalize(t)
		}
		if w[1].Before(w[0]) {
			return r, errors.New("window ends before it starts")
		}
		r.WindowStart, r.WindowEnd = &w[0], &w[1]
		r.Window = []string{relational.FormatTime(w[0]), relational.FormatTime(w[1])}
	default:
		return r, errors.New("window must be [start, end]")
	}
	return r, nil
}

// label trims s and checks it: at most max bytes, no control characters, and non-empty if required.
func label(s, field string, max int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" && required {
		return "", fmt.Errorf("%s is required", field)
	}
	if len(s) > max {
		return "", fmt.Errorf("%s is longer than %d bytes", field, max)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%s has control characters", field)
		}
	}
	return s, nil
}

// SeriesSummary describes series for a trajectory: no points, only how many.
func SeriesSummary(series []SeriesSpec) []map[string]any {
	out := make([]map[string]any, 0, len(series))
	for _, s := range series {
		out = append(out, map[string]any{"key": s.Key, "field": s.Field, "points": len(s.Points), "anchors": len(s.Anchors)})
	}
	return out
}
