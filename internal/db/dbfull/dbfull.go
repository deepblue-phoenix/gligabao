// Package dbfull opens the databases with every table gligabao has: the knowledge base (package db) and the tables of training, the
// evals, the training audits and the research agent runs. The server and the eval runners use it. A program that needs only the
// knowledge base (cmd/gligabao_agent) opens package db directly and so does not link the code of those features.
package dbfull

import (
	"github.com/deepblue-phoenix/gligabao/internal/agent_research"
	"github.com/deepblue-phoenix/gligabao/internal/db"
	"github.com/deepblue-phoenix/gligabao/internal/evals"
	trainingdb "github.com/deepblue-phoenix/gligabao/internal/training/db"
	"github.com/deepblue-phoenix/gligabao/internal/training_audits"
)

// Schemas are the schemas of every feature, in the order they must be created: the training tables come before the evals', which
// reference the trained models.
var Schemas = []db.Schema{trainingdb.Open, evals.Open, training_audits.Open, agent_research.Open}

// Open is db.Open with Schemas added to cfg.Schemas.
func Open(cfg db.Config) (*db.DB, error) {
	cfg.Schemas = append(cfg.Schemas[:len(cfg.Schemas):len(cfg.Schemas)], Schemas...)
	return db.Open(cfg)
}
