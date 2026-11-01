package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/deepblue-phoenix/gligabao/internal/db/graph/model"
	rpostgres "github.com/deepblue-phoenix/gligabao/internal/db/relational/postgres"
)

// InsertArtifactClasses stores one record in gb_artifact_classes for each class, all with the same run (inferenceID, 0 for none), in one
// transaction, and returns them in the order of classes.
func InsertArtifactClasses(ctx context.Context, d *sql.DB, dsID, artifactID, inferenceID int64, classes []string, createdAt time.Time) ([]model.ArtifactClass, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("insert artifact classes: %w", err)
	}
	defer tx.Rollback()
	createdAt = rpostgres.Normalize(createdAt)
	out := make([]model.ArtifactClass, 0, len(classes))
	for _, c := range classes {
		var id int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO gb_artifact_classes (dataset_id, artifact_id, class, inference_id, created_at)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id`, dsID, artifactID, c, nullIfZero(inferenceID), createdAt).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("insert artifact class: %w", err)
		}
		out = append(out, model.ArtifactClass{ID: id, DatasetID: dsID, ArtifactID: artifactID, Class: c, InferenceID: inferenceID, CreatedAt: createdAt})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListArtifactClasses returns the class records of the artifacts of the dataset, by artifact ID then record ID.
func ListArtifactClasses(ctx context.Context, d *sql.DB, dsID int64) ([]model.ArtifactClass, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, dataset_id, artifact_id, class, COALESCE(inference_id, 0), created_at
		FROM gb_artifact_classes
		WHERE dataset_id = $1
		ORDER BY artifact_id, id`, dsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ArtifactClass{}
	for rows.Next() {
		var c model.ArtifactClass
		if err := rows.Scan(&c.ID, &c.DatasetID, &c.ArtifactID, &c.Class, &c.InferenceID, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt = c.CreatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ClassNames returns the distinct class names used in the dataset, the most used first (then by name), at most limit.
func ClassNames(ctx context.Context, d *sql.DB, dsID int64, limit int) ([]string, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT class FROM gb_artifact_classes
		WHERE dataset_id = $1
		GROUP BY class
		ORDER BY COUNT(*) DESC, class
		LIMIT $2`, dsID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
