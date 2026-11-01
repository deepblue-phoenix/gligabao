package memory_core

import (
	"context"
	"database/sql"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph"

	log "github.com/sirupsen/logrus"
)

// InContextEvidenceOnly is the stage of the in-context events of a retrieval-only query (WithEvidenceOnly): its entities and edges are
// returned to the caller as the context of the answer, and no LLM call has them in a prompt.
const InContextEvidenceOnly = "evidence_only"

// CountInContext is the incrementer every place of a pipeline that puts stored entities or edges into an LLM prompt calls, right before
// the call (and a retrieval-only query calls it for what it returns): it appends an event for each of them to the in-context log (see
// graph.CountInContext). src says where they are used: its Pipeline (graph.PipelineQuery or graph.PipelineRemember), Stage (the LLM
// stage, or InContextEvidenceOnly) and RunID (the run log of the call, which says who started it). The log tells which nodes and edges
// contribute a lot to what the LLM reasons from, and which hardly do. Counting is bookkeeping: a failure is logged and never fails the
// call, and a cancelled call still counts what it sent.
func CountInContext(ctx context.Context, d *sql.DB, src graph.InContextEvent, entityIDs, edgeIDs []int64) {
	if len(entityIDs)+len(edgeIDs) == 0 {
		return
	}
	if err := graph.CountInContext(context.WithoutCancel(ctx), d, src, entityIDs, edgeIDs); err != nil {
		log.WithError(err).WithFields(log.Fields{"pipeline": src.Pipeline, "stage": src.Stage}).
			Warn("memory: counting the entities and edges in the LLM context failed")
	}
}
