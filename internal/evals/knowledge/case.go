package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/chunk"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
	"github.com/deepblue-phoenix/gligabao/internal/evals/core"
	"github.com/deepblue-phoenix/gligabao/internal/memory/memory_core"
	"github.com/deepblue-phoenix/gligabao/internal/sys/server"
)

// EvalUser owns the eval datasets.
const EvalUser = "eval"

// DatasetName is the dataset a case remembers into and recalls from: one per case and eval run ("<case>_run<evalRunID>"), so runs
// are isolated from each other and the graph of every case of every run stays in the eval database (and can be viewed in the admin
// UI). Characters a dataset name does not allow become "_"; the case part is cut so the whole name fits the 64-byte limit.
func DatasetName(caseName string, evalRunID core.EvalRunID) string {
	suffix := fmt.Sprintf("_run%d", evalRunID)
	b := []byte(caseName)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' ||
			i > 0 && (ch == '.' || ch == '_' || ch == '-')
		if !ok {
			b[i] = '_'
		}
	}
	if max := 64 - len(suffix); len(b) > max {
		b = b[:max]
	}
	return string(b) + suffix
}

// outcome is what one case produced.
type outcome struct {
	Answer   string
	Problems []string // failed checks
	// PassedChecks are the checks that held.
	PassedChecks []string
	// DatasetID is the dataset the case ran in (0 if it was never created); it holds the graph the case built.
	DatasetID int64
	// ArtifactIDs[i] are the IDs, in DatasetID, of the artifacts of Trajectories[i]: the artifact a remember call stored, the
	// artifacts a recall's evidence was sourced from.
	ArtifactIDs [][]int64
	// Trajectories are the ATIF trajectories returned by the eval endpoints: one per remember, then the recall's.
	Trajectories []*atif.Trajectory
	// Chunks is the total number of chunks the remember calls reported (0 if no text was chunked).
	Chunks int
}

// cost is the total LLM cost of all trajectories, in USD, and their number of steps.
func (o *outcome) cost() (usd float64, steps int) {
	for _, t := range o.Trajectories {
		if t.FinalMetrics != nil {
			usd += t.FinalMetrics.TotalCostUSD
			steps += t.FinalMetrics.TotalSteps
		}
	}
	return usd, steps
}

// runCase executes one case in the eval database through the eval endpoints, in a dataset of its own (see DatasetName): remember
// (once per text, in order), the checks of what was stored, then recall. input has the texts read in. The outcome is returned even
// when err is set, holding whatever was collected before the failure.
func (x *Executor) runCase(ctx context.Context, name string, in core.KnowledgeCaseDefMeta, input core.KnowledgeInput, evalRunID core.EvalRunID, llm memory_core.LLM) (out *outcome, err error) {
	out = &outcome{}
	database := x.DB

	caseModels, err := memory_core.ResolveModels(in.Models)
	if err != nil {
		return out, fmt.Errorf("models: %w", err)
	}
	models := x.Models.Merge(caseModels)
	rememberReasoning, recallReasoning, err := mergeReasoning(x.Reasoning, in.Reasoning)
	if err != nil {
		return out, fmt.Errorf("reasoning: %w", err)
	}

	if in.ChunkSize != 0 {
		// The handlers read the chunking settings from the DB they are given: use a copy with the case's size.
		cp := *database
		cp.Chunking = chunk.Config{Size: in.ChunkSize, OverlapPercent: chunk.DefaultOverlapPercent}
		if err := cp.Chunking.Validate(); err != nil {
			return out, fmt.Errorf("chunk_size: %w", err)
		}
		database = &cp
	}
	mgrCtx, stopMgr := context.WithCancel(context.Background()) // stops the remember manager of the handler
	defer stopMgr()
	srv := httptest.NewServer(server.NewHandler(mgrCtx, database, llm, x.Embedder, nil, x.WebSearch, nil, nil, nil, true, true, false))
	defer srv.Close()

	dataset := DatasetName(name, evalRunID)
	defer func() {
		// Recorded with the result, so the graph can be read back from the dataset later (also after a failure).
		if id, err := relational.DatasetID(context.WithoutCancel(ctx), database.Relational, EvalUser, dataset); err == nil {
			out.DatasetID = id
		}
	}()

	// check records a held check (pass) or a failed one (fail).
	check := func(ok bool, pass, fail string) {
		if ok {
			out.PassedChecks = append(out.PassedChecks, pass)
		} else {
			out.Problems = append(out.Problems, fail)
		}
	}
	var parents []int64            // artifact IDs of the remembered texts that were chunked
	var reported []rememberedChunk // the chunks the remember calls reported

	for i, m := range input.Remember {
		body := map[string]any{"user_id": EvalUser, "text": m.Text, "dataset": dataset}
		if m.Kind != "" {
			body["kind"] = m.Kind
		}
		if m.ReferenceTime != "" {
			body["reference_time"] = m.ReferenceTime
		}
		if len(models) > 0 {
			body["models"] = models
		}
		if len(rememberReasoning) > 0 {
			body["reasoning"] = rememberReasoning
		}
		if m.WebSearch {
			body["web_search"] = true
		}
		if len(m.Timeseries) > 0 {
			body["timeseries"] = m.Timeseries
		}
		var rem struct {
			ArtifactID int64             `json:"artifact_id"`
			Chunks     []rememberedChunk `json:"chunks"`
			Trajectory *atif.Trajectory  `json:"trajectory"`
		}
		if err := post(ctx, srv.URL+"/v1/gb/eval/remember", body, &rem); err != nil {
			return out, fmt.Errorf("remember #%d: %w", i+1, err)
		}
		if rem.Trajectory == nil {
			return out, fmt.Errorf("remember #%d: response has no trajectory (is the server in eval mode?)", i+1)
		}
		out.Trajectories = append(out.Trajectories, rem.Trajectory)
		out.ArtifactIDs = append(out.ArtifactIDs, []int64{rem.ArtifactID})
		if len(rem.Chunks) > 0 {
			parents = append(parents, rem.ArtifactID)
			reported = append(reported, rem.Chunks...)
		}
	}
	out.Chunks = len(reported)
	edges, err := graph.ListEdges(ctx, database.Graph, EvalUser, dataset)
	if err != nil {
		return out, err
	}
	tstr := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return relational.FormatTime(*t)
	}
	if x.Verbose {
		fmt.Fprintln(x.out(), "  stored edges:")
		for _, e := range edges {
			fmt.Fprintf(x.out(), "    [%d] %s --%s--> %s valid_at=%s invalid_at=%q expired_at=%q\n",
				e.ID, e.Source, e.Relation, e.Target, tstr(e.ValidAt), tstr(e.InvalidAt), tstr(e.ExpiredAt))
		}
	}
	for _, want := range in.ExpectEdges {
		var found *graph.Edge
		for i, e := range edges {
			if strings.EqualFold(e.Source, want.Source) && strings.EqualFold(e.Target, want.Target) {
				found = &edges[i]
				break
			}
		}
		desc := want.Source + " -> " + want.Target
		if found == nil {
			check(false, "", desc+": no edge stored")
			continue
		}
		check(true, desc+": edge stored", "")
		if want.ValidAt != "" {
			check(tstr(found.ValidAt) == want.ValidAt, fmt.Sprintf("%s: valid_at = %s", desc, want.ValidAt),
				fmt.Sprintf("%s: valid_at = %s, want %s", desc, tstr(found.ValidAt), want.ValidAt))
		}
		if want.InvalidAt != "" {
			check(tstr(found.InvalidAt) == want.InvalidAt, fmt.Sprintf("%s: invalid_at = %s", desc, want.InvalidAt),
				fmt.Sprintf("%s: invalid_at = %q, want %s", desc, tstr(found.InvalidAt), want.InvalidAt))
		}
		check(want.Ended == (found.InvalidAt != nil), fmt.Sprintf("%s: ended = %v", desc, want.Ended),
			fmt.Sprintf("%s: ended = %v, want %v", desc, found.InvalidAt != nil, want.Ended))
		if want.Ended {
			check(found.ExpiredAt != nil, desc+": ended and expired_at is set", desc+": ended but expired_at is not set")
		}
	}

	if in.ExpectMinChunks > 0 {
		if err := checkChunks(ctx, database, dataset, in.ExpectMinChunks, parents, reported, check); err != nil {
			return out, fmt.Errorf("check chunks: %w", err)
		}
	}
	if in.ExpectWebSearches > 0 {
		if err := checkWebSearches(ctx, database, dataset, in.ExpectWebSearches, check); err != nil {
			return out, fmt.Errorf("check web searches: %w", err)
		}
	}
	if len(in.ExpectSeries) > 0 {
		var w io.Writer
		if x.Verbose {
			w = x.out()
		}
		if err := checkSeries(ctx, database, dataset, in.ExpectSeries, w, check); err != nil {
			return out, fmt.Errorf("check series: %w", err)
		}
	}

	if strings.TrimSpace(input.Query) == "" {
		return out, nil // ingestion only
	}

	var rec struct {
		Answer   string `json:"answer"`
		Evidence struct {
			Edges []struct {
				ID         int64  `json:"id"`
				ArtifactID *int64 `json:"artifact_id"`
			} `json:"edges"`
		} `json:"evidence"`
		Trajectory *atif.Trajectory `json:"trajectory"`
	}
	recallBody := map[string]any{"user_id": EvalUser, "query": input.Query, "dataset": dataset}
	if recallReasoning != "" {
		recallBody["reasoning"] = recallReasoning
	}
	if err := post(ctx, srv.URL+"/v1/gb/eval/recall", recallBody, &rec); err != nil {
		return out, fmt.Errorf("recall: %w", err)
	}
	if rec.Trajectory == nil {
		return out, errors.New("recall: response has no trajectory (is the server in eval mode?)")
	}
	out.Answer = rec.Answer
	out.Trajectories = append(out.Trajectories, rec.Trajectory)
	cited := map[int64]*int64{} // cited edge ID -> artifact that created it
	for _, e := range rec.Evidence.Edges {
		cited[e.ID] = e.ArtifactID
	}
	ids, err := sourceArtifactIDs(ctx, database, dataset, cited)
	if err != nil {
		// Not worth failing the case for: the recall itself worked.
		fmt.Fprintf(os.Stderr, "warning: %s: collect the recall's source artifacts: %v\n", name, err)
	}
	out.ArtifactIDs = append(out.ArtifactIDs, ids)
	return out, nil
}

// trajectoryRef builds the database reference to an ATIF trajectory: its ID, the operation that made it (taken from the agent name,
// "gligabao-remember" or "gligabao-recall"), its size and cost, and the path of its file in dir (empty dir = not stored).
func trajectoryRef(seq int, traj *atif.Trajectory, dir string, artifactIDs []int64) (core.KnowledgeTrajectory, error) {
	if traj == nil || traj.TrajectoryID == "" {
		return core.KnowledgeTrajectory{}, errors.New("trajectory has no trajectory_id")
	}
	kind := strings.TrimPrefix(traj.Agent.Name, agentPrefix)
	if kind != KindRemember && kind != KindRecall {
		return core.KnowledgeTrajectory{}, fmt.Errorf("trajectory %s: unknown agent %q", traj.TrajectoryID, traj.Agent.Name)
	}
	ref := core.KnowledgeTrajectory{Seq: seq, Kind: kind, TrajectoryID: traj.TrajectoryID, AgentName: traj.Agent.Name, Steps: len(traj.Steps), ArtifactIDs: slices.Clone(artifactIDs)}
	if fm := traj.FinalMetrics; fm != nil {
		ref.Steps, ref.CostUSD = fm.TotalSteps, fm.TotalCostUSD
	}
	if dir != "" {
		ref.Path = filepath.Join(dir, traj.TrajectoryID+".json")
	}
	return ref, nil
}

// The kinds of the trajectories of a case (core.KnowledgeTrajectory.Kind).
const (
	KindRemember = "remember"
	KindRecall   = "recall"
)

// agentPrefix is what the memory package puts before the operation in the ATIF agent name ("gligabao-remember").
const agentPrefix = "gligabao-"
