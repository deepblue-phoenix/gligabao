package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
	"github.com/deepblue-phoenix/gligabao/internal/training/db/model"
)

//---------------------------------------------------------------------------------------------
// The configuration and results of training job runs (gb_training_job_runs_meta, see sqlite/sql/schema.sql for what the functions do)

const trainingJobRunMetaColumns = `run_id, user_id, model_type, epochs, lr, batch_size, seed, dataset_sha256, num_train_examples, num_validation_examples,
	validation_fraction, split_seed, final_val_loss, meta_schema, meta, created_at, updated_at`

func scanTrainingJobRunMeta(s rowScanner) (*model.TrainingJobRunMeta, error) {
	var m model.TrainingJobRunMeta
	var epochs, batch, trainN, valN, seed, splitSeed sql.NullInt64
	var lr, valFraction, valLoss sql.NullFloat64
	var meta string
	if err := s.Scan(&m.RunID,
		&m.UserID,
		&m.ModelType,
		&epochs,
		&lr,
		&batch,
		&seed,
		&m.DatasetSHA256,
		&trainN,
		&valN,
		&valFraction,
		&splitSeed,
		&valLoss,
		&m.MetaSchema,
		&meta,
		&m.CreatedAt,
		&m.UpdatedAt); err != nil {
		return nil, err
	}
	m.Epochs, m.BatchSize = intPtr(epochs), intPtr(batch)
	m.NumTrainExamples, m.NumValidationExamples = intPtr(trainN), intPtr(valN)
	m.LR, m.ValidationFraction, m.FinalValLoss = floatPtr(lr), floatPtr(valFraction), floatPtr(valLoss)
	m.Seed, m.SplitSeed = int64Ptr(seed), int64Ptr(splitSeed)
	m.Meta = json.RawMessage(meta)
	m.CreatedAt, m.UpdatedAt = m.CreatedAt.UTC(), m.UpdatedAt.UTC()
	return &m, nil
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func int64Ptr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func floatPtr(f sql.NullFloat64) *float64 {
	if !f.Valid {
		return nil
	}
	return &f.Float64
}

// UpsertTrainingJobRunMeta stores the meta of a run, replacing the one it has (m.CreatedAt is kept, m.UpdatedAt is set now). The run must
// exist.
func UpsertTrainingJobRunMeta(ctx context.Context,
	d *sql.DB,
	m model.TrainingJobRunMeta) error {
	now := rpostgres.Now()
	_, err := d.ExecContext(ctx,
		`
		INSERT INTO gb_training_job_runs_meta (`+trainingJobRunMetaColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (run_id) DO UPDATE SET
			user_id = excluded.user_id, model_type = excluded.model_type, epochs = excluded.epochs, lr = excluded.lr,
			batch_size = excluded.batch_size, seed = excluded.seed, dataset_sha256 = excluded.dataset_sha256,
			num_train_examples = excluded.num_train_examples, num_validation_examples = excluded.num_validation_examples,
			validation_fraction = excluded.validation_fraction, split_seed = excluded.split_seed,
			final_val_loss = excluded.final_val_loss, meta_schema = excluded.meta_schema, meta = excluded.meta,
			updated_at = excluded.updated_at`,
		m.RunID,
		m.UserID,
		m.ModelType,
		m.Epochs,
		m.LR,
		m.BatchSize,
		m.Seed,
		m.DatasetSHA256,
		m.NumTrainExamples,
		m.NumValidationExamples,
		m.ValidationFraction,
		m.SplitSeed,
		m.FinalValLoss,
		m.MetaSchema,
		m.MetaJSON(),
		now,
		now)
	if err != nil {
		return fmt.Errorf("upsert training job run meta: %w", err)
	}
	return nil
}

// GetTrainingJobRunMeta returns the meta of a run, or model.ErrNotFound.
func GetTrainingJobRunMeta(ctx context.Context,
	d *sql.DB,
	runID int64) (*model.TrainingJobRunMeta, error) {
	m, err := scanTrainingJobRunMeta(d.QueryRowContext(ctx,
		`SELECT `+trainingJobRunMetaColumns+` FROM gb_training_job_runs_meta WHERE run_id = $1`,
		runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	return m, err
}
