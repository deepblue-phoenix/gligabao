// Package experiments tracks eval experiments in the relational database.
//
// An Experiment is a named series of eval runs (table gb_experiments, type "knowledge"). Each execution of it is an ExperimentRun
// (gb_experiment_runs) that holds one ExperimentResult per eval (case) it executed (gb_experiment_results, with the knowledge-only
// columns in gb_knowledge_eval_meta; an eval is a case of a suite) and, for each of those, references to the ATIF trajectories
// of its remember and recall calls
// (gb_experiment_trajectories: the trajectory IDs, so the JSON files can be found again). The evals an experiment has
// executed over all its runs are listed in Experiment.Evals (gb_experiment_evals).
//
// Typical use, as in evals/knowledge/main.go:
//
//	t, err := experiments.Start(ctx, d, experiments.Options{Name: "baseline"})
//	for each eval { t.Record(ctx, experiments.CaseOutcome{...}) }
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
	EvalRef    = relational.EvalRef
	Run        = relational.ExperimentRun
	Result     = relational.ExperimentResult
	Trajectory = relational.ExperimentTrajectory
	Artifact   = relational.ExperimentArtifact
	EvalPoint  = relational.ExperimentEvalPoint
)

// Statuses of runs and evals.
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
	// Params describes how this run was invoked (suites directory, selected cases, ...). Stored with the run.
	Params map[string]any
}

// Tracker records one run of an experiment. Create it with Start, Record every eval's result and end it with
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

// CaseOutcome is what one eval produced.
type CaseOutcome struct {
	// Suite is the suite the eval is in (its file name without ".json") and Name the case name.
	Suite, Name string
	Answer      string
	// Problems are the failed checks. Err is why the eval could not be run to the end. Without either the eval passed.
	Problems []string
	// PassedChecks are the checks that held.
	PassedChecks []string
	Err          error
	// StartedAt and FinishedAt bound the eval; zero values are replaced by the time of Record.
	StartedAt, FinishedAt time.Time
	// Trajectories are the ATIF trajectories of the eval's calls in order (remember calls, then the recall), also
	// those collected before Err. TrajectoryDir is the directory the trajectory files were stored in ("" if they
	// were not stored); it is kept as a reference next to each trajectory ID.
	Trajectories  []*atif.Trajectory
	TrajectoryDir string
	// ArtifactIDs[i] are the IDs, in DatasetID, of the artifacts of Trajectories[i]: the artifact a remember call
	// stored, the artifacts the answer was sourced from for a recall. May be shorter than Trajectories (or nil).
	ArtifactIDs [][]int64
	// DatasetID is the dataset the eval ran in, which holds the knowledge graph it built; 0 = none.
	DatasetID int64
}

// Status is the status the eval is recorded with.
func (r CaseOutcome) Status() string {
	switch {
	case r.Err != nil:
		return StatusErrored
	case len(r.Problems) > 0:
		return StatusFailed
	}
	return StatusPassed
}

// Record stores the result of one eval in the run, with references to its trajectories, and adds the eval to the
// experiment's evals.
func (t *Tracker) Record(ctx context.Context, r CaseOutcome) (*Result, error) {
	if r.Suite == "" || r.Name == "" {
		return nil, errors.New("eval suite and name are required")
	}
	now := time.Now()
	if r.FinishedAt.IsZero() {
		r.FinishedAt = now
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = r.FinishedAt
	}
	e := Result{
		RunID:        t.Run.ID,
		Suite:        r.Suite,
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
	return relational.RecordExperimentResult(ctx, t.d, e)
}

// Finish totals the run's evals and ends the run: passed if every recorded eval passed, otherwise failed.
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

// Get returns the experiment called name with the evals its runs executed, or ErrNotFound.
func Get(ctx context.Context, d *sql.DB, name string) (*Experiment, error) {
	return relational.GetExperiment(ctx, d, name)
}

// List returns all experiments, oldest first (without their Evals).
func List(ctx context.Context, d *sql.DB) ([]Experiment, error) {
	return relational.ListExperiments(ctx, d)
}

// Runs returns the runs of the experiment, oldest first (without their evals; see GetRun).
func Runs(ctx context.Context, d *sql.DB, experimentID int64) ([]Run, error) {
	return relational.ListExperimentRuns(ctx, d, experimentID)
}

// EvalHistory returns the results of one eval (suite and case name) over the runs of the experiment, oldest run first:
// how its status progressed over time.
func EvalHistory(ctx context.Context, d *sql.DB, experimentID int64, suite, name string) ([]EvalPoint, error) {
	return relational.ListExperimentEvalHistory(ctx, d, experimentID, suite, name)
}

// GetRun returns a run with all its evals and their trajectory references, or ErrNotFound. The artifacts of each
// trajectory are read from the dataset its eval ran in.
func GetRun(ctx context.Context, d *sql.DB, runID int64) (*Run, error) {
	run, err := relational.GetExperimentRun(ctx, d, runID)
	if err != nil {
		return nil, err
	}
	for i := range run.Results {
		e := &run.Results[i]
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
					return nil, fmt.Errorf("load the artifacts of eval %d: %w", e.ID, err)
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

// ResultDataset returns the dataset an eval ran in (deleted or not), whose graph is the one the eval built, or
// ErrNotFound if the eval does not exist or recorded no dataset.
func ResultDataset(ctx context.Context, d *sql.DB, resultID int64) (*relational.Dataset, error) {
	return relational.GetExperimentResultDataset(ctx, d, resultID)
}

// FindTrajectory returns the reference to the trajectory with the given ATIF trajectory_id, or ErrNotFound. Its
// ResultID leads to the eval, and through it (Result.RunID) to the run and the experiment.
func FindTrajectory(ctx context.Context, d *sql.DB, trajectoryID string) (*Trajectory, error) {
	return relational.FindExperimentTrajectory(ctx, d, trajectoryID)
}
