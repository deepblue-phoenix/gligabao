// Package experiments tracks eval experiments in the relational database.
//
// An Experiment is a named series of runs (table gb_experiments, type "knowledge"). Each execution of it is an ExperimentRun
// (gb_experiment_runs) that holds one ExperimentCaseResult per case it executed (gb_experiment_case_results, with the knowledge-only
// columns in gb_knowledge_case_meta). A case is one input plus what is checked (and metadata) and belongs to an eval. Each result
// has references to the ATIF trajectories of its remember and recall calls (gb_experiment_trajectories: the trajectory IDs, so the
// JSON files can be found again). The cases an experiment has executed over all its runs are listed in Experiment.Cases
// (gb_experiment_cases).
//
// Typical use, as in internal/evals/knowledge/main.go:
//
//	t, err := experiments.Start(ctx, d, experiments.Options{Name: "baseline"})
//	for each case { t.Record(ctx, experiments.CaseOutcome{...}) }
//	run, err := t.Finish(ctx)
package experiments

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/ai/atif"
	"github.com/deepblue-phoenix/gligabao/internal/db/graph"
	"github.com/deepblue-phoenix/gligabao/internal/db/relational"
)

// Types stored in the database (see internal/db/relational/model).
type (
	Experiment = relational.Experiment
	CaseRef    = relational.CaseRef
	Run        = relational.ExperimentRun
	CaseResult = relational.ExperimentCaseResult
	Trajectory = relational.ExperimentTrajectory
	Artifact   = relational.ExperimentArtifact
)

// Statuses of runs and cases.
const (
	StatusRunning = relational.ExperimentRunning
	StatusPassed  = relational.ExperimentPassed
	StatusFailed  = relational.ExperimentFailed
	StatusErrored = relational.ExperimentErrored
)

// ErrRunNotRunning is returned by Record when the run is already finished.
var (
	ErrRunNotRunning = relational.ErrRunNotRunning
)

// ErrNotFound is returned by the lookups below when the experiment, run or trajectory does not exist.
var ErrNotFound = relational.ErrNotFound

// agentPrefix is what the memory package puts before the operation in the ATIF agent name ("gligabao-remember").
const agentPrefix = "gligabao-"

//---------------------------------------------------------------------------------------------

// Options describe the experiment and run to Start.
type Options struct {
	// Name identifies the experiment; runs with the same name belong to the same experiment. Required.
	Name string
	// Description and Config are stored when the experiment is created; an existing experiment keeps its own.
	Description string
	Config      map[string]any
	// Params describes how this run was invoked (evals directory, selected cases, ...). Stored with the run.
	Params map[string]any
}

// Tracker records one run of an experiment. Create it with Start, Record every case's result and end it with
// Finish. It is not safe for concurrent use.
type Tracker struct {
	d *sql.DB
	// Experiment is the experiment the run belongs to.
	Experiment *Experiment
	// Run is the run being recorded, as stored when it was started.
	Run *Run
}

// Start gets or creates the experiment called opts.Name and starts a new run of it.
func Start(ctx context.Context, d *sql.DB, opts Options) (*Tracker, error) {
	if strings.TrimSpace(opts.Name) == "" {
		return nil, errors.New("experiment name is required")
	}
	exp, err := relational.EnsureExperiment(ctx, d, opts.Name, opts.Description, opts.Config)
	if err != nil {
		return nil, err
	}
	run, err := relational.StartExperimentRun(ctx, d, exp.ID, opts.Params)
	if err != nil {
		return nil, err
	}
	return &Tracker{d: d, Experiment: exp, Run: run}, nil
}

// CaseOutcome is what one case produced.
type CaseOutcome struct {
	// Eval is the eval the case is in (the name set in its JSON file) and Name the case name.
	Eval, Name string
	Answer     string
	// Problems are the failed checks. Err is why the case could not be run to the end. Without either the case passed.
	Problems []string
	// PassedChecks are the checks that held.
	PassedChecks []string
	Err          error
	// StartedAt and FinishedAt bound the case; zero values are replaced by the time of Record.
	StartedAt, FinishedAt time.Time
	// Trajectories are the ATIF trajectories of the case's calls in order (remember calls, then the recall), also
	// those collected before Err. TrajectoryDir is the directory the trajectory files were stored in ("" if they
	// were not stored); it is kept as a reference next to each trajectory ID.
	Trajectories  []*atif.Trajectory
	TrajectoryDir string
	// ArtifactIDs[i] are the IDs, in DatasetID, of the artifacts of Trajectories[i]: the artifact a remember call
	// stored, the artifacts the answer was sourced from for a recall. May be shorter than Trajectories (or nil).
	ArtifactIDs [][]int64
	// DatasetID is the dataset the case ran in, which holds the knowledge graph it built; 0 = none.
	DatasetID int64
}

// Status is the status the case is recorded with.
func (r CaseOutcome) Status() string {
	switch {
	case r.Err != nil:
		return StatusErrored
	case len(r.Problems) > 0:
		return StatusFailed
	}
	return StatusPassed
}

// Record stores the result of one case in the run, with references to its trajectories, and adds the case to the
// experiment's cases.
func (t *Tracker) Record(ctx context.Context, r CaseOutcome) (*CaseResult, error) {
	if r.Eval == "" || r.Name == "" {
		return nil, errors.New("case eval and name are required")
	}
	now := time.Now()
	if r.FinishedAt.IsZero() {
		r.FinishedAt = now
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = r.FinishedAt
	}
	e := CaseResult{
		RunID:        t.Run.ID,
		Eval:         r.Eval,
		Name:         r.Name,
		Status:       r.Status(),
		Answer:       r.Answer,
		Problems:     r.Problems,
		PassedChecks: r.PassedChecks,
		StartedAt:    relational.Normalize(r.StartedAt),
		FinishedAt:   relational.Normalize(r.FinishedAt),
	}
	if r.DatasetID != 0 {
		e.DatasetID = &r.DatasetID
	}
	if r.Err != nil {
		e.Error = r.Err.Error()
	}
	for i, traj := range r.Trajectories {
		ref, err := TrajectoryRef(traj, r.TrajectoryDir)
		if err != nil {
			return nil, err
		}
		if i < len(r.ArtifactIDs) {
			ref.ArtifactIDs = r.ArtifactIDs[i]
		}
		e.Steps += ref.Steps
		e.CostUSD += ref.CostUSD
		e.Trajectories = append(e.Trajectories, ref)
	}
	return relational.RecordExperimentCaseResult(ctx, t.d, e)
}

// Finish totals the run's cases and ends the run: passed if every recorded case passed, otherwise failed.
func (t *Tracker) Finish(ctx context.Context) (*Run, error) {
	run, err := relational.FinishExperimentRun(ctx, t.d, t.Run.ID)
	if err != nil {
		return nil, err
	}
	t.Run = run
	return run, nil
}

// TrajectoryRef builds the database reference to an ATIF trajectory: its ID, the operation that made it (taken from
// the agent name, "gligabao-remember" or "gligabao-recall"), its size and cost, and the path of its file in dir
// (empty dir = not stored).
func TrajectoryRef(traj *atif.Trajectory, dir string) (Trajectory, error) {
	if traj == nil || traj.TrajectoryID == "" {
		return Trajectory{}, errors.New("trajectory has no trajectory_id")
	}
	kind := strings.TrimPrefix(traj.Agent.Name, agentPrefix)
	if kind != relational.TrajectoryRemember && kind != relational.TrajectoryRecall {
		return Trajectory{}, fmt.Errorf("trajectory %s: unknown agent %q", traj.TrajectoryID, traj.Agent.Name)
	}
	ref := Trajectory{Kind: kind, TrajectoryID: traj.TrajectoryID, AgentName: traj.Agent.Name, Steps: len(traj.Steps)}
	if fm := traj.FinalMetrics; fm != nil {
		ref.Steps, ref.CostUSD = fm.TotalSteps, fm.TotalCostUSD
	}
	if dir != "" {
		ref.Path = filepath.Join(dir, traj.TrajectoryID+".json")
	}
	return ref, nil
}

//---------------------------------------------------------------------------------------------

// Get returns the experiment called name with the cases its runs executed, or ErrNotFound.
func Get(ctx context.Context, d *sql.DB, name string) (*Experiment, error) {
	return relational.GetExperiment(ctx, d, name)
}

// List returns all experiments, oldest first (without their Cases).
func List(ctx context.Context, d *sql.DB) ([]Experiment, error) {
	return relational.ListExperiments(ctx, d)
}

// Runs returns the runs of the experiment, oldest first (without their cases; see GetRun).
func Runs(ctx context.Context, d *sql.DB, experimentID int64) ([]Run, error) {
	return relational.ListExperimentRuns(ctx, d, experimentID)
}

// CaseHistory returns the results of one case (eval and case name) over the runs of the experiment, oldest run first:
// how its status progressed over time. Each result has its RunID; the trajectories are not loaded.
func CaseHistory(ctx context.Context, d *sql.DB, experimentID int64, eval, name string) ([]CaseResult, error) {
	return relational.ListExperimentCaseHistory(ctx, d, experimentID, eval, name)
}

// GetRun returns a run with all its cases and their trajectory references, or ErrNotFound. The artifacts of each
// trajectory are read from the dataset its case ran in.
func GetRun(ctx context.Context, d *sql.DB, runID int64) (*Run, error) {
	run, err := relational.GetExperimentRun(ctx, d, runID)
	if err != nil {
		return nil, err
	}
	for i := range run.CaseResults {
		e := &run.CaseResults[i]
		var all map[int64]graph.Artifact // the dataset's artifacts, loaded when a trajectory needs them
		for j := range e.Trajectories {
			t := &e.Trajectories[j]
			t.Artifacts = []Artifact{}
			if len(t.ArtifactIDs) == 0 || e.DatasetID == nil {
				continue
			}
			if all == nil {
				list, err := graph.ListArtifacts(ctx, d, *e.DatasetID)
				if err != nil {
					return nil, fmt.Errorf("load the artifacts of case %d: %w", e.ID, err)
				}
				all = make(map[int64]graph.Artifact, len(list))
				for _, a := range list {
					all[a.ID] = a
				}
			}
			for _, id := range t.ArtifactIDs {
				if a, ok := all[id]; ok {
					t.Artifacts = append(t.Artifacts, Artifact{ID: a.ID, Kind: a.Kind, Text: a.Text, ValidAt: a.ValidAt, CreatedAt: a.CreatedAt})
				}
			}
		}
	}
	return run, nil
}

// CaseResultDataset returns the dataset a case ran in (deleted or not), whose graph is the one the case built, or
// ErrNotFound if the case does not exist or recorded no dataset.
func CaseResultDataset(ctx context.Context, d *sql.DB, caseResultID int64) (*relational.Dataset, error) {
	return relational.GetExperimentCaseResultDataset(ctx, d, caseResultID)
}

// FindTrajectory returns the reference to the trajectory with the given ATIF trajectory_id, or ErrNotFound. Its
// CaseResultID leads to the case, and through it (CaseResult.RunID) to the run and the experiment.
func FindTrajectory(ctx context.Context, d *sql.DB, trajectoryID string) (*Trajectory, error) {
	return relational.FindExperimentTrajectory(ctx, d, trajectoryID)
}
