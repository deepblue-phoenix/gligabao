package model

import "time"

// The timeseries records (see docs/timeseries.md). A series is kept apart from the text artifacts (gb_artifacts) and
// the graph, like an image: its points are rows of gb_series_points, and it is linked to artifacts, entities and
// edges by anchors (gb_series_anchors).

// Anchor types: what a series can be anchored to.
const (
	AnchorArtifact = "artifact"
	AnchorEntity   = "entity"
	AnchorEdge     = "edge"
)

// Series sources: where the points of a series came from.
const (
	SeriesSupplied = "supplied" // sent by the caller
	SeriesFetched  = "fetched"  // fetched from a provider (not implemented yet)
)

// NewSeries is a series to insert, or whose metadata to update: a series is identified inside its dataset by
// (Provider, Key, Field).
type NewSeries struct {
	DatasetID int64
	Key       string // the caller's identifier of the series, e.g. "AAPL.close"
	Field     string // which value of the key the points are, e.g. "close" (default "value")
	Name      string
	Unit      string
	Frequency string // a label ("1d"), not a constraint
	Source    string // SeriesSupplied or SeriesFetched
	Provider  string // "" for a supplied series
	// Definition is JSON that says how to fetch the series again (empty for a supplied one).
	Definition string
	CreatedAt  time.Time
}

// SeriesPoint is one value of a series at a time.
type SeriesPoint struct {
	TS    time.Time
	Value float64
}

// Series is a stored series (without its points).
type Series struct {
	ID         int64
	DatasetID  int64
	Key        string
	Field      string
	Name       string
	Unit       string
	Frequency  string
	Source     string
	Provider   string
	Definition string
	FirstTS    *time.Time // nil while the series has no points
	LastTS     *time.Time
	PointCount int
	// Stats is the JSON summary of all the points (see memory_timeseries.Stats); "" while there are none.
	Stats     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewSeriesAnchor links a series to an artifact, entity or edge (Type, ID). The anchor is unique per series and
// target: anchoring again replaces Role and the window.
type NewSeriesAnchor struct {
	SeriesID int64
	Type     string // AnchorArtifact, AnchorEntity or AnchorEdge
	ID       int64  // the ID of the artifact, entity or edge
	Role     string
	// WindowStart and WindowEnd are the slice of the series that matters to the anchor; nil for the whole series.
	WindowStart, WindowEnd *time.Time
	CreatedAt              time.Time
}

// SeriesAnchor is a stored anchor.
type SeriesAnchor struct {
	ID          int64
	SeriesID    int64
	Type        string
	TargetID    int64
	Role        string
	WindowStart *time.Time
	WindowEnd   *time.Time
	CreatedAt   time.Time
}
