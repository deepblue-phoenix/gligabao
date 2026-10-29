package memory_query

import (
	"reflect"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_pipeline"
)

// The stages of the pipeline are those of the run log, in the same order.
func TestPipelineStagesAreTheRunLogs(t *testing.T) {
	var want []string
	for _, s := range relational.QueryStages {
		want = append(want, s.Name)
	}
	//------------------
	// MEMORY PIPELINE
	if got := memory_pipeline.Names(queryPipeline); !reflect.DeepEqual(got, want) {
		t.Errorf("queryPipeline = %v, want %v", got, want)
	}
	//------------------
}
