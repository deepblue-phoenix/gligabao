package memory_timeseries

// The text of a series observation (see docs/timeseries.md): a derived artifact of kind series_observation. The text is
// generated deterministically, in Go, from the stats of the points of a window: no LLM writes it, so it can only say what
// the numbers say. Remember then extracts it, reconciles it and stores it like any other text, which is how a series
// reaches the graph as facts.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// Observed describes the series an observation is of.
type Observed struct {
	Key, Field, Name, Unit, Frequency string
	// Provider is the provider that fetched the series; "" for a supplied one.
	Provider string
	// Subject and SubjectType name the entity the series is about (the entity a series is anchored to by name, or the one
	// a fetch was made for); "" if it is about none in particular.
	Subject, SubjectType string
}

// ObservationQuery says what an observation is of: the series (key, field) and the period of its points. It is stored
// in the query column of the artifact.
func ObservationQuery(o Observed, from, to time.Time) string {
	q := o.Key
	if o.Field != "" && o.Field != "value" {
		q += " (" + o.Field + ")"
	}
	return q + " from " + observationTime(from) + " to " + observationTime(to)
}

// ObservationText describes points (sorted oldest first) of the series o in a few plain sentences, from their Stats. It
// returns "" for no points. Times are dates when every point is at midnight UTC, else RFC 3339.
func ObservationText(o Observed, points []graph.SeriesPoint) string {
	s := Summarize(points)
	if s == nil {
		return ""
	}
	name := o.Name
	if name == "" {
		name = o.Key
	}
	unit := ""
	if o.Unit != "" {
		unit = " " + o.Unit
	}
	val := func(v float64) string { return numberText(v) + unit }
	day := dayFormat(points)
	at := func(t time.Time) string { return t.UTC().Format(day) }

	var b strings.Builder
	fmt.Fprintf(&b, "Observation of the timeseries %q (key %s", name, o.Key)
	if o.Field != "" && o.Field != "value" {
		fmt.Fprintf(&b, ", field %s", o.Field)
	}
	if o.Unit != "" {
		fmt.Fprintf(&b, ", unit %s", o.Unit)
	}
	if o.Frequency != "" {
		fmt.Fprintf(&b, ", frequency %s", o.Frequency)
	}
	b.WriteString(")")
	if o.Subject != "" {
		fmt.Fprintf(&b, " for %s", o.Subject)
		if o.SubjectType != "" && o.SubjectType != "other" {
			fmt.Fprintf(&b, " (%s)", o.SubjectType)
		}
	}
	if o.Provider != "" {
		fmt.Fprintf(&b, ", from the provider %s", o.Provider)
	}
	if s.Count == 1 {
		fmt.Fprintf(&b, ", at %s: one point, with the value %s.", at(s.FirstTS), val(s.First))
		return b.String()
	}
	fmt.Fprintf(&b, ", from %s to %s (%d points).\n", at(s.FirstTS), at(s.LastTS), s.Count)

	switch {
	case s.Trend == "flat" && s.PctChange != nil:
		fmt.Fprintf(&b, "%s was flat in this period: it went from %s on %s to %s on %s (%s).\n",
			name, val(s.First), at(s.FirstTS), val(s.Last), at(s.LastTS), pctText(*s.PctChange))
	case s.Trend == "flat":
		fmt.Fprintf(&b, "%s was flat in this period at %s.\n", name, val(s.First))
	case s.PctChange != nil:
		fmt.Fprintf(&b, "%s %s %s in this period, from %s on %s to %s on %s.\n",
			name, verb(s.Trend), strings.TrimLeft(pctText(*s.PctChange), "+-"), val(s.First), at(s.FirstTS), val(s.Last), at(s.LastTS))
	default: // it started from 0: there is no relative change
		fmt.Fprintf(&b, "%s %s in this period, from %s on %s to %s on %s.\n",
			name, verb(s.Trend), val(s.First), at(s.FirstTS), val(s.Last), at(s.LastTS))
	}
	fmt.Fprintf(&b, "Its highest value was %s on %s, its lowest %s on %s, and its average %s.",
		val(s.Max), at(s.MaxTS), val(s.Min), at(s.MinTS), val(s.Mean))
	if m := s.LargestMove; m != nil {
		fmt.Fprintf(&b, "\nThe largest move between two consecutive points was %s, from %s on %s to %s on %s.",
			pctText(m.Pct), val(m.FromValue), at(m.From), val(m.ToValue), at(m.To))
	}
	return b.String()
}

func verb(trend string) string {
	if trend == "down" {
		return "fell"
	}
	return "rose"
}

// pctText formats a percentage with its sign and at most two decimals: "-6.57%", "+2%".
func pctText(p float64) string {
	r := math.Round(p*100) / 100
	s := strconv.FormatFloat(math.Abs(r), 'f', -1, 64) + "%"
	if r < 0 {
		return "-" + s
	}
	return "+" + s
}

// numberText formats a value: up to four decimals, or four significant digits for a tiny one.
func numberText(v float64) string {
	if a := math.Abs(v); a != 0 && a < 1e-4 {
		return strconv.FormatFloat(v, 'g', 4, 64)
	}
	return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
}

// dayFormat is the time layout for points: a date if all of them are at midnight UTC, else RFC 3339.
func dayFormat(points []graph.SeriesPoint) string {
	for _, p := range points {
		if t := p.TS.UTC(); t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
			return time.RFC3339
		}
	}
	return "2006-01-02"
}

func observationTime(t time.Time) string {
	t = t.UTC()
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format(time.RFC3339)
}
