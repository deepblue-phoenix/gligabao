package memory_timeseries

import (
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

func pts(vs ...float64) []graph.SeriesPoint {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]graph.SeriesPoint, len(vs))
	for i, v := range vs {
		out[i] = graph.SeriesPoint{TS: t0.AddDate(0, 0, i), Value: v}
	}
	return out
}

func TestSummarize(t *testing.T) {
	if Summarize(nil) != nil {
		t.Error("no points must give nil")
	}
	s := Summarize(pts(100, 110, 99, 120))
	if s.Count != 4 || s.First != 100 || s.Last != 120 || s.Min != 99 || s.Max != 120 || s.Mean != 107.25 || s.Change != 20 || s.Trend != "up" {
		t.Errorf("stats: %+v", s)
	}
	if s.PctChange == nil || *s.PctChange != 20 {
		t.Errorf("pct change: %v", s.PctChange)
	}
	// 99 -> 120 is +21.2%, bigger than 110 -> 99 (-10%) and 100 -> 110 (+10%).
	if m := s.LargestMove; m == nil || m.FromValue != 99 || m.ToValue != 120 || m.Pct < 21 || m.Pct > 21.3 {
		t.Errorf("largest move: %+v", m)
	}
	if s := Summarize(pts(100, 100.5)); s.Trend != "flat" {
		t.Errorf("0.5%% change must be flat: %+v", s)
	}
	if s := Summarize(pts(5)); s.Trend != "flat" || s.LargestMove != nil || s.Count != 1 {
		t.Errorf("single point: %+v", s)
	}
	// From zero there is no percent, but the direction is known; a step from zero is not a move.
	if s := Summarize(pts(0, 3)); s.PctChange != nil || s.Trend != "up" || s.LargestMove != nil {
		t.Errorf("from zero: %+v", s)
	}
	if s := Summarize(pts(100, 50)); s.Trend != "down" || s.LargestMove.Pct != -50 {
		t.Errorf("down: %+v %+v", s, s.LargestMove)
	}
}
