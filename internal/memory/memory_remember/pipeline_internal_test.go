package memory_remember

import (
	"reflect"
	"slices"
	"testing"

	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_pipeline"
)

// The stages of the pipeline are those of the run log: a stage that is not in relational.RememberStages could not be logged, and
// one that no pipeline step runs would never leave not started. They come in the same order, but for store, which a document runs
// twice (its parent, before the chunks are reconciled, and then each chunk) so that it appears twice in the pipeline.
func TestPipelineStagesAreTheRunLogs(t *testing.T) {
	var want []string
	for _, s := range relational.RememberStages {
		want = append(want, s.Name)
	}
	//------------------
	// MEMORY PIPELINE
	got := memory_pipeline.Names(rememberPipeline)
	//------------------
	if slices.Sort(got); !reflect.DeepEqual(got, sorted(want)) {
		t.Errorf("rememberPipeline has the stages %v, the run log %v", got, sorted(want))
	}
	//------------------
	// MEMORY PIPELINE
	got = memory_pipeline.Names(rememberPipeline)
	//------------------
	got, want = without(got, StageStore), without(want, StageStore)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rememberPipeline = %v, want %v (without store)", got, want)
	}
	//------------------
	// MEMORY PIPELINE
	if got, want := memory_pipeline.Names(reconcileSteps), []string{StageLoadCandidates, "resolve_edges", StageStore}; !reflect.DeepEqual(got, want) {
		t.Errorf("reconcileSteps = %v, want %v", got, want)
	}
	//------------------
}

func sorted(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }

func without(s []string, name string) []string {
	return slices.DeleteFunc(slices.Clone(s), func(n string) bool { return n == name })
}
