package audits

import (
	evalsdb "github.com/deepblue-phoenix/gligabao/internal/evals/db"
	"github.com/deepblue-phoenix/gligabao/internal/training"
)

// CallJudge is the type of the runs (gb_inferences) the judge's LLM calls of an audit are recorded as.
const CallJudge = "training_audit_judge"

// Ops is the kind of run of the audits: how they are stored and read, for training.NewRunManager.
var Ops = training.RunOps{
	Kind:        evalsdb.TrainedModelKindAudit,
	CallType:    CallJudge,
	Record:      evalsdb.RecordTrainedModelAuditRun,
	Save:        evalsdb.SaveTrainedModelAuditRunBatch,
	Finish:      evalsdb.FinishTrainedModelAuditRun,
	FailRunning: evalsdb.FailRunningTrainedModelAuditRuns,
	Get:         evalsdb.GetTrainedModelAuditRun,
	List:        evalsdb.ListTrainedModelAuditRuns,
	Cases:       evalsdb.ListTrainedModelAuditCases,
}
