package memory_timeseries

import (
	"strings"
	"testing"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

func day(d int) time.Time { return time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC) }

func TestObservationText(t *testing.T) {
	o := Observed{Key: "AAPL.close", Name: "Apple close", Unit: "USD", Frequency: "1d", Subject: "Apple", SubjectType: "organization"}
	got := ObservationText(o, []graph.SeriesPoint{{TS: day(29), Value: 235.1}, {TS: day(30), Value: 237.6}, {TS: day(31), Value: 222}})
	for _, want := range []string{
		`"Apple close" (key AAPL.close, unit USD, frequency 1d) for Apple (organization), from 2025-01-29 to 2025-01-31 (3 points)`,
		"Apple close fell 5.57% in this period, from 235.1 USD on 2025-01-29 to 222 USD on 2025-01-31.",
		"highest value was 237.6 USD on 2025-01-30, its lowest 222 USD on 2025-01-31",
		"largest move between two consecutive points was -6.57%, from 237.6 USD on 2025-01-30 to 222 USD on 2025-01-31",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestObservationTextShapes(t *testing.T) {
	if ObservationText(Observed{Key: "k"}, nil) != "" {
		t.Error("no points must give no text")
	}
	one := ObservationText(Observed{Key: "k", Field: "close", Provider: "prices"}, []graph.SeriesPoint{{TS: day(1).Add(3 * time.Hour), Value: 5}})
	if !strings.Contains(one, "field close") || !strings.Contains(one, "from the provider prices") || !strings.Contains(one, "at 2025-01-01T03:00:00Z: one point, with the value 5.") {
		t.Errorf("one point: %s", one)
	}
	flat := ObservationText(Observed{Key: "k"}, []graph.SeriesPoint{{TS: day(1), Value: 100}, {TS: day(2), Value: 100.5}})
	if !strings.Contains(flat, "k was flat in this period: it went from 100 on 2025-01-01 to 100.5 on 2025-01-02 (+0.5%).") {
		t.Errorf("flat: %s", flat)
	}
	zero := ObservationText(Observed{Key: "k"}, []graph.SeriesPoint{{TS: day(1), Value: 0}, {TS: day(2), Value: 3}})
	if !strings.Contains(zero, "k rose in this period, from 0 on 2025-01-01 to 3 on 2025-01-02.") || strings.Contains(zero, "largest move") {
		t.Errorf("zero start: %s", zero)
	}
	if q := ObservationQuery(Observed{Key: "AAPL", Field: "close"}, day(1), day(3).Add(time.Hour)); q != "AAPL (close) from 2025-01-01 to 2025-01-03T01:00:00Z" {
		t.Errorf("query: %s", q)
	}
}
