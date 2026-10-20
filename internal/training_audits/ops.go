package training_audits

import (
	"fmt"

	"github.com/deepblue-phoenix/gligabao/internal/sys/config"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits/db/sqlite"
)

const (
	// CallJudge is the type of the runs (gb_inferences) that the judge's LLM calls of an audit are recorded as; StageJudge is
	// their stage.
	CallJudge  = "training_audit_judge"
	StageJudge = "judge"
)

// Ops is what the Manager needs of the world around it that depends on the database: the functions that store and read the
// audits (the backend) and the type its judge calls are recorded as.
type Ops struct {
	Store    db.Store
	CallType string
}

// The Ops of each backend.
var (
	SQLiteOps   = Ops{Store: sqlite.Store, CallType: CallJudge}
	PostgresOps = Ops{Store: postgres.Store, CallType: CallJudge}
)

// OpsFor returns the Ops of a database backend (config.DBBackendSQLite or config.DBBackendPostgres; empty is sqlite).
func OpsFor(backend string) (Ops, error) {
	switch backend {
	case "", config.DBBackendSQLite:
		return SQLiteOps, nil
	case config.DBBackendPostgres:
		return PostgresOps, nil
	}
	return Ops{}, fmt.Errorf("unknown training audits db backend %q", backend)
}
