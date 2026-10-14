package memory_timeseries

import (
	"math"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// TrendThresholdPct is the change (in percent, first to last point) beyond which a series trends up or down; a smaller
// change is flat.
const TrendThresholdPct = 1.0

// Stats summarizes the points of a series. It is computed in Go from the points (never by an LLM) and stored as JSON
// in gb_series.stats; it is what the rest of the system looks at instead of the raw points.
type Stats struct {
	Count   int       `json:"count"`
	FirstTS time.Time `json:"first_ts"`
	LastTS  time.Time `json:"last_ts"`
	First   float64   `json:"first"`
	Last    float64   `json:"last"`
	Min     float64   `json:"min"`
	MinTS   time.Time `json:"min_ts"`
	Max     float64   `json:"max"`
	MaxTS   time.Time `json:"max_ts"`
	Mean    float64   `json:"mean"`
	// Change is Last - First; PctChange is that relative to First (omitted when First is 0).
	Change    float64  `json:"change"`
	PctChange *float64 `json:"pct_change,omitempty"`
	// Trend is "up", "down" or "flat" (see TrendThresholdPct); "flat" for a single point.
	Trend string `json:"trend"`
	// LargestMove is the biggest relative change between two consecutive points; omitted for fewer than two points or
	// when no step starts from a non-zero value.
	LargestMove *Move `json:"largest_move,omitempty"`
}

// Move is a change between two consecutive points.
type Move struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	FromValue float64   `json:"from_value"`
	ToValue   float64   `json:"to_value"`
	Pct       float64   `json:"pct"`
}

// Summarize computes the Stats of points, which must be sorted oldest first. It returns nil for no points.
func Summarize(points []graph.SeriesPoint) *Stats {
	if len(points) == 0 {
		return nil
	}
	f, l := points[0], points[len(points)-1]
	s := &Stats{Count: len(points), FirstTS: f.TS, LastTS: l.TS, First: f.Value, Last: l.Value,
		Min: f.Value, MinTS: f.TS, Max: f.Value, MaxTS: f.TS, Change: l.Value - f.Value, Trend: "flat"}
	var sum, best float64
	for i, p := range points {
		sum += p.Value
		if p.Value < s.Min {
			s.Min, s.MinTS = p.Value, p.TS
		}
		if p.Value > s.Max {
			s.Max, s.MaxTS = p.Value, p.TS
		}
		if i == 0 || points[i-1].Value == 0 {
			continue
		}
		prev := points[i-1]
		if pct := (p.Value - prev.Value) / math.Abs(prev.Value) * 100; math.Abs(pct) > best {
			best = math.Abs(pct)
			s.LargestMove = &Move{From: prev.TS, To: p.TS, FromValue: prev.Value, ToValue: p.Value, Pct: pct}
		}
	}
	s.Mean = sum / float64(len(points))
	switch {
	case f.Value != 0:
		pct := s.Change / math.Abs(f.Value) * 100
		s.PctChange = &pct
		if pct > TrendThresholdPct {
			s.Trend = "up"
		} else if pct < -TrendThresholdPct {
			s.Trend = "down"
		}
	case s.Change > 0:
		s.Trend = "up"
	case s.Change < 0:
		s.Trend = "down"
	}
	return s
}
