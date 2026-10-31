package memory_temporal

import (
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
)

// ApplyReference gives an extracted edge its event-time span: a fact whose start the text does not state inherits ref,
// the time of the artifact that states it, and the span is then made consistent (see FixSpan).
func ApplyReference(e *graph.Edge, ref time.Time) {
	if e.ValidAt == nil {
		v := ref.UTC()
		e.ValidAt = &v
	}
	FixSpan(e)
}

// FixSpan makes the event-time span of an extracted edge consistent. The text said the fact ended (InvalidAt), which is
// more reliable than a start the model defaulted or guessed, so the end is kept and the start is moved back to it when it
// is later ("Alice left Acme in June 2024" with no start: ValidAt would be the reference time, after the end). The span is
// then empty (ValidAt == InvalidAt): the start is unknown and the fact ended at that instant. ValidAt must be set.
func FixSpan(e *graph.Edge) {
	if e.InvalidAt != nil && e.InvalidAt.Before(*e.ValidAt) {
		v := *e.InvalidAt
		e.ValidAt = &v
	}
}
